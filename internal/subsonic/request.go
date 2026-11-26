// Package subsonic knows the Subsonic API's request and response shapes.
//
// File map:
//   - request.go  (this file) parses an incoming client request: method name,
//     parameters from the query and from a form POST, credentials, format.
//   - render.go   writes ok and error envelopes in XML or JSON.
//   - xmlsong.go  turns a JSON song object into its XML element.
//
// Used by internal/server (incoming requests), internal/upstream (outgoing
// calls reuse Auth) and internal/mix (song objects).
package subsonic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// maxFormBody bounds how much of a form POST body SubSlip will buffer to read
// parameters from. Larger bodies are left alone and the request is proxied.
const maxFormBody = 64 << 10

// Request is a parsed Subsonic call.
type Request struct {
	// Method is the lower-cased method name without ".view", e.g. "stream".
	Method string
	Params url.Values
	Auth   Auth
}

// Auth is the set of parameters that identify and authenticate a caller.
// SubSlip forwards these unchanged on its own upstream calls.
type Auth struct {
	User, Token, Salt, Password, APIKey, Version, Client string
}

// Style names how the caller authenticates, for request-shape logging. It
// never includes a value.
func (a Auth) Style() string {
	switch {
	case a.APIKey != "":
		return "apiKey"
	case a.Token != "":
		return "token"
	case strings.HasPrefix(a.Password, "enc:"):
		return "password-enc"
	case a.Password != "":
		return "password"
	}
	return "none"
}

// UserOrDefault is the name used to key per-user state (cache rows, background
// slots, pre-cache users). A caller with only an API key is keyed by a short
// hash of that key, so two keys belonging to different users never share
// state. The hash is not reversible to the key.
func (a Auth) UserOrDefault() string {
	switch {
	case a.User != "":
		return a.User
	case a.APIKey != "":
		sum := sha256.Sum256([]byte(a.APIKey))
		return "apikey:" + hex.EncodeToString(sum[:6])
	}
	return "default"
}

// Values renders the credentials as query parameters for an upstream call.
func (a Auth) Values() url.Values {
	v := url.Values{}
	set := func(key, value string) {
		if value != "" {
			v.Set(key, value)
		}
	}
	set("u", a.User)
	set("t", a.Token)
	set("s", a.Salt)
	set("p", a.Password)
	set("apiKey", a.APIKey)
	set("c", a.Client)
	version := a.Version
	if version == "" {
		version = "1.16.1"
	}
	v.Set("v", version)
	return v
}

// MethodFromPath returns the lower-cased Subsonic method for a path such as
// "/rest/getSimilarSongs2.view", or "" when the path is not a Subsonic call.
func MethodFromPath(path string) string {
	i := strings.LastIndex(path, "/rest/")
	if i < 0 {
		return ""
	}
	name := path[i+len("/rest/"):]
	if strings.Contains(name, "/") {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(name, ".view"))
}

// Parse reads the method and parameters of r. For a form POST it reads the
// body and puts an identical copy back, so the request can still be proxied
// byte for byte. ok is false when r is not a Subsonic call SubSlip can read.
func Parse(r *http.Request) (req *Request, ok bool) {
	method := MethodFromPath(r.URL.Path)
	if method == "" {
		return nil, false
	}
	params := r.URL.Query()
	if isFormPost(r) {
		form, readable := readFormBody(r)
		if !readable {
			return nil, false
		}
		for key, vals := range form {
			for _, v := range vals {
				params.Add(key, v)
			}
		}
	}
	return &Request{Method: method, Params: params, Auth: authFrom(params)}, true
}

// Format is the response format the client asked for: "xml" (the default),
// "json", or whatever else it sent, lower-cased.
func (r *Request) Format() string {
	f := strings.ToLower(r.Params.Get("f"))
	if f == "" {
		return "xml"
	}
	return f
}

func authFrom(p url.Values) Auth {
	return Auth{
		User: p.Get("u"), Token: p.Get("t"), Salt: p.Get("s"),
		Password: p.Get("p"), APIKey: p.Get("apiKey"),
		Version: p.Get("v"), Client: p.Get("c"),
	}
}

func isFormPost(r *http.Request) bool {
	if r.Method != http.MethodPost || r.Body == nil {
		return false
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mediaType == "application/x-www-form-urlencoded"
}

// readFormBody buffers a small form body, parses it and restores r.Body.
func readFormBody(r *http.Request) (url.Values, bool) {
	if r.ContentLength > maxFormBody {
		return nil, false
	}
	original := r.Body
	body, err := io.ReadAll(io.LimitReader(original, maxFormBody+1))
	if err != nil || len(body) > maxFormBody {
		// Put back what was read in front of the unread rest, so the proxy
		// still sends the whole body.
		r.Body = readCloser{io.MultiReader(bytes.NewReader(body), original), original}
		return nil, false
	}
	original.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, false
	}
	return form, true
}

// readCloser pairs a reader with the original body's Close.
type readCloser struct {
	io.Reader
	io.Closer
}
