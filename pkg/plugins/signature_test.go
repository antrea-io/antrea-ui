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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginSource installs one plugin - manifestJSON, bundleZip, and one signature file per entry of
// signatures, holding exactly those bytes - into a registry configured with trustedKeys, and
// reports whether it ended up in Index(), and if so which trusted key it was verified by. The two
// sources are driven through the same shape so the tables below can run every case against each:
// the ConfigMap source through handleUpsert, the directory source through loadDiskPlugin, which
// is as far down as either goes before parsePluginConfigMap / parsePluginArchive. Neither path
// involves a watch loop, so every case is a plain synchronous assertion.
//
// Signatures are passed in already built, rather than a signer to build them from, because
// several cases are precisely about the signature not covering the manifest as delivered.
type pluginSource struct {
	name string
	load func(t *testing.T, trustedKeys []SignatureVerifier, manifestJSON string, signatures map[string][]byte, bundleZip []byte) (verifiedBy string, loaded bool)
}

func pluginSources() []pluginSource {
	return []pluginSource{
		{
			name: "configMap",
			load: func(t *testing.T, trustedKeys []SignatureVerifier, manifestJSON string, signatures map[string][]byte, bundleZip []byte) (string, bool) {
				t.Helper()
				r := newSignatureTestRegistry(t, trustedKeys)
				r.handleUpsert(signedConfigMap(t, "pod-counter-plugin", manifestJSON, signatures, bundleZip))
				if len(r.Index()) != 1 {
					return "", false
				}
				entry, _ := r.getConfigMapPlugin("pod-counter-plugin")
				return entry.verifiedBy, true
			},
		},
		{
			name: "directory",
			load: func(t *testing.T, trustedKeys []SignatureVerifier, manifestJSON string, signatures map[string][]byte, bundleZip []byte) (string, bool) {
				t.Helper()
				r := newSignatureTestRegistry(t, trustedKeys)
				root := t.TempDir()
				signedPluginDir(t, root, "pod-counter", manifestJSON, signatures, bundleZip)
				r.loadDiskPlugin(root, "pod-counter", newTestWatcher(t))
				if len(r.Index()) != 1 {
					return "", false
				}
				r.mu.RLock()
				defer r.mu.RUnlock()
				return r.diskPlugins["pod-counter"].verifiedBy, true
			},
		},
	}
}

func newSignatureTestRegistry(t *testing.T, trustedKeys []SignatureVerifier) *Registry {
	t.Helper()
	r := NewRegistry(Options{
		Logger:             testr.New(t),
		Namespace:          "antrea-ui",
		LabelSelector:      "ui.antrea.io/plugin=true",
		SignatureVerifiers: trustedKeys,
	})
	t.Cleanup(r.Close)
	return r
}

// asc is the signatures argument for a plugin carrying only an OpenPGP signature.
func asc(signature []byte) map[string][]byte {
	return map[string][]byte{openPGPSignatureFileName: signature}
}

