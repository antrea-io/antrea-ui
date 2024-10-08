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
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #nosec G101: test credentials
const sampleKey = `-----BEGIN RSA PRIVATE KEY-----
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
-----END RSA PRIVATE KEY-----`

func TestLoadPrivateKeyFromFile(t *testing.T) {
	fs = afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "key.pem", []byte(sampleKey), 0400))
	privateKey, err := LoadPrivateKeyFromFile("key.pem")
	require.NoError(t, err, "failed to load key from PEM file")
	assert.NoError(t, privateKey.Validate())
}

func TestGeneratePrivateKey(t *testing.T) {
	privateKey, err := GeneratePrivateKey()
	require.NoError(t, err, "failed to generate private key")
	assert.GreaterOrEqual(t, privateKey.N.BitLen(), 2048)
}
