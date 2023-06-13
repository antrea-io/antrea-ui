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

package cookie

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cookieName = "a-cookie"

func showCookie(cookie *http.Cookie) {
	s := cookie.String()
	if len(s) < 128 {
		fmt.Println(s)
	} else {
		fmt.Printf("%s...%s\n", s[0:64], s[len(s)-64:])
	}
}

func showCookies(cookies []*http.Cookie) {
	for _, cookie := range cookies {
		showCookie(cookie)
	}
}

func TestLargeCookie(t *testing.T) {
	testCases := []struct {
		name              string
		cookie            *http.Cookie
		expectedNumChunks int
	}{
		{
			name: "max single chunk",
			cookie: &http.Cookie{
				Name: cookieName,
				// 1 for "=", 2 for "<number-of-chunks>:"
				Value: strings.Repeat("a", maxCookieSize-len(cookieName)-1-2),
			},
			expectedNumChunks: 1,
		},
		{
			name: "max single chunk + 1",
			cookie: &http.Cookie{
				Name:  cookieName,
				Value: strings.Repeat("a", maxCookieSize-len(cookieName)-1-2+1),
			},
			expectedNumChunks: 2,
		},
		{
			name: "empty value",
			cookie: &http.Cookie{
				Name: cookieName,
			},
			expectedNumChunks: 1,
		},
		{
			name: "max size",
			cookie: &http.Cookie{
				Name:  cookieName,
				Value: strings.Repeat("a", maxChunksPerCookie*(maxCookieSize-len(cookieName)-1-2)),
			},
			expectedNumChunks: maxChunksPerCookie,
		},
		{
			name: "with attributes",
			cookie: &http.Cookie{
				Name:     cookieName,
				Value:    "abc",
				Path:     "/foo/bar",
				Domain:   "",
				MaxAge:   0,
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			},
			expectedNumChunks: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			require.NoError(t, SetLargeCookie(rr, tc.cookie))
			resp := rr.Result()
			cookies := resp.Cookies()
			showCookies(cookies)
			assert.Len(t, cookies, tc.expectedNumChunks)
			req := httptest.NewRequest("", "/", nil)
			for _, cookie := range cookies {
				req.AddCookie(cookie)
			}
			v, err := GetLargeCookieValue(req, tc.cookie.Name)
			require.NoError(t, err)
			assert.Equal(t, tc.cookie.Value, v)
		})
	}
}

