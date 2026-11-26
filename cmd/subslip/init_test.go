package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antidamage/subslip/internal/config"
)

func TestInitWritesUsableSettings(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
			"status": "ok", "version": "1.16.1", "type": "navidrome", "openSubsonic": true,
		}})
	}))
	defer up.Close()
	env := filepath.Join(t.TempDir(), "conf", ".env")
	if code := runInit([]string{"--env", env, "--upstream", up.URL + "/", "--listen", "127.0.0.1:6000"}); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpstreamURL.String() != up.URL || cfg.ListenAddr != "127.0.0.1:6000" || len(cfg.StatusToken) < 32 || len(cfg.Warnings) != 0 {
		t.Fatalf("settings: upstream=%s listen=%s token=%d warnings=%v", cfg.UpstreamURL, cfg.ListenAddr, len(cfg.StatusToken), cfg.Warnings)
	}
	before, _ := os.ReadFile(env)
	runInit([]string{"--env", env, "--upstream", up.URL})
	after, _ := os.ReadFile(env)
	if string(before) != string(after) {
		t.Fatal("init replaced an existing file without --force")
	}
}

func TestInitRefusesUnreachableUpstream(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	if code := runInit([]string{"--env", env, "--upstream", "http://127.0.0.1:1"}); code == 0 {
		t.Fatal("init accepted an upstream that does not answer")
	}
	if _, err := os.Stat(env); err == nil {
		t.Fatal("init wrote a file for an unreachable upstream")
	}
}

func TestSetValueReplacesFirstOnly(t *testing.T) {
	got := setValue("A=1\n# A=2\nA=3\n", "A", "x")
	if !strings.HasPrefix(got, "A=x\n# A=2\nA=3") {
		t.Fatalf("got %q", got)
	}
}
