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

// flowbench deploys antrea-ui, unchanged, next to a fake Flow Aggregator streaming the flows of a
// synthetic cluster, and measures the flow visibility pages under that load. See
// test/benchmark/flowvisibility/README.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"antrea.io/antrea-ui/test/benchmark/flowvisibility/fakefa"
)

const usage = `Usage: flowbench <command> [flags]

Commands:
  deploy     Deploy (or update) the fake Flow Aggregator, the synthetic Namespaces and antrea-ui,
             then print the URL and a token to log in with.
  run        Deploy each requested scale in turn and measure it with a headless browser.
  monitor    Sample the backend and the fake Flow Aggregator only, e.g. while using the UI by hand.
  teardown   Remove everything deploy created.
  presets    Print the scale presets.

Run "flowbench <command> --help" for the flags of a command.
`

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// cluster is how flowbench reaches the cluster: client-go for the objects it manages, and the
// same kubeconfig and context for helm.
type cluster struct {
	kubeconfig string
	context    string
	clientset  kubernetes.Interface
}

func (c *cluster) helmArgs() []string {
	var args []string
	if c.kubeconfig != "" {
		args = append(args, "--kubeconfig", c.kubeconfig)
	}
	if c.context != "" {
		args = append(args, "--kube-context", c.context)
	}
	return args
}

func (c *cluster) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.kubeconfig, "kubeconfig", "", "path to the kubeconfig (defaults to the usual loading rules)")
	fs.StringVar(&c.context, "context", "", "kubeconfig context to use (defaults to the current one)")
}

func (c *cluster) connect() error {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = c.kubeconfig
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: c.context}).ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	c.clientset = cs
	return nil
}

// scaleFlags select a preset and override individual knobs of it.
type scaleFlags struct {
	presets []string
	sets    []string
}

func (s *scaleFlags) addFlags(fs *pflag.FlagSet, multi bool) {
	help := "scale preset (see \"flowbench presets\")"
	if multi {
		help = "comma-separated scale presets, measured one after the other"
	}
	fs.StringSliceVar(&s.presets, "scale", []string{"small"}, help)
	fs.StringArrayVar(&s.sets, "set", nil, "override a knob of the scale, e.g. --set namespaces=50 --set activeTimeout=30s (repeatable)")
}

// resolve returns the scales to deploy, in order, with their names.
func (s *scaleFlags) resolve() ([]string, []fakefa.Scale, error) {
	var names []string
	var scales []fakefa.Scale
	for _, p := range s.presets {
		scale, ok := fakefa.Presets[p]
		if !ok {
			return nil, nil, fmt.Errorf("unknown scale preset %q", p)
		}
		scale, err := applySets(scale, s.sets)
		if err != nil {
			return nil, nil, err
		}
		if err := scale.Validate(); err != nil {
			return nil, nil, err
		}
		name := p
		if len(s.sets) > 0 {
			name += "+" + strings.Join(s.sets, ",")
		}
		names = append(names, name)
		scales = append(scales, scale)
	}
	return names, scales, nil
}

// applySets overrides knobs by their JSON name, so that every knob of fakefa.Scale can be set
// without a flag of its own.
func applySets(scale fakefa.Scale, sets []string) (fakefa.Scale, error) {
	data, _ := json.Marshal(scale)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	for _, set := range sets {
		key, value, ok := strings.Cut(set, "=")
		if !ok {
			return scale, fmt.Errorf("--set %q: expected key=value", set)
		}
		current, ok := m[key]
		if !ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			return scale, fmt.Errorf("--set %q: unknown knob %q, known ones are %s", set, key, strings.Join(keys, ", "))
		}
		switch current.(type) {
		case bool:
			b, err := strconv.ParseBool(value)
			if err != nil {
				return scale, fmt.Errorf("--set %q: %w", set, err)
			}
			m[key] = b
		default:
			// Durations are encoded as nanoseconds; accept them as Go durations too.
			if d, err := time.ParseDuration(value); err == nil {
				m[key] = d.Nanoseconds()
				continue
			}
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return scale, fmt.Errorf("--set %q: %w", set, err)
			}
			m[key] = f
		}
	}
	data, _ = json.Marshal(m)
	var out fakefa.Scale
	if err := json.Unmarshal(data, &out); err != nil {
		return scale, fmt.Errorf("invalid --set: %w", err)
	}
	return out, nil
}

// repoRoot finds the root of the antrea-ui repository, which holds the chart and the browser
// script.
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if ok {
		if root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../../../..")); err == nil {
			if _, err := os.Stat(filepath.Join(root, "build/charts/antrea-ui")); err == nil {
				return root
			}
		}
	}
	wd, _ := os.Getwd()
	return wd
}

func addDeployFlags(fs *pflag.FlagSet, o *deployOptions) {
	fs.StringVar(&o.fakeFAImage, "fakefa-image", "antrea/antrea-ui-fakefa:latest", "image of the fake Flow Aggregator")
	fs.StringVar(&o.uiImageTag, "ui-image-tag", "latest", "tag of the antrea-ui frontend and backend images")
	fs.StringVar(&o.uiNamespace, "ui-namespace", metav1.NamespaceSystem, "Namespace antrea-ui is installed in")
	fs.StringVar(&o.uiRelease, "ui-release", "antrea-ui", "Helm release name of antrea-ui")
	fs.IntVar(&o.nodePort, "node-port", 31234, "NodePort of the antrea-ui Service")
	fs.StringArrayVar(&o.helmSets, "helm-set", nil, "extra --set for the antrea-ui chart (repeatable)")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd := os.Args[1]; cmd {
	case "deploy":
		err = runDeploy(ctx, os.Args[2:])
	case "run":
		err = runRun(ctx, os.Args[2:])
	case "monitor":
		err = runMonitor(ctx, os.Args[2:])
	case "teardown":
		err = runTeardown(ctx, os.Args[2:])
	case "presets":
		printPresets()
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		logf("Error: %v", err)
		os.Exit(1)
	}
}

