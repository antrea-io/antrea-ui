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

package plugins

import (
	"bytes"
	"crypto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenPGPKeyPolicy covers openPGPPolicy's key half: which trusted keys load, and that a key
// which loads can actually sign a plugin that verifies. A key refused by the policy must fail at
// load time (startup), not only when every signature it makes is later rejected.
func TestOpenPGPKeyPolicy(t *testing.T) {
	manifest := []byte(`{"name":"pod-counter"}`)

	tests := []struct {
		name   string
		config *packet.Config
		want   bool
	}{
		{name: "EdDSA legacy (Curve25519)", config: &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Curve: packet.Curve25519}, want: true},
		{name: "Ed25519", config: &packet.Config{Algorithm: packet.PubKeyAlgoEd25519}, want: true},
		{name: "Ed448", config: &packet.Config{Algorithm: packet.PubKeyAlgoEd448}, want: true},
		{name: "ECDSA P-256", config: &packet.Config{Algorithm: packet.PubKeyAlgoECDSA, Curve: packet.CurveNistP256}, want: true},
		{name: "ECDSA P-384", config: &packet.Config{Algorithm: packet.PubKeyAlgoECDSA, Curve: packet.CurveNistP384}, want: true},
		{name: "ECDSA P-521", config: &packet.Config{Algorithm: packet.PubKeyAlgoECDSA, Curve: packet.CurveNistP521}, want: true},
		// RSA-3072 is covered by the GnuPG fixtures below, rather than paying for its keygen here.
		{name: "RSA-2048", config: &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048}, want: false},
		{name: "ECDSA Brainpool P-256", config: &packet.Config{Algorithm: packet.PubKeyAlgoECDSA, Curve: packet.CurveBrainpoolP256}, want: false},
		{name: "ECDSA secp256k1", config: &packet.Config{Algorithm: packet.PubKeyAlgoECDSA, Curve: packet.CurveSecP256k1}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			signer := newTestEntity(tc.config, 0)
			verifier, err := NewSignatureVerifier("test", SignatureTypeOpenPGP, writeKeyFile(t, armoredPublicKey(t, signer)))
			if !tc.want {
				assert.ErrorContains(t, err, "can't sign under the plugin signature policy")
				return
			}
			require.NoError(t, err)
			assert.NoError(t, verifier.Verify(manifest, sign(t, signer, manifest)))
		})
	}
}

// TestOpenPGPGnuPGInterop verifies signatures made by GnuPG itself (see testdata/gnupg/README.md),
// the tool hack/sign-plugin.sh and most operators sign with - including the SHA-1 cases go-crypto
// refuses to produce, so they can only be tested against fixtures.
func TestOpenPGPGnuPGInterop(t *testing.T) {
	fixture := func(name string) string { return filepath.Join("testdata", "gnupg", name) }
	manifest, err := os.ReadFile(fixture("manifest.json"))
	require.NoError(t, err)

	verify := func(t *testing.T, key, signature string) error {
		t.Helper()
		verifier, err := NewSignatureVerifier("gnupg", SignatureTypeOpenPGP, fixture(key))
		require.NoError(t, err)
		data, err := os.ReadFile(fixture(signature))
		require.NoError(t, err)
		return verifier.Verify(manifest, data)
	}

	t.Run("default key and signature", func(t *testing.T) {
		assert.NoError(t, verify(t, "eddsa-key.asc", "manifest.json.eddsa.asc"))
	})
	t.Run("RSA-3072", func(t *testing.T) {
		assert.NoError(t, verify(t, "rsa3072-key.asc", "manifest.json.rsa3072.asc"))
	})
	t.Run("SHA-1 signature", func(t *testing.T) {
		assert.ErrorContains(t, verify(t, "rsa3072-key.asc", "manifest.json.rsa3072-sha1.asc"), "insecure message hash algorithm: SHA-1")
	})
	t.Run("SHA-1 self-signature", func(t *testing.T) {
		// Keys made by older GnuPG versions can carry these. go-crypto's own default accepts
		// them; openPGPPolicy doesn't, and says so at load time.
		_, err := NewSignatureVerifier("gnupg", SignatureTypeOpenPGP, fixture("rsa3072-sha1-self-signature-key.asc"))
		assert.ErrorContains(t, err, "can't sign under the plugin signature policy")
	})
	t.Run("signature by a different key", func(t *testing.T) {
		assert.Error(t, verify(t, "eddsa-key.asc", "manifest.json.rsa3072.asc"))
	})
}

// TestOpenPGPSignatureHashPolicy covers openPGPPolicy's hash half on the plugin's own signature -
// the part an attacker able to write a plugin ConfigMap gets to choose - against a key the policy
// accepts, so the hash is the only variable. SHA-1 is covered by TestOpenPGPGnuPGInterop, since
// go-crypto can't produce a SHA-1 signature.
func TestOpenPGPSignatureHashPolicy(t *testing.T) {
	manifest := []byte(`{"name":"pod-counter"}`)
	verifier := trustedOpenPGPKey(t, "test", testSigner())

	tests := []struct {
		hash crypto.Hash
		want bool
	}{
		{hash: crypto.SHA224, want: false},
		{hash: crypto.SHA256, want: true},
		{hash: crypto.SHA384, want: true},
		{hash: crypto.SHA512, want: true},
		{hash: crypto.SHA3_256, want: true},
		{hash: crypto.SHA3_512, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.hash.String(), func(t *testing.T) {
			err := verifier.Verify(manifest, signWithHash(t, testSigner(), manifest, tc.hash))
			if tc.want {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, "insecure message hash algorithm")
			}
		})
	}
}

// signWithHash is sign with the hash forced, built at the packet level: go-crypto's signing API
// picks a hash itself, and silently upgrades a weak one rather than using it.
func signWithHash(t *testing.T, entity *openpgp.Entity, data []byte, hash crypto.Hash) []byte {
	t.Helper()
	sig := &packet.Signature{
		Version:      entity.PrimaryKey.Version,
		SigType:      packet.SigTypeBinary,
		PubKeyAlgo:   entity.PrivateKey.PubKeyAlgo,
		Hash:         hash,
		CreationTime: time.Now(),
		IssuerKeyId:  &entity.PrimaryKey.KeyId,
	}
	h, err := sig.PrepareSign(nil)
	require.NoError(t, err)
	h.Write(data)
	require.NoError(t, sig.Sign(h, entity.PrivateKey, nil))
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.SignatureType, nil)
	require.NoError(t, err)
	require.NoError(t, sig.Serialize(w))
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func writeKeyFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "public-key.asc")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}
