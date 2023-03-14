package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/spf13/afero"
)

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
