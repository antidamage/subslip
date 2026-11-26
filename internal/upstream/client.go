// Package upstream makes SubSlip's own JSON calls to the Subsonic server it
// sits in front of.
//
// File map:
//   - client.go  (this file) one call: build the URL, send, decode the
//     envelope, classify the failure.
//   - kind.go    works out whether the upstream is Octo, Navidrome or another
//     Subsonic server, for diagnostics.
//
// Used by internal/mix (getSimilarSongs2, getSong, getArtist, search3),
// internal/auth (ping) and internal/doctor. Proxied client traffic does not go
// through here; it goes through internal/server's reverse proxy.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/antidamage/subslip/internal/subsonic"
)

// maxBody bounds how much of an upstream answer is read.
const maxBody = 16 << 20

// Client calls one upstream.
type Client struct {
	base *url.URL
	http *http.Client
}

// New returns a client for the upstream at base (which may carry a path such
// as /navidrome). The http.Client has no overall timeout: callers pass a
// context, so a client's own request can wait as long as the client does.
func New(base *url.URL) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 0
	return &Client{base: base, http: &http.Client{Transport: transport}}
}

// Base is the upstream URL.
func (c *Client) Base() *url.URL { return c.base }

// Response is a decoded "subsonic-response" object.
type Response struct {
	Body map[string]any // the whole subsonic-response object
	Took time.Duration
}

// FailedError is an upstream answer with status="failed".
type FailedError struct {
	Code    int
	Message string
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("upstream answered failed: code %d: %s", e.Code, e.Message)
}

// HTTPError is an upstream answer that was not a 200 Subsonic envelope.
type HTTPError struct {
	Status      int
	ContentType string
	Body        []byte
}

func (e *HTTPError) Error() string { return fmt.Sprintf("upstream HTTP %d", e.Status) }

// IsAuthFailure reports whether err is the upstream rejecting credentials:
// Subsonic error codes 40-44 (wrong credentials, token or API key problems,
// not authorised for this) or HTTP 401/403.
func IsAuthFailure(err error) bool {
	var failed *FailedError
	if errors.As(err, &failed) {
		return failed.Code >= 40 && failed.Code <= 44
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden
	}
	return false
}

// Call runs method with the caller's credentials plus params, always as JSON.
// It returns a *FailedError, an *HTTPError, or a transport error. Transport
// errors carry the URL, so log them only through internal/redact.
func (c *Client) Call(ctx context.Context, method string, auth subsonic.Auth, params url.Values) (*Response, error) {
	query := auth.Values()
	for key, vals := range params {
		query[key] = vals
	}
	query.Set("f", "json")
	if query.Get("c") == "" {
		query.Set("c", "subslip")
	}
	target := c.endpoint(method)
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}
	}
	envelope, err := decodeEnvelope(body)
	if err != nil {
		return nil, &HTTPError{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}
	}
	if status, _ := envelope["status"].(string); status != "ok" {
		return nil, failedFrom(envelope)
	}
	return &Response{Body: envelope, Took: time.Since(started)}, nil
}

// endpoint is base + "/rest/" + method.
func (c *Client) endpoint(method string) *url.URL {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/rest/" + method
	u.RawPath = ""
	return &u
}

func decodeEnvelope(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var outer map[string]any
	if err := dec.Decode(&outer); err != nil {
		return nil, err
	}
	inner, ok := outer["subsonic-response"].(map[string]any)
	if !ok {
		return nil, errors.New("no subsonic-response object")
	}
	return inner, nil
}

func failedFrom(envelope map[string]any) *FailedError {
	failed := &FailedError{Code: 0, Message: "unknown error"}
	if e, ok := envelope["error"].(map[string]any); ok {
		if n, ok := e["code"].(json.Number); ok {
			if v, err := n.Int64(); err == nil {
				failed.Code = int(v)
			}
		}
		if m, ok := e["message"].(string); ok {
			failed.Message = m
		}
	}
	return failed
}

// Songs extracts the song list under element (e.g. "similarSongs2" or
// "searchResult3"). Servers send either an array or, for one song, an object.
func Songs(body map[string]any, element string) []subsonic.Song {
	holder, _ := body[element].(map[string]any)
	switch v := holder["song"].(type) {
	case []any:
		songs := make([]subsonic.Song, 0, len(v))
		for _, item := range v {
			if song, ok := item.(map[string]any); ok {
				songs = append(songs, song)
			}
		}
		return songs
	case map[string]any:
		return []subsonic.Song{v}
	}
	return nil
}

// Str reads a string field from a song or other object.
func Str(object map[string]any, key string) string {
	switch v := object[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}