// TestPluginSignatureVerification is the decision matrix from docs/plugins.md, one case per row,
// run against both plugin sources.
func TestPluginSignatureVerification(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	otherBundle := buildZip(t, map[string]string{"index.js": "console.log('something else')"})

	// The three "bundleSha256" columns of the matrix: absent, pinning this bundle, and pinning
	// some other bundle.
	noDigest := podCounterManifest("pod-counter", "0.1.0")
	goodDigest := signedManifest("pod-counter", "0.1.0", "index.js", bundle)
	badDigest := signedManifest("pod-counter", "0.1.0", "index.js", otherBundle)
	malformedDigest := manifestWithDigest("pod-counter", "0.1.0", "index.js", "deadbeef")
	prefixedDigest := manifestWithDigest("pod-counter", "0.1.0", "index.js", "sha256:"+bundleDigest(bundle))

	trusted := []SignatureVerifier{trustedOpenPGPKey(t, "trusted", testSigner())}

	tests := []struct {
		name string
		// trustedKeys nil means no trusted key is configured, i.e. verification is disabled.
		trustedKeys []SignatureVerifier
		// manifest is what is delivered; signOver, when non-nil, is what the shipped
		// manifest.json.asc is actually made over. Equal for every case but the tampering one.
		manifest string
		signOver *string
		signer   *openpgp.Entity
		want     bool
	}{
		// Rows 1-3: no key configured. A manifest.json.asc, present or not, is ignored - there
		// is nothing to verify it against - but a bundleSha256 is still honored when present.
		{name: "no key, no digest", manifest: noDigest, want: true},
		{name: "no key, matching digest", manifest: goodDigest, want: true},
		{name: "no key, mismatched digest", manifest: badDigest, want: false},
		// Rows 4-9: key configured.
		{name: "key, no signature", trustedKeys: trusted, manifest: goodDigest, want: false},
		{name: "key, signature from a different key", trustedKeys: trusted, signer: testOtherSigner(), manifest: goodDigest, want: false},
		// The key is trusted, it just expired before now - and the signature is dated within
		// its lifetime, which go-crypto on its own accepts (see openPGPVerifier.Verify).
		{name: "key, signature from an expired key", trustedKeys: []SignatureVerifier{trustedOpenPGPKey(t, "trusted", testExpiredSigner())}, signer: testExpiredSigner(), manifest: goodDigest, want: false},
		// Distinct from "different key" above in what it proves: the signature is from the
		// trusted key and does verify - over bytes that are not the manifest as delivered.
		{name: "key, valid signature over different manifest bytes", trustedKeys: trusted, signer: testSigner(), manifest: goodDigest, signOver: &badDigest, want: false},
		{name: "key, valid signature, no digest", trustedKeys: trusted, signer: testSigner(), manifest: noDigest, want: false},
		{name: "key, valid signature, mismatched digest", trustedKeys: trusted, signer: testSigner(), manifest: badDigest, want: false},
		{name: "key, valid signature, matching digest", trustedKeys: trusted, signer: testSigner(), manifest: goodDigest, want: true},
		// Outside the matrix: a bundleSha256 that isn't 64 hex characters must reject rather
		// than be treated as absent, so a typo can't silently downgrade a plugin to unverified.
		{name: "malformed digest, no key", manifest: malformedDigest, want: false},
		{name: "sha256:-prefixed digest, no key", manifest: prefixedDigest, want: false},
		{name: "malformed digest, valid signature", trustedKeys: trusted, signer: testSigner(), manifest: malformedDigest, want: false},
	}

	for _, source := range pluginSources() {
		for _, tc := range tests {
			t.Run(source.name+"/"+tc.name, func(t *testing.T) {
				var signatures map[string][]byte
				if tc.signer != nil {
					signed := tc.manifest
					if tc.signOver != nil {
						signed = *tc.signOver
					}
					signatures = asc(sign(t, tc.signer, []byte(signed)))
				}
				verifiedBy, loaded := source.load(t, tc.trustedKeys, tc.manifest, signatures, bundle)
				assert.Equal(t, tc.want, loaded)
				if loaded && tc.trustedKeys != nil {
					assert.Equal(t, "trusted", verifiedBy)
				}
			})
		}
	}
}

// fakeSignatureFileName is the signature file of fakeVerifier, a stand-in for a second signature
// type: nothing in the registry may assume that every trusted key reads manifest.json.asc.
const fakeSignatureFileName = manifestFileName + ".fake"

// fakeVerifier accepts exactly fakeSignature(manifest), under its own signature file name.
type fakeVerifier struct {
	name string
}

func (v fakeVerifier) Name() string              { return v.name }
func (v fakeVerifier) SignatureFileName() string { return fakeSignatureFileName }
func (v fakeVerifier) Verify(manifest, signature []byte) error {
	if !bytes.Equal(signature, fakeSignature(manifest)) {
		return errors.New("not a fake signature over this manifest")
	}
	return nil
}

