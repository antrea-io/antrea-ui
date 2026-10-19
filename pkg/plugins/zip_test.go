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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openZip builds a bundle.zip from files and returns a reader over it, the shape extractZip
// consumes. Both sources reach extractZip with a *zip.Reader already in hand - one over a
// ConfigMap's binaryData, one over a bundle.zip on disk - so the tests below skip either source
// and exercise extractZip on its own.
func openZip(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()
	data := buildZip(t, files)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	return zr
}

// buildTraversalZip writes a zip with a traversal-shaped entry name directly, rather than through
// buildZip: zip.Writer.Create stores whatever name it is given, which is the point here, and
// going through buildZip's map argument would not make that any clearer.
func buildTraversalZip(t *testing.T) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"index.js":          "console.log('hi')",
		"../../../evil.txt": "should not escape",
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return zr
}

func TestExtractZipNeutralizesPathTraversal(t *testing.T) {
	cacheParent := t.TempDir()
	dest := filepath.Join(cacheParent, "cache", "plugin")

	root, err := extractZip(buildTraversalZip(t), dest, 0)
	require.NoError(t, err)

	// The malicious entry lands safely inside the plugin's own extraction directory...
	content, err := os.ReadFile(filepath.Join(root, "evil.txt"))
	require.NoError(t, err)
	assert.Equal(t, "should not escape", string(content))

	// ...and does not actually escape onto the filesystem outside it ("zip slip").
	_, err = os.Stat(filepath.Join(cacheParent, "evil.txt"))
	assert.True(t, os.IsNotExist(err), "path traversal entry must not escape the cache root")
	_, err = os.Stat(filepath.Join(cacheParent, "cache", "evil.txt"))
	assert.True(t, os.IsNotExist(err), "path traversal entry must not escape the plugin directory")
}

func TestExtractZipRejectsSingleEntryPastTheDecompressedSizeLimit(t *testing.T) {
	zr := openZip(t, map[string]string{"index.js": strings.Repeat("x", 200)})

	_, err := extractZip(zr, filepath.Join(t.TempDir(), "plugin"), 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zip bomb")
}

// TestExtractZipRejectsCombinedEntriesPastTheDecompressedSizeLimit exercises the case a single
// oversized entry can't: maxBundleBytes bounds the bundle's total decompressed size, not any one
// entry's, so several individually-small entries that together exceed it must also be rejected.
func TestExtractZipRejectsCombinedEntriesPastTheDecompressedSizeLimit(t *testing.T) {
	zr := openZip(t, map[string]string{
		"index.js": strings.Repeat("x", 60),
		"other.js": strings.Repeat("y", 60),
	})

	_, err := extractZip(zr, filepath.Join(t.TempDir(), "plugin"), 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zip bomb")
}

// TestExtractZipErrorsTellAnInvalidArchiveFromAFailingFilesystem checks the distinction a
// downloaded bundle's retry policy rests on: an archive that cannot be extracted however many times
// it is downloaded is errInvalidArchive, a failure of the local disk is not.
func TestExtractZipErrorsTellAnInvalidArchiveFromAFailingFilesystem(t *testing.T) {
	zr := openZip(t, map[string]string{"index.js": strings.Repeat("x", 200)})

	_, err := extractZip(zr, filepath.Join(t.TempDir(), "plugin"), 100)
	require.ErrorIs(t, err, errInvalidArchive)

	// The parent of dest is a file, so creating it fails.
	notADir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADir, nil, 0o600))
	_, err = extractZip(zr, filepath.Join(notADir, "plugin"), 1000)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errInvalidArchive)
}

// Entry names that cannot be laid out on a filesystem are the same on every download, so they are
// errInvalidArchive too, unlike a failure that says nothing about the archive.
func TestExtractZipEntryNamesThatCannotBeLaidOutAreAnInvalidArchive(t *testing.T) {
	longName := strings.Repeat("a", 300)
	for name, zr := range map[string]*zip.Reader{
		"file and directory of the same name": openZipEntries(t, []string{"lib", "lib/a.js"}),
		"directory then file of that name":    openZipEntries(t, []string{"lib/a.js", "lib"}),
		"entry naming the root":               openZipEntries(t, []string{"."}),
		"entry naming a parent":               openZipEntries(t, []string{"a/.."}),
		"name too long":                       openZipEntries(t, []string{longName}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extractZip(zr, filepath.Join(t.TempDir(), "plugin"), 1000)
			require.Error(t, err)
			assert.ErrorIs(t, err, errInvalidArchive)
		})
	}
}

// openZipEntries is openZip for entry names in a given order, which a map does not keep.
func openZipEntries(t *testing.T, names []string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte("x"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return zr
}
