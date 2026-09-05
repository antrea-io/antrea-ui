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
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// errDestGone marks an extractZip failure that happened after it already removed the previous
// extraction at dest to make way for the new one - as opposed to a failure before that point,
// which leaves dest (and whatever it was already serving) untouched. Callers that keep a
// previous extraction alive across a failed reload (handleUpsert) need to tell the two apart:
// wrapped with errors.Is, not returned as a distinct type, so every other caller can keep
// treating extractZip's error as opaque.
var errDestGone = errors.New("previous extraction removed")

// safeJoin joins root and name the way net/http.Dir does: name is treated as rooted (as if it
// had a leading "/") before being cleaned, so any number of ".." segments collapse instead of
// escaping root - the standard technique for turning a URL path segment (pluginEntry.open, from
// an unauthenticated HTTP request) or an untrusted archive entry name (extractZip, "zip slip")
// into a safe local filesystem path.
func safeJoin(root, name string) string {
	return filepath.Join(root, filepath.FromSlash(cleanEntryName(name)))
}

// cleanEntryName normalizes a bundle-relative name (a zip central-directory entry, or a
// manifest's Entry/Federation.RemoteEntry) the same way safeJoin resolves it when writing
// (extractZip) or reading (pluginEntry.open) that file, so a name carrying a non-clean prefix
// (e.g. "./index.js", "/index.js", "a/./b.js") is keyed and looked up identically everywhere -
// see zipEntryNames and validateManifest.
func cleanEntryName(name string) string {
	return strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(name)), "/")
}

// zipEntryNames returns the set of regular (non-directory) file names in zr - the name-only view
// validateManifest checks a manifest's references against, before either source commits to
// extracting the whole bundle to disk.
func zipEntryNames(zr *zip.Reader) map[string]bool {
	names := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			names[cleanEntryName(f.Name)] = true
		}
	}
	return names
}

// extractZip extracts every regular file entry of zr into a fresh directory, then atomically
// swaps it into place at dest, replacing any previous extraction there. maxBundleBytes bounds
// the combined decompressed size across every entry (zero means unbounded) - a backstop against
// a "zip bomb", checked while extracting rather than after the fact, so a small,
// maliciously-high-ratio entry can't balloon into an unbounded write before this ever gets a
// chance to reject it.
//
// Extracting to a temporary directory first, rather than clearing dest and writing the new
// bundle directly into it, matters because dest is also what's being served live
// (pluginEntry.diskRoot): a reader that already resolved diskRoot == dest (mid-request, via
// pluginEntry.open) keeps reading whatever's physically there until this function's rename
// below completes, so it never observes a half-extracted dest. os.Rename can't atomically
// replace a non-empty directory (a POSIX limitation, not something fixable here), so dest is
// still removed before the rename - but that's now a two-syscall gap where dest doesn't exist at
// all (a racing request 404s rather than seeing either version), rather than however long it
// takes to overwrite every file in the bundle one at a time. If the RemoveAll or the Rename
// itself then fails, dest is left gone (or partly gone) rather than restored to the previous
// extraction - see errDestGone.
func extractZip(zr *zip.Reader, dest string, maxBundleBytes int64) (string, error) {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("failed to create %q: %w", parent, err)
	}
	tmpDest, err := os.MkdirTemp(parent, filepath.Base(dest)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("failed to create extraction directory: %w", err)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			os.RemoveAll(tmpDest)
		}
	}()

	// A non-positive maxBundleBytes means unbounded; treat it as an effectively-infinite budget
	// rather than special-casing "no limit" separately below - no real bundle will ever reach
	// math.MaxInt64 decompressed bytes.
	budgetTotal := maxBundleBytes
	if budgetTotal <= 0 {
		budgetTotal = math.MaxInt64
	}
	var written int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// safeJoin neutralizes a malicious entry name (e.g. "../../etc/cron.d/evil") the same
		// way it neutralizes a malicious URL filename in pluginEntry.open - this is exactly the
		// "zip slip" vulnerability class, on the write side instead of the read side.
		target := safeJoin(tmpDest, f.Name)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", fmt.Errorf("failed to create directory for %q: %w", f.Name, err)
		}
		n, err := extractZipFile(f, target, budgetTotal-written)
		if err != nil {
			return "", err
		}
		written += n
	}

	if err := os.RemoveAll(dest); err != nil {
		// RemoveAll doesn't stop at the first failing entry - it keeps deleting siblings and
		// returns the first error - so dest can come out of this partly deleted rather than
		// fully removed or fully intact. Either way it no longer reliably holds the previous
		// extraction, so this counts as errDestGone too.
		return "", fmt.Errorf("failed to remove previous extraction: %w: %w", errDestGone, err)
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		return "", fmt.Errorf("failed to finalize extraction: %w: %w", errDestGone, err)
	}
	succeeded = true
	return dest, nil
}

// extractZipFile writes f's decompressed content to target, stopping and returning an error if
// it would write more than budget bytes - budget is whatever's left of the caller's total
// maxBundleBytes after every earlier entry in the same bundle (see extractZip), so the limit
// applies to the bundle as a whole rather than resetting per file. Returns the number of bytes
// written.
func extractZipFile(f *zip.File, target string, budget int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("failed to open %q in %s: %w", f.Name, bundleFileName, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("failed to create %q: %w", target, err)
	}
	// budget can be math.MaxInt64 (unbounded case) - budget+1 would overflow, so instead of
	// io.CopyN(out, rc, budget+1) copy exactly budget bytes, then peek one more to tell "exactly
	// budget bytes" apart from "more remained".
	written, err := io.Copy(out, io.LimitReader(rc, budget))
	if err != nil {
		out.Close()
		return 0, fmt.Errorf("failed to write %q: %w", target, err)
	}
	if written == budget {
		// io.Reader permits a (0, nil) return that callers must not treat as EOF, so loop until
		// we get either a byte (more data remained past the budget) or a real error/io.EOF.
		var extra [1]byte
		for {
			n, err := rc.Read(extra[:])
			if n > 0 {
				out.Close()
				return 0, fmt.Errorf("%q decompresses past this plugin's bundle size budget, refusing to extract further (possible zip bomb)", f.Name)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				out.Close()
				return 0, fmt.Errorf("failed to read %q past its size budget: %w", f.Name, err)
			}
		}
	}
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("failed to write %q: %w", target, err)
	}
	return written, nil
}
