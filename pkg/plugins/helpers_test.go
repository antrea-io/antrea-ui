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
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTestRegistry(t *testing.T) *Registry {
	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
	return r
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(data)
}

// configMap builds a plugin ConfigMap in the current data["manifest.json"] +
// binaryData["bundle.zip"] shape: manifest.json separate (small, human-readable), everything
// else (bundleFiles) zipped into one binaryData key - see registry.go's package doc for why. A
// nil bundleFiles omits binaryData entirely (for cases exercising a missing bundle.zip). Reuses
// version as the ConfigMap's ResourceVersion, standing in for the apiserver bumping it on every
// real write - every test upserting the same name with a new version this way still exercises
// handleUpsert's ResourceVersion-unchanged skip correctly.
func configMap(t *testing.T, name, pluginName, version, entry string, bundleFiles map[string]string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "antrea-ui", ResourceVersion: version},
		Data: map[string]string{
			"manifest.json": fmt.Sprintf(`{"name":%q,"version":%q,"entry":%q}`, pluginName, version, entry),
		},
	}
	if bundleFiles != nil {
		cm.BinaryData = map[string][]byte{"bundle.zip": buildZip(t, bundleFiles)}
	}
	return cm
}

// buildZip builds an in-memory bundle.zip from files (path -> content), for any test that needs
// one - both sources' fixtures are built on top of it, as is extractZip's own suite.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// writePluginDir writes a plugin's on-disk bundle in the current manifest.json + bundle.zip
// shape (see registry.go's package doc): manifestJSON goes to disk as-is, bundleFiles get zipped
// into bundle.zip.
func writePluginDir(t *testing.T, root, name, manifestJSON string, bundleFiles map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFileName), []byte(manifestJSON), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundleFileName), buildZip(t, bundleFiles), 0o600))
}

func podCounterManifest(pluginName, version string) string {
	return fmt.Sprintf(`{"name":%q,"version":%q,"entry":"index.js"}`, pluginName, version)
}

func podCounterBundle() map[string]string {
	return map[string]string{"index.js": "console.log('hi')"}
}

// waitFor polls cond until it returns true or the timeout elapses, failing the test otherwise -
// needed because RunDirectoryWatch's fsnotify-driven updates happen asynchronously.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, cond(), "condition not met within %s", timeout)
}

// startDirectoryWatch runs r.RunDirectoryWatch(dir, ...) in a goroutine and registers a
// t.Cleanup that stops it and waits for the goroutine to actually exit before the test returns -
// r.RunDirectoryWatch logs through r.logger (testr, which writes via t.Log), so a goroutine still
// running after the test function itself returns would otherwise race with the test harness's
// own teardown of t.
func startDirectoryWatch(t *testing.T, r *Registry, dir string) {
	t.Helper()
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.RunDirectoryWatch(dir, stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		<-done
	})
}

// Test OpenPGP entities, generated once per package rather than per case: three are needed
// across the signature suite (the trusted signer, a different signer, and an expired key), and
// regenerating any of them per table row would show up in the package's runtime for no added
// coverage.
//
// Ed25519 rather than go-crypto's RSA default: keygen is effectively instant, where an RSA key
// large enough for openPGPPolicy takes a noticeable fraction of a second. The algorithm policy
// itself has its own tests (see signature_openpgp_test.go).
var (
	testSigner        = sync.OnceValue(func() *openpgp.Entity { return newTestEntity(ed25519Config(), 0) })
	testOtherSigner   = sync.OnceValue(func() *openpgp.Entity { return newTestEntity(ed25519Config(), 0) })
	testExpiredSigner = sync.OnceValue(func() *openpgp.Entity {
		// Created two hours ago with a one-hour lifetime, so it is already expired by the time
		// any test uses it - and so is every signature it makes.
		return newTestEntity(ed25519Config(), time.Hour)
	})
)

func ed25519Config() *packet.Config {
	return &packet.Config{Algorithm: packet.PubKeyAlgoEd25519}
}

// newTestEntity generates a throwaway OpenPGP entity with the key type config selects. A non-zero
// lifetime backdates the key by twice that duration, producing one that is already expired.
func newTestEntity(config *packet.Config, lifetime time.Duration) *openpgp.Entity {
	if lifetime > 0 {
		created := time.Now().Add(-2 * lifetime)
		config.Time = func() time.Time { return created }
		config.KeyLifetimeSecs = uint32(lifetime.Seconds())
	}
	entity, err := openpgp.NewEntity("antrea-ui plugin test", "", "", config)
	if err != nil {
		panic(err)
	}
	return entity
}

