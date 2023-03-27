// Copyright 2023 Antrea Authors.
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

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/spf13/afero"
)

const defaultPrivateKeySize = 2048

var fs = afero.NewOsFs()

func LoadPrivateKeyFromBytes(pemData []byte) (*rsa.PrivateKey, error) {
	// Use the PEM decoder and parse the private key
	pemBlock, _ := pem.Decode(pemData)
	priv, err := x509.ParsePKCS1PrivateKey(pemBlock.Bytes)

	// Public key can be obtained through priv.PublicKey
	return priv, err
}

func LoadPrivateKeyFromFile(filepath string) (*rsa.PrivateKey, error) {
	// Read the bytes of the PEM file, e.g. id_rsa
	pemData, err := afero.ReadFile(fs, filepath)
	if err != nil {
		return nil, err
	}

	return LoadPrivateKeyFromBytes(pemData)
}

func LoadPrivateKeyOrDie(filepath string) *rsa.PrivateKey {
	p, err := LoadPrivateKeyFromFile(filepath)
	if err != nil {
		panic(fmt.Sprintf("Cannot load PEM data: %v", err))
	}
	return p
}

func GeneratePrivateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, defaultPrivateKeySize)
}
