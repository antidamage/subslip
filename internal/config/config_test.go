package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := FromMap(map[string]string{})
	if cfg.UpstreamURL.String() != "http://127.0.0.1:4533" || cfg.ListenAddr != "0.0.0.0:5276" {
		t.Fatalf("unexpected defaults: %s %s", cfg.UpstreamURL, cfg.ListenAddr)
	}
	if !cfg.RequireAuth || cfg.PrecacheMode != "true" || cfg.MissWait != 22*time.Second || cfg.InterimBudget != 6*time.Second || cfg.BackgroundMaxConcurrent != 2 || cfg.CacheTTL != 24*time.Hour || cfg.MinMixTracks != 15 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("defaults produced warnings: %v", cfg.Warnings)
	}
}

func TestAliasesAndBadValues(t *testing.T) {
	cfg := FromMap(map[string]string{
		"OCTO_URL": "http://octo:5274/", "PROXY_PORT": "6000", "CACHE_TTL_HOURS": "soon",
		"REQUIRE_AUTH": "maybe", "MIN_MIX_TRACKS": "40", "BASE_PATH": "subslip/", "SUBSLIP_MODE": "Passthrough",
	})
	if cfg.UpstreamURL.String() != "http://octo:5274" || cfg.ListenAddr != "0.0.0.0:6000" {
		t.Fatalf("aliases not applied: %s %s", cfg.UpstreamURL, cfg.ListenAddr)
	}
	if cfg.CacheTTL != 24*time.Hour || !cfg.RequireAuth || cfg.MinMixTracks != cfg.MaxMixTracks {
		t.Fatalf("bad values not replaced by defaults: %+v", cfg)
	}
	if cfg.BasePath != "/subslip" || !cfg.Passthrough {
		t.Fatalf("base path or mode wrong: %q %v", cfg.BasePath, cfg.Passthrough)
	}
	if len(cfg.Warnings) != 3 {
		t.Fatalf("want 3 warnings, got %v", cfg.Warnings)
	}
}

func TestBadUpstreamFallsBack(t *testing.T) {
	cfg := FromMap(map[string]string{"UPSTREAM_URL": "octo:5274"})
	if cfg.UpstreamURL.String() != "http://127.0.0.1:4533" || len(cfg.Warnings) != 1 {
		t.Fatalf("got %s %v", cfg.UpstreamURL, cfg.Warnings)
	}
}

func TestDotenv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# comment\nexport UPSTREAM_URL=\"http://a:1/navidrome\"\nSTATUS_TOKEN='x y'\nLOG_LEVEL=debug # trailing\nnot a line\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	values, ok, err := ReadDotenv(path)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if values["UPSTREAM_URL"] != "http://a:1/navidrome" || values["STATUS_TOKEN"] != "x y" || values["LOG_LEVEL"] != "debug" {
		t.Fatalf("parsed %v", values)
	}
	if _, ok, _ := ReadDotenv(filepath.Join(t.TempDir(), "missing")); ok {
		t.Fatal("missing file reported as found")
	}
}

func TestBadUpstreamWarningHidesValue(t *testing.T) {
	for _, raw := range []string{"user:hunter2@host:4533", "ftp://user:hunter2@host"} {
		cfg := FromMap(map[string]string{"UPSTREAM_URL": raw})
		for _, w := range cfg.Warnings {
			if strings.Contains(w, "hunter2") {
				t.Fatalf("warning echoes the password: %q", w)
			}
		}
	}
}

// TestPrecacheAutoReadAsTrue: v0.1's default "auto" must not stop pre-caching
// after an upgrade; it is read as true, with a warning.
func TestPrecacheAutoReadAsTrue(t *testing.T) {
	cfg := FromMap(map[string]string{"PRECACHE_ON_PLAY": "auto", "MISS_WAIT_SECONDS": "0.5"})
	if cfg.PrecacheMode != "true" || len(cfg.Warnings) != 1 {
		t.Fatalf("auto: mode %q warnings %v", cfg.PrecacheMode, cfg.Warnings)
	}
	if cfg.MissWait != 500*time.Millisecond {
		t.Fatalf("fractional MISS_WAIT_SECONDS: %v", cfg.MissWait)
	}
	if off := FromMap(map[string]string{"PRECACHE_ON_PLAY": "false", "MISS_WAIT_SECONDS": "0"}); off.PrecacheMode != "false" || off.MissWait != 0 {
		t.Fatalf("opt-outs: %q %v", off.PrecacheMode, off.MissWait)
	}
}
