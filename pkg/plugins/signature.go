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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// SignatureTypeOpenPGP is the only signature type supported today (see signature_openpgp.go).
// Adding another is additive: a new constant, a case in NewSignatureVerifier, and a
// SignatureVerifier implementation with its own SignatureFileName, so plugins signed with an
// existing type keep verifying unchanged.
const SignatureTypeOpenPGP = "openpgp"

// sha256HexLen is how many characters a lowercase hex SHA-256 digest has. A manifest's
// bundleSha256, when set, must be exactly this long (see verifyBundleDigest).
const sha256HexLen = sha256.Size * 2

// SignatureVerifier checks a detached signature over a plugin's manifest.json against one trusted
// key source - one entry of plugins.signature.trustedKeys in the backend configuration. A plugin
// loads when its manifest verifies against any one of the configured verifiers (see
// verifyManifestSignature), which is what lets a deployment trust several signers, of the same
// signature type or not, and move signers from one type to another without re-signing every
// plugin at once.
type SignatureVerifier interface {
	// Name identifies the trusted key source in logs, both when a plugin is rejected and when one
	// is loaded (as the key that vouched for it).
	Name() string
	// SignatureFileName is the ConfigMap key, or the file in a plugin directory, holding the
	// detached signature this verifier checks. Each signature type has its own, so a plugin can
	// carry signatures of several types side by side. Verifiers of the same type share it.
	SignatureFileName() string
	// Verify returns an error unless signature is a valid signature, by a key this verifier
	// trusts, over manifest's exact bytes. It must be safe for concurrent use: the ConfigMap and
	// directory watches share the same verifiers and verify from their own goroutines.
	Verify(manifest, signature []byte) error
}

// NewSignatureVerifier loads the trusted key source of the given signature type from path. Called
// once per configured trusted key at startup (see cmd/server/main.go): an unknown type fails the
// process rather than being skipped, so an older backend given a configuration written for a
// newer one never runs trusting fewer keys than its operator intended.
func NewSignatureVerifier(name, signatureType, path string) (SignatureVerifier, error) {
	switch signatureType {
	case SignatureTypeOpenPGP:
		keyring, err := loadOpenPGPKeyRing(path)
		if err != nil {
			return nil, err
		}
		return &openPGPVerifier{name: name, keyring: keyring}, nil
	default:
		return nil, fmt.Errorf("unsupported signature type %q (supported: %q)", signatureType, SignatureTypeOpenPGP)
	}
}

// requireSignature reports whether verifiers enforce signatures - i.e. whether there is at least
// one. A single predicate rather than an inline len() at each decision point (in both
// parsePluginConfigMap and parsePluginArchive), so "what does no verifier mean" cannot come to be
// answered differently in one of them than in the others.
//
// No verifiers means "not enforcing" rather than "enforcing against no key, reject everything".
// Both readings are defensible, and this one is only safe because a configured trusted key can't
// turn into no verifier: cmd/server fails the process at startup on a key file that is missing,
// malformed, holds no public key, or holds a key the algorithm policy refuses. A key file whose
// keys have all expired or been revoked does load (see checkOpenPGPKeyPolicy for why), but that
// still yields a verifier, so enforcement stays on and every plugin is rejected rather than let
// through.
func requireSignature(verifiers []SignatureVerifier) bool {
	return len(verifiers) > 0
}

// signatureReader returns the named signature file from a plugin source - a ConfigMap key or a
// file in a plugin directory. found is false, with a nil error, when the plugin doesn't carry it.
type signatureReader func(fileName string) (signature []byte, found bool, err error)

// verifyManifestSignature checks manifest against verifiers, reading each verifier's signature
// file through read, and returns the name of the first verifier it verifies against. It fails
// unless at least one does. A signature file is read once however many verifiers share it, and a
// verifier whose signature file the plugin doesn't carry is skipped rather than counted as a
// failure: a plugin signed only with one of several configured types is still validly signed.
func verifyManifestSignature(verifiers []SignatureVerifier, manifest []byte, read signatureReader) (string, error) {
	type readResult struct {
		signature []byte
		found     bool
	}
	signatures := make(map[string]readResult)
	var missing []string
	var errs []error
	for _, verifier := range verifiers {
		fileName := verifier.SignatureFileName()
		result, seen := signatures[fileName]
		if !seen {
			signature, found, err := read(fileName)
			if err != nil {
				return "", fmt.Errorf("failed to read %s: %w", fileName, err)
			}
			result = readResult{signature: signature, found: found}
			signatures[fileName] = result
			if !found {
				missing = append(missing, fileName)
			}
		}
		if !result.found {
			continue
		}
		if err := verifier.Verify(manifest, result.signature); err != nil {
			errs = append(errs, fmt.Errorf("invalid %s for trusted key %q: %w", fileName, verifier.Name(), err))
			continue
		}
		return verifier.Name(), nil
	}
	if len(errs) > 0 {
		return "", errors.Join(errs...)
	}
	// Every verifier was skipped, so the plugin carries none of their signature files.
	if len(missing) == 1 {
		return "", fmt.Errorf("missing %s", missing[0])
	}
	return "", fmt.Errorf("missing a signature (expected one of: %s)", strings.Join(missing, ", "))
}

// verifyManifestBundleDigest checks bundle, the plugin's bundle.zip as delivered, against
// manifest's bundleSha256. The digest is required when verifiers enforce signatures (it is what
// extends the signature over manifest.json to the bundle), and verified whenever present
// regardless. Shared by parsePluginConfigMap and parsePluginArchive, so the two sources cannot
// come to disagree on when a digest is required, the same reason requireSignature exists.
func verifyManifestBundleDigest(verifiers []SignatureVerifier, manifest *apisv1.PluginManifest, bundle io.Reader) error {
	if manifest.BundleSha256 == "" {
		if requireSignature(verifiers) {
			return fmt.Errorf("manifest is missing 'bundleSha256', which is required when plugin signature verification is enabled")
		}
		return nil
	}
	return verifyBundleDigest(bundle, manifest.BundleSha256)
}

// verifyBundleDigest streams r through SHA-256 and compares the result to wantHex, the manifest's
// bundleSha256. Streaming, so a large bundle is never held in memory just to be hashed.
//
// wantHex must be exactly 64 hex characters (compared case-insensitively). Anything else - a
// truncated digest, a "sha256:" prefix, a typo - is a rejection rather than a "treat as absent",
// so a malformed digest can't silently downgrade a plugin to unverified.
func verifyBundleDigest(r io.Reader, wantHex string) error {
	if len(wantHex) != sha256HexLen {
		return fmt.Errorf("manifest's 'bundleSha256' must be %d hex characters", sha256HexLen)
	}
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		return fmt.Errorf("manifest's 'bundleSha256' is not valid hex: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("failed to hash %s: %w", bundleFileName, err)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("%s does not match the manifest's 'bundleSha256'", bundleFileName)
	}
	return nil
}