func printPresets() {
	names := make([]string, 0, len(fakefa.Presets))
	for n := range fakefa.Presets {
		names = append(names, n)
	}
	slices.SortFunc(names, func(a, b string) int {
		sa, sb := fakefa.Presets[a], fakefa.Presets[b]
		return sa.Nodes*sa.PodsPerNode - sb.Nodes*sb.PodsPerNode
	})
	for _, n := range names {
		s := fakefa.Presets[n]
		fmt.Printf("%-7s namespaces=%d nodes=%d podsPerNode=%d podsPerWorkload=%d shortConnectionRate=%g\n",
			n, s.Namespaces, s.Nodes, s.PodsPerNode, s.PodsPerWorkload, s.ShortConnectionRate)
	}
	data, _ := json.MarshalIndent(fakefa.DefaultScale(), "", "  ")
	fmt.Printf("\nAll knobs, with their defaults (durations in ns, or as Go durations with --set):\n%s\n", data)
}

func runDeploy(ctx context.Context, args []string) error {
	fs := pflag.NewFlagSet("deploy", pflag.ExitOnError)
	o := &deployOptions{repoRoot: repoRoot()}
	o.addFlags(fs)
	addDeployFlags(fs, o)
	var sf scaleFlags
	sf.addFlags(fs, false)
	var url string
	fs.StringVar(&url, "url", "", "URL antrea-ui is reached at (defaults to the first Node's address and the NodePort)")
	_ = fs.Parse(args)
	names, scales, err := sf.resolve()
	if err != nil {
		return err
	}
	if len(scales) != 1 {
		return fmt.Errorf("deploy takes a single --scale")
	}
	if err := o.connect(); err != nil {
		return err
	}
	o.scale = scales[0]
	logf("Deploying scale %s", names[0])
	if err := deploy(ctx, o); err != nil {
		return err
	}
	if url == "" {
		if url, err = defaultURL(ctx, o.clientset, o.nodePort); err != nil {
			return err
		}
	}
	token, err := benchToken(ctx, o.clientset)
	if err != nil {
		return err
	}
	fmt.Printf(`
antrea-ui is up at %s, streaming the flows of the synthetic cluster.

Log in with this token (valid 24h, cluster-admin):

%s

Service map: %s/flows/map?namespace=_all
To reach it from another machine: kubectl%s port-forward --address 0.0.0.0 -n %s svc/%s 3000:3000

`, url, token, url, kubectlContextFlag(o.context), o.uiNamespace, o.uiRelease)
	return nil
}

func kubectlContextFlag(ctx string) string {
	if ctx == "" {
		return ""
	}
	return " --context " + ctx
}

func runTeardown(ctx context.Context, args []string) error {
	fs := pflag.NewFlagSet("teardown", pflag.ExitOnError)
	var c cluster
	c.addFlags(fs)
	var uiNamespace, uiRelease string
	fs.StringVar(&uiNamespace, "ui-namespace", metav1.NamespaceSystem, "Namespace antrea-ui is installed in")
	fs.StringVar(&uiRelease, "ui-release", "antrea-ui", "Helm release name of antrea-ui")
	_ = fs.Parse(args)
	if err := c.connect(); err != nil {
		return err
	}
	return teardown(ctx, &c, uiNamespace, uiRelease)
}

// defaultURL is the first Node's InternalIP and the NodePort: reachable from the host for a Kind
// cluster, whose Nodes are containers on a local bridge.
func defaultURL(ctx context.Context, cs kubernetes.Interface, nodePort int) (string, error) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return "", err
	}
	if len(nodes.Items) == 0 {
		return "", fmt.Errorf("the cluster has no Nodes")
	}
	for _, a := range nodes.Items[0].Status.Addresses {
		if a.Type == "InternalIP" {
			return fmt.Sprintf("http://%s:%d", a.Address, nodePort), nil
		}
	}
	return "", fmt.Errorf("the Node %s has no InternalIP; pass --url", nodes.Items[0].Name)
}

type object interface {
	metav1.Object
	k8sruntime.Object
}

// typedClient is the subset of a client-go typed client apply needs.
type typedClient[T object] interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (T, error)
	Create(ctx context.Context, obj T, opts metav1.CreateOptions) (T, error)
	Update(ctx context.Context, obj T, opts metav1.UpdateOptions) (T, error)
}

// apply creates obj, or replaces the existing object of that name with it.
func apply[T object](ctx context.Context, c typedClient[T], obj T) error {
	// Retried on a conflict: a controller may update the object (its status, typically) between
	// the Get and the Update.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := c.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = c.Create(ctx, obj, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		_, err = c.Update(ctx, obj, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to apply %T %s: %w", obj, obj.GetName(), err)
	}
	return nil
}
