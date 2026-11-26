// init.go implements `subslip init`: write a starting .env for this machine.
//
// Works with: main.go (dispatch), the repository-root package (EnvExample,
// the embedded .env.example), internal/upstream (Detect, to find and identify
// the server). Install scripts and AGENT_SETUP.md call it, so the same step
// works on every platform.
//
// It never overwrites an existing .env unless --force is given, and it never
// writes credentials: only the upstream URL, the listen address and a new
// random STATUS_TOKEN.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/antidamage/subslip"
	"github.com/antidamage/subslip/internal/upstream"
)

// candidateUpstreams are tried in order when --upstream is not given: Octo's
// default port, then Navidrome's with and without a common base path.
var candidateUpstreams = []string{
	"http://127.0.0.1:5274",
	"http://127.0.0.1:4533",
	"http://127.0.0.1:4533/navidrome",
	"http://127.0.0.1:4533/music",
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	envPath := fs.String("env", ".env", "where to write the settings file")
	upstreamURL := fs.String("upstream", "", "the Subsonic server's URL (found automatically when empty)")
	listen := fs.String("listen", "0.0.0.0:5276", "address SubSlip listens on")
	force := fs.Bool("force", false, "replace an existing settings file")
	fs.Parse(args)

	if _, err := os.Stat(*envPath); err == nil && !*force {
		fmt.Printf("%s already exists; leaving it alone (use --force to replace it)\n", *envPath)
		return 0
	}
	target, kind, err := chooseUpstream(*upstreamURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "subslip init:", err)
		return 1
	}
	token, err := randomToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "subslip init:", err)
		return 1
	}
	body := setValue(subslip.EnvExample, "UPSTREAM_URL", target)
	body = setValue(body, "LISTEN_ADDR", *listen)
	body = setValue(body, "STATUS_TOKEN", token)
	if dir := filepath.Dir(*envPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "subslip init:", err)
			return 1
		}
	}
	if err := os.WriteFile(*envPath, []byte(body), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "subslip init:", err)
		return 1
	}
	fmt.Printf("wrote %s\n  upstream: %s (%s)\n  listen:   %s\n  status token: generated (stored in the file)\n",
		*envPath, target, kind, *listen)
	return 0
}

// chooseUpstream checks the given URL, or finds the first local server that
// answers like a Subsonic server.
func chooseUpstream(given string) (string, string, error) {
	candidates := candidateUpstreams
	if given != "" {
		candidates = []string{strings.TrimRight(given, "/")}
	}
	for _, raw := range candidates {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", "", fmt.Errorf("%q is not a URL", raw)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		id := upstream.New(u).Detect(ctx)
		cancel()
		if id.Kind != upstream.KindUnreachable {
			return raw, id.Kind, nil
		}
	}
	if given != "" {
		return "", "", fmt.Errorf("no Subsonic server answered at %s", given)
	}
	return "", "", fmt.Errorf("no Subsonic server found on %s; pass --upstream", strings.Join(candidateUpstreams, ", "))
}

// setValue replaces the first `KEY=...` line in an env template.
func setValue(body, key, value string) string {
	line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=.*$`)
	replaced := false
	return line.ReplaceAllStringFunc(body, func(old string) string {
		if replaced {
			return old
		}
		replaced = true
		return key + "=" + value
	})
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