func fakeSignature(manifest []byte) []byte {
	return []byte("fake:" + bundleDigest(manifest))
}

// TestPluginSignatureVerificationWithSeveralTrustedKeys covers plugins.signature.trustedKeys
// holding more than one entry, of the same type and of different types: a plugin loads when its
// signature verifies against any one of them, and records which one.
func TestPluginSignatureVerificationWithSeveralTrustedKeys(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	manifest := signedManifest("pod-counter", "0.1.0", "index.js", bundle)
	otherManifest := signedManifest("pod-counter", "0.2.0", "index.js", bundle)

	twoOpenPGPKeys := []SignatureVerifier{
		trustedOpenPGPKey(t, "antrea", testSigner()),
		trustedOpenPGPKey(t, "vendor", testOtherSigner()),
	}
	twoTypes := []SignatureVerifier{
		trustedOpenPGPKey(t, "antrea", testSigner()),
		fakeVerifier{name: "fake"},
	}

	tests := []struct {
		name        string
		trustedKeys []SignatureVerifier
		signatures  map[string][]byte
		// wantVerifiedBy empty means the plugin must be rejected.
		wantVerifiedBy string
	}{
		{name: "same type, signed by the first key", trustedKeys: twoOpenPGPKeys, signatures: asc(sign(t, testSigner(), []byte(manifest))), wantVerifiedBy: "antrea"},
		{name: "same type, signed by the second key", trustedKeys: twoOpenPGPKeys, signatures: asc(sign(t, testOtherSigner(), []byte(manifest))), wantVerifiedBy: "vendor"},
		{name: "same type, signed by neither", trustedKeys: twoOpenPGPKeys, signatures: asc(sign(t, testExpiredSigner(), []byte(manifest)))},
		{name: "two types, only an OpenPGP signature", trustedKeys: twoTypes, signatures: asc(sign(t, testSigner(), []byte(manifest))), wantVerifiedBy: "antrea"},
		{name: "two types, only the other type's signature", trustedKeys: twoTypes, signatures: map[string][]byte{fakeSignatureFileName: fakeSignature([]byte(manifest))}, wantVerifiedBy: "fake"},
		// Any one valid signature is enough, even alongside an invalid one of another type.
		{
			name:        "two types, invalid OpenPGP signature, valid other signature",
			trustedKeys: twoTypes,
			signatures: map[string][]byte{
				openPGPSignatureFileName: sign(t, testSigner(), []byte(otherManifest)),
				fakeSignatureFileName:    fakeSignature([]byte(manifest)),
			},
			wantVerifiedBy: "fake",
		},
		{name: "two types, no signature at all", trustedKeys: twoTypes},
		{name: "only a signature of a type no trusted key has", trustedKeys: twoOpenPGPKeys, signatures: map[string][]byte{fakeSignatureFileName: fakeSignature([]byte(manifest))}},
	}

	for _, source := range pluginSources() {
		for _, tc := range tests {
			t.Run(source.name+"/"+tc.name, func(t *testing.T) {
				verifiedBy, loaded := source.load(t, tc.trustedKeys, manifest, tc.signatures, bundle)
				assert.Equal(t, tc.wantVerifiedBy != "", loaded)
				assert.Equal(t, tc.wantVerifiedBy, verifiedBy)
			})
		}
	}
}

