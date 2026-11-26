// kind.go identifies what the upstream is, for status and doctor output only.
// Nothing in SubSlip behaves differently by kind.
//
// Works with: client.go. Used by internal/diag (status) and internal/doctor.
//
// Octo reports itself as "navidrome" in ping (it fronts a Navidrome), so it is
// recognised by the octo* extensions in getOpenSubsonicExtensions, which the
// OpenSubsonic spec lets callers read without credentials.
package upstream

import (
	"context"
	"strings"

	"github.com/antidamage/subslip/internal/subsonic"
)

// Kind values.
const (
	KindOcto        = "octo"
	KindNavidrome   = "navidrome"
	KindSubsonic    = "subsonic"
	KindUnreachable = "unreachable"
)

// Identity is what Detect learned about the upstream.
type Identity struct {
	Kind          string `json:"kind"`
	Type          string `json:"type,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
	OpenSubsonic  bool   `json:"openSubsonic"`
	Error         string `json:"error,omitempty"`
}

// Detect asks the upstream for its OpenSubsonic extensions without
// credentials. A server that refuses with an auth error is reachable but not
// OpenSubsonic-aware, and is reported as plain "subsonic".
func (c *Client) Detect(ctx context.Context) Identity {
	resp, err := c.Call(ctx, "getOpenSubsonicExtensions", subsonic.Auth{Client: "subslip"}, nil)
	if err != nil {
		if IsAuthFailure(err) {
			return Identity{Kind: KindSubsonic}
		}
		if _, ok := err.(*FailedError); ok {
			return Identity{Kind: KindSubsonic}
		}
		if httpErr, ok := err.(*HTTPError); ok {
			return Identity{Kind: KindSubsonic, Error: httpErr.Error()}
		}
		return Identity{Kind: KindUnreachable, Error: "cannot reach upstream"}
	}
	id := Identity{
		Kind:          KindSubsonic,
		Type:          Str(resp.Body, "type"),
		ServerVersion: Str(resp.Body, "serverVersion"),
	}
	id.OpenSubsonic, _ = resp.Body["openSubsonic"].(bool)
	if strings.EqualFold(id.Type, "navidrome") {
		id.Kind = KindNavidrome
	}
	if extensions, ok := resp.Body["openSubsonicExtensions"].([]any); ok {
		for _, ext := range extensions {
			if m, ok := ext.(map[string]any); ok && strings.HasPrefix(Str(m, "name"), "octo") {
				id.Kind = KindOcto
				break
			}
		}
	}
	return id
}