// sign returns an armored detached OpenPGP signature over data, as manifest.json.asc holds.
func sign(t *testing.T, entity *openpgp.Entity, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	// The entity's own creation time, so a backdated (expired) key produces a signature that was
	// valid when made and has since expired, rather than one dated before its own key existed.
	config := &packet.Config{Time: func() time.Time { return entity.PrimaryKey.CreationTime }}
	require.NoError(t, openpgp.ArmoredDetachSign(&buf, []*openpgp.Entity{entity}, bytes.NewReader(data), &openpgp.SignParams{Config: config}))
	return buf.Bytes()
}

// publicKeySerializer is what armoredPublicKey needs from an entity: satisfied by both the v2
// openpgp.Entity the tests normally use and the v1 one signature_openpgp_test.go builds the
// fixtures v2 refuses to produce with.
type publicKeySerializer interface {
	Serialize(w io.Writer) error
}

// armoredPublicKey serializes entities' public halves the way `gpg --armor --export` does, for
// loadOpenPGPKeyRing's tests and for building a trusted key to verify against.
func armoredPublicKey(t *testing.T, entities ...publicKeySerializer) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	require.NoError(t, err)
	for _, entity := range entities {
		require.NoError(t, entity.Serialize(w))
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// trustedOpenPGPKey builds the SignatureVerifier a Registry is configured with for an OpenPGP
// trusted key named name, holding only entities' public halves - going through the same key file
// NewSignatureVerifier loads at startup, so openPGPPolicy's load-time check applies too.
func trustedOpenPGPKey(t *testing.T, name string, entities ...publicKeySerializer) SignatureVerifier {
	t.Helper()
	path := filepath.Join(t.TempDir(), "public-key.asc")
	require.NoError(t, os.WriteFile(path, armoredPublicKey(t, entities...), 0o600))
	verifier, err := NewSignatureVerifier(name, SignatureTypeOpenPGP, path)
	require.NoError(t, err)
	return verifier
}

// bundleDigest is bundleZip's lowercase hex SHA-256 - what a manifest's bundleSha256 must hold.
func bundleDigest(bundleZip []byte) string {
	return hex.EncodeToString(sha256Sum(bundleZip))
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// manifestWithDigest builds a manifest JSON with an arbitrary bundleSha256 string, including the
// malformed ones the signature suite needs.
func manifestWithDigest(pluginName, version, entry, digest string) string {
	return fmt.Sprintf(`{"name":%q,"version":%q,"entry":%q,"bundleSha256":%q}`, pluginName, version, entry, digest)
}

// signedManifest returns a manifest JSON pinning bundleZip's real SHA-256 in bundleSha256.
func signedManifest(pluginName, version, entry string, bundleZip []byte) string {
	return manifestWithDigest(pluginName, version, entry, bundleDigest(bundleZip))
}

// signedConfigMap builds a plugin ConfigMap carrying manifestJSON verbatim plus one key per entry
// of signatures (signature file name to its bytes). Separate from configMap above rather than an
// option on it: this one takes the manifest as a string, because several signature cases are
// precisely about the manifest not being what the signature covers.
func signedConfigMap(t *testing.T, name, manifestJSON string, signatures map[string][]byte, bundleZip []byte) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "antrea-ui", ResourceVersion: "1"},
		Data:       map[string]string{manifestFileName: manifestJSON},
		BinaryData: map[string][]byte{bundleFileName: bundleZip},
	}
	for fileName, signature := range signatures {
		cm.Data[fileName] = string(signature)
	}
	return cm
}

// signedPluginDir is signedConfigMap's directory-source counterpart: manifest.json, bundle.zip
// and one file per entry of signatures written into root/name/.
func signedPluginDir(t *testing.T, root, name, manifestJSON string, signatures map[string][]byte, bundleZip []byte) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFileName), []byte(manifestJSON), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundleFileName), bundleZip, 0o600))
	for fileName, signature := range signatures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fileName), signature, 0o600))
	}
	return dir
}

// serializePublicKey is armoredPublicKey's binary (non-armored) counterpart, for the binary key
// file format loadOpenPGPKeyRing also accepts.
func serializePublicKey(t *testing.T, entity *openpgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, entity.Serialize(&buf))
	return buf.Bytes()
}

// emptyArmoredBlock is a well-formed PGP PUBLIC KEY BLOCK holding no key at all - the case
// loadOpenPGPKeyRing must reject rather than turn into an empty ring, which would leave a
// configured trusted key that trusts nothing.
func emptyArmoredBlock(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// newTestWatcher returns an fsnotify watcher for a test driving loadDiskPlugin directly (which
// registers a per-plugin watch before loading), closed when the test finishes.
func newTestWatcher(t *testing.T) *fsnotify.Watcher {
	t.Helper()
	watcher, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	t.Cleanup(func() { watcher.Close() })
	return watcher
}