// TestVerifyManifestSignature covers what verifyManifestSignature reports when no trusted key
// accepts a manifest, and that a signature file shared by several trusted keys is read once.
func TestVerifyManifestSignature(t *testing.T) {
	manifest := []byte(`{"name":"pod-counter"}`)

	readerFor := func(files map[string][]byte, reads map[string]int) signatureReader {
		return func(fileName string) ([]byte, bool, error) {
			reads[fileName]++
			signature, ok := files[fileName]
			return signature, ok, nil
		}
	}

	t.Run("one signature type, missing", func(t *testing.T) {
		_, err := verifyManifestSignature([]SignatureVerifier{trustedOpenPGPKey(t, "antrea", testSigner())}, manifest, readerFor(nil, map[string]int{}))
		assert.EqualError(t, err, "missing manifest.json.asc")
	})

	t.Run("two signature types, both missing", func(t *testing.T) {
		verifiers := []SignatureVerifier{trustedOpenPGPKey(t, "antrea", testSigner()), fakeVerifier{name: "fake"}}
		_, err := verifyManifestSignature(verifiers, manifest, readerFor(nil, map[string]int{}))
		assert.EqualError(t, err, "missing a signature (expected one of: manifest.json.asc, manifest.json.fake)")
	})

	t.Run("shared signature file, rejected by every key", func(t *testing.T) {
		verifiers := []SignatureVerifier{
			trustedOpenPGPKey(t, "antrea", testSigner()),
			trustedOpenPGPKey(t, "vendor", testOtherSigner()),
		}
		reads := map[string]int{}
		files := map[string][]byte{openPGPSignatureFileName: sign(t, testExpiredSigner(), manifest)}
		_, err := verifyManifestSignature(verifiers, manifest, readerFor(files, reads))
		require.Error(t, err)
		// Every trusted key's reason is reported, not just the last one's.
		assert.ErrorContains(t, err, `trusted key "antrea"`)
		assert.ErrorContains(t, err, `trusted key "vendor"`)
		assert.Equal(t, map[string]int{openPGPSignatureFileName: 1}, reads)
	})

	t.Run("read error", func(t *testing.T) {
		readErr := errors.New("permission denied")
		read := func(string) ([]byte, bool, error) { return nil, false, readErr }
		_, err := verifyManifestSignature([]SignatureVerifier{fakeVerifier{name: "fake"}}, manifest, read)
		assert.ErrorIs(t, err, readErr)
	})
}

func TestNewSignatureVerifierRejectsUnknownType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "public-key.asc")
	require.NoError(t, os.WriteFile(path, armoredPublicKey(t, testSigner()), 0o600))
	_, err := NewSignatureVerifier("antrea", "minisign", path)
	assert.ErrorContains(t, err, `unsupported signature type "minisign"`)
}

