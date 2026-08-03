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

package session

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	secretToken = "eyJhbGciOiJSUzI1NiIscretpayload"                  // #nosec G101: not a real credential
	secretKey   = "-----BEGIN RSA PRIVATE KEY-----secretkeymaterial" // #nosec G101: not a real credential
)

// A Credential is passed around freely and could easily end up in a log line or an error string
// by accident. Both of the ways Go turns a value into text must redact it.
func TestCredentialIsUnprintable(t *testing.T) {
	cred := Credential{
		Kind:    KindBearer,
		Token:   []byte(secretToken),
		CertPEM: []byte("cert"),
		KeyPEM:  []byte(secretKey),
	}

	for _, rendered := range []string{
		fmt.Sprintf("%v", cred),
		cred.String(),
		fmt.Sprintf("%v", &cred),
	} {
		assert.NotContains(t, rendered, secretToken)
		assert.NotContains(t, rendered, secretKey)
	}

	b, err := json.Marshal(cred)
	require.NoError(t, err)
	assert.NotContains(t, string(b), secretToken)
	assert.NotContains(t, string(b), secretKey)

	// Structured loggers commonly marshal a wrapping struct, so the redaction has to survive
	// being nested.
	b, err = json.Marshal(map[string]interface{}{"credential": cred})
	require.NoError(t, err)
	assert.NotContains(t, string(b), secretToken)
}

func TestSessionIsUnprintable(t *testing.T) {
	st, _ := newTestStore(t)
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte(secretToken)},
		RefreshToken: []byte("refresh-" + secretToken),
	})
	require.NoError(t, err)

	assert.NotContains(t, fmt.Sprintf("%v", s), secretToken)
	b, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(b), secretToken)
}

func TestCredentialZero(t *testing.T) {
	token := []byte(secretToken)
	cred := Credential{Kind: KindBearer, Token: token}
	cred.Zero()
	assert.Nil(t, cred.Token)
	assert.Equal(t, make([]byte, len(secretToken)), token, "the underlying array should be overwritten, not just dereferenced")
}