func TestSplitLargeCookies(t *testing.T) {
	testCases := []struct {
		name           string
		cookie         *http.Cookie
		expectedErr    string
		expectedChunks []string
	}{
		{
			name: "value too large",
			cookie: &http.Cookie{
				Name:  cookieName,
				Value: strings.Repeat("a", maxChunksPerCookie*(maxCookieSize-len(cookieName)-1-2)+1),
			},
			expectedErr: "cookie is too large",
		},
		{
			name: "large cookie with attributes",
			cookie: &http.Cookie{
				Name: cookieName,
				// maxCookie Size guarantees that we will have 2 chunks
				Value:    strings.Repeat("a", maxCookieSize),
				Path:     "/foo/bar",
				Domain:   "",
				MaxAge:   0,
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			},
			expectedChunks: []string{
				fmt.Sprintf("^%s=2:[a]+; Path=/foo/bar; HttpOnly; Secure; SameSite=Strict$", cookieName),
				fmt.Sprintf("^%s-1=[a]+; Path=/foo/bar; HttpOnly; Secure; SameSite=Strict$", cookieName),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			chunks, err := splitLargeCookie(tc.cookie)
			if tc.expectedErr != "" {
				assert.ErrorContains(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				require.Len(t, chunks, len(tc.expectedChunks))
				for idx := range chunks {
					assert.Regexp(t, tc.expectedChunks[idx], chunks[idx], "Chunk at index %d does not match expected regex", idx)
				}
				for idx := 0; idx < len(chunks)-1; idx++ {
					assert.Len(t, chunks[idx], maxCookieSize, "Chunk at index %d should have max cookie size", idx)
				}
			}
		})
	}
}

func TestJoinLargeCookie(t *testing.T) {
	testCases := []struct {
		name          string
		cookies       []*http.Cookie
		expectedErr   string
		expectedValue string
	}{
		{
			name: "missing first cookie",
			cookies: []*http.Cookie{
				{
					Name: "foo",
				},
			},
			expectedErr: "no cookie with name",
		},
		{
			name: "invalid first cookie 1",
			cookies: []*http.Cookie{
				{
					Name:  cookieName,
					Value: "a",
				},
			},
			expectedErr: "invalid format for value of first cookie chunk",
		},
		{
			name: "invalid first cookie 2",
			cookies: []*http.Cookie{
				{
					Name:  cookieName,
					Value: "xyz",
				},
			},
			expectedErr: "invalid format for value of first cookie chunk",
		},
		{
			name: "invalid first cookie 3",
			cookies: []*http.Cookie{
				{
					Name:  cookieName,
					Value: "y:",
				},
			},
			expectedErr: "invalid format for value of first cookie chunk",
		},
		{
			name: "missing chunk",
			cookies: []*http.Cookie{
				{
					Name: cookieName,
					// joinLargeCookie doesn't require this cookie to have max size
					Value: "3:123",
				},
				// &http.Cookie{
				// 	Name:  cookieName + "-1",
				// 	Value: "456",
				// },
				{
					Name:  cookieName + "-2",
					Value: "789",
				},
			},
			expectedErr: "missing cookie chunk with index 1",
		},
		{
			name: "single chunk",
			cookies: []*http.Cookie{
				{
					Name:  cookieName,
					Value: "1:123",
				},
			},
			expectedValue: "123",
		},
		{
			name: "multiple chunks",
			cookies: []*http.Cookie{
				{
					Name: cookieName,
					// joinLargeCookie doesn't require this cookie to have max size
					Value: "3:123",
				},
				{
					Name:  cookieName + "-1",
					Value: "456",
				},
				{
					Name:  cookieName + "-2",
					Value: "789",
				},
			},
			expectedValue: "123456789",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			value, _, err := joinLargeCookie(cookieName, tc.cookies)
			if tc.expectedErr != "" {
				assert.ErrorContains(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedValue, value)
			}
		})
	}
}

func TestUnsetLargeCookie(t *testing.T) {
	const path = "/foo/bar"
	cookies := []*http.Cookie{
		{
			Name:     cookieName,
			Path:     path,
			HttpOnly: true,
			Value:    "3:123",
		},
		{
			Name:     cookieName + "-1",
			Path:     path,
			HttpOnly: true,
			Value:    "456",
		},
		{
			Name:     cookieName + "-2",
			Path:     path,
			HttpOnly: true,
			Value:    "789",
		},
	}
	req := httptest.NewRequest("", "/", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	v, err := UnsetLargeCookie(req, rr, cookieName, path)
	require.NoError(t, err)
	assert.Equal(t, "123456789", v)
	resp := rr.Result()
	assert.Equal(t, []*http.Cookie{
		{
			Name:   cookieName,
			Path:   path,
			MaxAge: -1,
			Raw:    fmt.Sprintf("%s=; Path=/foo/bar; Max-Age=0", cookieName),
		},
		{
			Name:   cookieName + "-1",
			Path:   path,
			MaxAge: -1,
			Raw:    fmt.Sprintf("%s-1=; Path=/foo/bar; Max-Age=0", cookieName),
		},
		{
			Name:   cookieName + "-2",
			Path:   path,
			MaxAge: -1,
			Raw:    fmt.Sprintf("%s-2=; Path=/foo/bar; Max-Age=0", cookieName),
		},
	}, resp.Cookies())
}