// TestLoadOpenPGPKeyRing covers the key file formats an operator may supply, and the ways a bad
// one must fail rather than silently produce an empty ring, which would leave a configured
// trusted key that trusts nothing.
func TestLoadOpenPGPKeyRing(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		return path
	}
	verify := func(t *testing.T, ring openpgp.EntityList, signer *openpgp.Entity) {
		t.Helper()
		manifest := []byte(`{"name":"pod-counter"}`)
		verifier := &openPGPVerifier{name: "test", keyring: ring}
		require.NoError(t, verifier.Verify(manifest, sign(t, signer, manifest)))
	}

	t.Run("armored", func(t *testing.T) {
		ring, err := loadOpenPGPKeyRing(write("armored.asc", armoredPublicKey(t, testSigner())))
		require.NoError(t, err)
		assert.Len(t, ring, 1)
	})

	t.Run("binary", func(t *testing.T) {
		ring, err := loadOpenPGPKeyRing(write("binary.gpg", serializePublicKey(t, testSigner())))
		require.NoError(t, err)
		assert.Len(t, ring, 1)
	})

	t.Run("two keys, either may sign", func(t *testing.T) {
		// The rotation case: the new key is added to the file before the old one is retired, so
		// a manifest signed by either must verify.
		ring, err := loadOpenPGPKeyRing(write("rotation.asc", armoredPublicKey(t, testSigner(), testOtherSigner())))
		require.NoError(t, err)
		require.Len(t, ring, 2)
		verify(t, ring, testSigner())
		verify(t, ring, testOtherSigner())
	})

	t.Run("two keys in separate armored blocks, either may sign", func(t *testing.T) {
		// The other rotation shape: two separate exports concatenated into one file
		// (`cat new.asc >> public-key.asc`). openpgp.ReadArmoredKeyRing alone reads only the
		// first block, which would silently drop the new key. armoredPublicKey's output has no
		// trailing newline, so this also covers the second header following the first block's
		// END line on the same line.
		data := append(armoredPublicKey(t, testSigner()), armoredPublicKey(t, testOtherSigner())...)
		ring, err := loadOpenPGPKeyRing(write("concatenated.asc", data))
		require.NoError(t, err)
		require.Len(t, ring, 2)
		verify(t, ring, testSigner())
		verify(t, ring, testOtherSigner())
	})

	t.Run("expired key", func(t *testing.T) {
		// Accepted at load time: one left in the file after a rotation is not worth refusing to
		// start over, and its signatures are rejected at verification time anyway.
		_, err := loadOpenPGPKeyRing(write("expired.asc", armoredPublicKey(t, testExpiredSigner())))
		require.NoError(t, err)
	})

	t.Run("revoked key", func(t *testing.T) {
		// Accepted at load time too, since carrying a key's revocation is a reason for it to be
		// in the file, but nothing it signed verifies any more - including a signature made
		// before the revocation, and under the "superseded" reason rather than a compromise.
		signer := newTestEntity(ed25519Config(), 0)
		manifest := []byte(`{"name":"pod-counter"}`)
		signature := sign(t, signer, manifest)
		require.NoError(t, signer.Revoke(packet.KeySuperseded, "rotated", nil))
		ring, err := loadOpenPGPKeyRing(write("revoked.asc", armoredPublicKey(t, signer)))
		require.NoError(t, err)
		verifier := &openPGPVerifier{name: "test", keyring: ring}
		assert.ErrorContains(t, verifier.Verify(manifest, signature), "revoked")
	})

	t.Run("key appended again with its revocation", func(t *testing.T) {
		// go-crypto would use only the first, unrevoked copy, so loading this file would keep a
		// revoked key trusted. It must fail instead.
		signer := newTestEntity(ed25519Config(), 0)
		data := armoredPublicKey(t, signer)
		require.NoError(t, signer.Revoke(packet.KeySuperseded, "rotated", nil))
		data = append(data, armoredPublicKey(t, signer)...)
		_, err := loadOpenPGPKeyRing(write("appended-revocation.asc", data))
		assert.ErrorContains(t, err, "appears in more than one key in the file")
	})

	t.Run("same key twice in one armored block", func(t *testing.T) {
		_, err := loadOpenPGPKeyRing(write("duplicate.asc", armoredPublicKey(t, testSigner(), testSigner())))
		assert.ErrorContains(t, err, "appears in more than one key in the file")
	})

	t.Run("valid armored block followed by one with no public key", func(t *testing.T) {
		// A later block that fails to parse fails the whole file rather than being skipped: the
		// operator added it for a reason, and running without it is a misconfiguration.
		data := append(armoredPublicKey(t, testSigner()), emptyArmoredBlock(t)...)
		_, err := loadOpenPGPKeyRing(write("second-empty.asc", data))
		require.Error(t, err)
	})

	t.Run("nonexistent path", func(t *testing.T) {
		_, err := loadOpenPGPKeyRing(filepath.Join(dir, "does-not-exist.asc"))
		require.Error(t, err)
	})

	t.Run("garbage", func(t *testing.T) {
		_, err := loadOpenPGPKeyRing(write("garbage.asc", []byte("not a key at all")))
		require.Error(t, err)
	})

	t.Run("armored block with no public key", func(t *testing.T) {
		// A well-formed armored block that yields no usable key must error, not produce an empty
		// ring: a trusted key that trusts nothing would reject every plugin with no startup
		// error pointing at the key file.
		_, err := loadOpenPGPKeyRing(write("empty.asc", emptyArmoredBlock(t)))
		require.Error(t, err)
	})
}
