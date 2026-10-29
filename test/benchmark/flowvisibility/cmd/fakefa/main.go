// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// fakefa serves a synthetic FlowStreamService, standing in for the Flow Aggregator in the flow
// visibility benchmark. See test/benchmark/flowvisibility/README.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"antrea.io/antrea-ui/pkg/flowpb"
	"antrea.io/antrea-ui/test/benchmark/flowvisibility/fakefa"
)

func main() {
	scaleFile := flag.String("scale", "", "path to a JSON-encoded Scale; defaults to the small preset")
	listen := flag.String("listen", ":14740", "address of the gRPC server")
	statsListen := flag.String("stats-listen", ":8080", "address of the HTTP server answering /stats")
	certFile := flag.String("tls-cert", "", "TLS certificate of the gRPC server")
	keyFile := flag.String("tls-key", "", "TLS key of the gRPC server")
	flag.Parse()

	scale := fakefa.DefaultScale()
	if *scaleFile != "" {
		data, err := os.ReadFile(*scaleFile)
		if err != nil {
			log.Fatalf("Failed to read scale: %v", err)
		}
		if err := json.Unmarshal(data, &scale); err != nil {
			log.Fatalf("Failed to parse scale: %v", err)
		}
	}
	gen, err := fakefa.NewGenerator(scale)
	if err != nil {
		log.Fatalf("Invalid scale: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ready := make(chan struct{})
	go gen.Run(ctx, ready)
	<-ready
	log.Printf("Synthetic cluster: %s", gen.Summary())

	var opts []grpc.ServerOption
	if *certFile != "" {
		creds, err := credentials.NewServerTLSFromFile(*certFile, *keyFile)
		if err != nil {
			log.Fatalf("Failed to load TLS credentials: %v", err)
		}
		opts = append(opts, grpc.Creds(creds))
	}
	grpcServer := grpc.NewServer(opts...)
	srv := fakefa.NewServer(gen)
	flowpb.RegisterFlowStreamServiceServer(grpcServer, srv)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(srv.Stats())
	})
	statsServer := &http.Server{Addr: *statsListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := statsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Stats server error: %v", err)
		}
	}()
	log.Printf("Serving FlowStreamService on %s, stats on %s", *listen, *statsListen)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			grpcServer.Stop()
			_ = statsServer.Close()
			return
		case <-ticker.C:
			st := srv.Stats()
			log.Printf("streams=%d generated=%d sent=%d liveConns=%d", st.StreamsOpen, st.RecordsGenerated, st.RecordsSent, st.LiveConnections)
		}
	}
}
