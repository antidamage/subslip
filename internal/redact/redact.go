// Package redact removes Subsonic credentials from anything SubSlip writes
// down: log lines, status events, error strings and doctor output.
//
// Subsonic carries credentials in the query string (t, s, p, apiKey), so any
// URL that reaches a log, including one inside a wrapped Go error from
// net/http, can leak a password. Every writer in SubSlip passes text through
// String, and internal/logging installs it on every slog attribute.
//
// Used by: internal/logging, internal/diag, internal/doctor, internal/server.
package redact

import (
	"net/url"
	"regexp"
)

// Mask is what a secret is replaced with.
const Mask = "***"

// secretParams are the query parameters that carry credentials. "token" covers
// the Authorization-style tokens some clients and tools add.
var secretParams = []string{"t", "s", "p", "apiKey", "apikey", "token", "password"}

// secretInText matches key=value pairs for those names anywhere in free text,
// for example inside `Get "http://host/rest/ping?u=a&p=secret": dial tcp ...`.
var secretInText = regexp.MustCompile(`(?i)([?&;\s"](?:t|s|p|apikey|token|password)=)[^&\s"';]*`)

// bearer matches "Bearer <token>" in text.
var bearer = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)

// Values returns a copy of v with every credential replaced by Mask.
func Values(v url.Values) url.Values {
	out := url.Values{}
	for key, vals := range v {
		if isSecret(key) {
			out[key] = []string{Mask}
			continue
		}
		out[key] = append([]string(nil), vals...)
	}
	return out
}

// Query redacts a raw query string. A query that does not parse is replaced
// wholesale, since it cannot be checked.
func Query(raw string) string {
	v, err := url.ParseQuery(raw)
	if err != nil {
		return String(raw)
	}
	return Values(v).Encode()
}

// String redacts credentials found anywhere in free text.
func String(s string) string {
	s = secretInText.ReplaceAllString(s, "${1}"+Mask)
	return bearer.ReplaceAllString(s, "${1}"+Mask)
}

func isSecret(key string) bool {
	for _, name := range secretParams {
		if key == name {
			return true
		}
	}
	return false
}
