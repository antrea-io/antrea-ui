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
	"strconv"
	"strings"
)

const (
	// #nosec G101: not credentials
	refreshTokenCookieName = "antrea-ui-refresh-token"
	refreshTokenCookiePath = "/auth"

	// This is to ensure maximum browser compatibility.
	// See http://browsercookielimits.iain.guru/
	// The max size includes all attributes, not just the cookie value itself.
	maxCookieSize = 4093
	// This is chosen arbitrarily. We should not need to store anything larger than that in a
	// cookie.
	maxChunksPerCookie = 4
)

func SetRefreshTokenCookie(w http.ResponseWriter, token string, cookieSecure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshTokenCookieName,
		Value:    token,
		Path:     refreshTokenCookiePath,
		MaxAge:   0, // make it a session cookie
		Secure:   cookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func UnsetRefreshTokenCookie(req *http.Request, w http.ResponseWriter) (string, bool) {
	cookie, err := req.Cookie(refreshTokenCookieName)
	if err != nil {
		// no cookie
		return "", false
	}
	token := cookie.Value
	http.SetCookie(w, &http.Cookie{
		Name:   refreshTokenCookieName,
		Value:  "",
		Path:   refreshTokenCookiePath,
		MaxAge: -1,
	})
	return token, true
}

func GetRefreshTokenFromCookie(req *http.Request) (string, bool) {
	cookie, err := req.Cookie(refreshTokenCookieName)
	if err != nil {
		// no cookie
		return "", false
	}
	return cookie.Value, true
}

func buildAttributes(cookie *http.Cookie) string {
	attributes := make([]string, 0)
	if cookie.Path != "" {
		attributes = append(attributes, fmt.Sprintf("Path=%s", cookie.Path))
	}
	if cookie.Domain != "" {
		attributes = append(attributes, fmt.Sprintf("Domain=%s", cookie.Path))
	}
	if !cookie.Expires.IsZero() {
		attributes = append(attributes, fmt.Sprintf("Expires=%s", cookie.Expires.UTC().Format(http.TimeFormat)))
	}
	if cookie.MaxAge > 0 {
		attributes = append(attributes, fmt.Sprintf("MaxAge=%d", cookie.MaxAge))
	} else if cookie.MaxAge < 0 {
		attributes = append(attributes, "MaxAge=0")
	}
	if cookie.HttpOnly {
		attributes = append(attributes, "HttpOnly")
	}
	if cookie.Secure {
		attributes = append(attributes, "Secure")
	}
	switch cookie.SameSite {
	case http.SameSiteDefaultMode:
		// Skip, default mode is obtained by not emitting the attribute.
	case http.SameSiteNoneMode:
		attributes = append(attributes, "SameSite=None")
	case http.SameSiteLaxMode:
		attributes = append(attributes, "SameSite=Lax")
	case http.SameSiteStrictMode:
		attributes = append(attributes, "SameSite=Strict")
	}
	return strings.Join(attributes, "; ")
}

// Most browsers have a size limit on cookies. For cookies that may be larger than ~4KB, we need the
// ability to split them into multiple chunks.
// All cookies follow this format: "<cookie-name>=<cookie-value>[; <cookie-attributes]"
// For the first chunk, "<number-of-chunks>:" is prepended to the cookie value.
// For all subsequent chunks, "-<chunk-index>" is appendend to the cookie name.
// We assume that <number-of-chunks> and <chunk-index> are represented with a single digit, which
// holds true as long as maxChunksPerCookie < 10.
func splitLargeCookie(cookie *http.Cookie) ([]string, error) {
	if err := cookie.Valid(); err != nil {
		return nil, fmt.Errorf("invalid cookie: %v", err)
	}
	if strings.ContainsAny(cookie.Value, " ,") {
		return nil, fmt.Errorf("cookie value should not include spaces or commas")
	}

	attributes := buildAttributes(cookie)

	valueSize := len(cookie.Value)
	maxValueSizePerChunk := maxCookieSize
	if attributes != "" {
		// 2 is for the semi-colon and space between cookie value and attributes
		maxValueSizePerChunk -= len(attributes) + 2
	}
	// 1 is for the equal sign between cookie name and value
	maxValueSizePerChunk -= len(cookie.Name) + 1
	// when splitting the cookie, we need to add some "metadata" to help keep track of the
	// number of chunks and the current index.
	maxValueSizePerChunk -= 2
	if valueSize > maxValueSizePerChunk*maxChunksPerCookie {
		return nil, fmt.Errorf("cookie is too large to be split into at most %d chunks", maxChunksPerCookie)
	}

	makeCookie := func(name, value string) string {
		s := fmt.Sprintf("%s=%s", name, value)
		if attributes != "" {
			s += "; " + attributes
		}
		return s
	}

	// this is guaranteed to be <= maxChunksPerCookie thanks to the check above
	numChunks := 1 + len(cookie.Value)/(maxValueSizePerChunk+1)
	chunks := make([]string, 0, numChunks)

	valueIdx := 0
	for chunkIdx := 0; chunkIdx < numChunks; chunkIdx++ {
		end := valueIdx + maxValueSizePerChunk
		if end > valueSize {
			end = valueSize
		}
		if chunkIdx == 0 {
			// first chunk
			chunks = append(chunks, makeCookie(cookie.Name, fmt.Sprintf("%d:%s", numChunks, cookie.Value[:end])))
		} else {
			chunks = append(chunks, makeCookie(fmt.Sprintf("%s-%d", cookie.Name, chunkIdx), cookie.Value[valueIdx:end]))
		}
		valueIdx = end
	}

	return chunks, nil
}

func joinLargeCookie(name string, cookies []*http.Cookie) (string, []*http.Cookie, error) {
	cookiesByName := make(map[string]*http.Cookie)
	for _, cookie := range cookies {
		if strings.HasPrefix(cookie.Name, name) {
			cookiesByName[cookie.Name] = cookie
		}
	}

	// get the first chunk
	c, ok := cookiesByName[name]
	if !ok {
		return "", nil, fmt.Errorf("no cookie with name '%s'", name)
	}
	if len(c.Value) < 2 || c.Value[1] != ':' {
		return "", nil, fmt.Errorf("invalid format for value of first cookie chunk")
	}
	numChunks, err := strconv.Atoi(c.Value[0:1])
	if err != nil {
		return "", nil, fmt.Errorf("invalid format for value of first cookie chunk")
	}
	chunks := make([]*http.Cookie, numChunks)
	chunks[0] = c

	value := c.Value[2:]
	for chunkIdx := 1; chunkIdx < numChunks; chunkIdx++ {
		name := fmt.Sprintf("%s-%d", name, chunkIdx)
		c, ok := cookiesByName[name]
		if !ok {
			return "", nil, fmt.Errorf("missing cookie chunk with index %d", chunkIdx)
		}
		value += c.Value
		chunks[chunkIdx] = c
	}

	return value, chunks, nil
}

func SetLargeCookie(w http.ResponseWriter, cookie *http.Cookie) error {
	if cookie.Domain != "" {
		return fmt.Errorf("cookie domain should be empty")
	}
	chunks, err := splitLargeCookie(cookie)
	if err != nil {
		return err
	}
	for idx := range chunks {
		w.Header().Add("Set-Cookie", chunks[idx])
	}
	return nil
}

func GetLargeCookieValue(req *http.Request, name string) (string, error) {
	value, _, err := joinLargeCookie(name, req.Cookies())
	return value, err
}

func UnsetLargeCookie(req *http.Request, w http.ResponseWriter, name string, path string) (string, error) {
	value, cookies, err := joinLargeCookie(name, req.Cookies())
	if err != nil {
		return "", err
	}
	for _, cookie := range cookies {
		http.SetCookie(w, &http.Cookie{
			Name:   cookie.Name,
			Value:  "",
			Path:   path,
			MaxAge: -1,
		})
	}
	return value, err
}
