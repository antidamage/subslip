// Command subslip is a Subsonic reverse proxy that pre-caches Instant Mixes.
//
// File map:
//   - main.go    (this file) command dispatch and `serve`.
//   - cache.go   `subslip cache stats|purge`.
//   - doctor.go  `subslip doctor`, a thin wrapper over internal/doctor.
//
// Behaviour is specified in specs/subslip.md; the packages under internal/
// each open with a map of the files they work with.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/config"
	"github.com/antidamage/subslip/internal/logging"
	"github.com/antidamage/subslip/internal/server"
)

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

const usage = `SubSlip %s: a Subsonic reverse proxy that pre-caches Instant Mixes.

Usage:
  subslip [serve] [--env FILE]     run the proxy (default)
  subslip doctor [--env FILE] [--user U --password P] [--json]
  subslip cache stats [--env FILE]
  subslip cache purge [--expired] [--env FILE]
  subslip version

Settings come from the environment and from a .env file: --env, else ./.env,
else .env next to the binary. See .env.example.
`

func main() {
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	var code int
	switch command {
	case "serve":
		code = runServe(args)
	case "doctor":
		code = runDoctor(args)
	case "cache":
		code = runCache(args)
	case "version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", command)
		fmt.Fprintf(os.Stderr, usage, version)
		code = 2
	}
	os.Exit(code)
}

// envFlag adds --env to fs and returns a function resolving the file to read.
func envFlag(fs *flag.FlagSet) func() string {
	path := fs.String("env", "", "path to a .env file")
	return func() string {
		if *path != "" {
			return *path
		}
		if _, err := os.Stat(".env"); err == nil {
			return ".env"
		}
		if exe, err := os.Executable(); err == nil {
			return filepath.Join(filepath.Dir(exe), ".env")
		}
		return ".env"
	}
}

// loadConfig reads settings; a relative DB_PATH is resolved against the .env
// file's directory, so a service started from any working directory finds the
// same cache.
func loadConfig(envPath string) (*config.Config, error) {
	cfg, err := config.Load(envPath)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(cfg.DBPath) && cfg.EnvFile != "" {
		cfg.DBPath = filepath.Join(filepath.Dir(cfg.EnvFile), cfg.DBPath)
	}
	return cfg, nil
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	env := envFlag(fs)
	fs.Parse(args)

	cfg, err := loadConfig(env())
	if err != nil {
		fmt.Fprintln(os.Stderr, "subslip:", err)
		return 1
	}
	logger, closeLog, err := logging.New(cfg.LogLevel, cfg.LogFormat, cfg.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "subslip: opening LOG_FILE:", err)
		return 1
	}
	defer closeLog()
	for _, w := range cfg.Warnings {
		logger.Warn("config: " + w)
	}

	store, storeErr := openStore(cfg, logger)
	if store != nil {
		defer store.Close()
	}
	srv := server.New(server.Options{Config: cfg, Logger: logger, Store: store, StoreError: storeErr, Version: version})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Run(ctx)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          logging.StdLogger(logger, slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("SubSlip listening", "version", version, "listen", cfg.ListenAddr,
		"upstream", cfg.UpstreamURL.Redacted(), "passthrough", cfg.Passthrough,
		"precache", cfg.PrecacheMode, "requireAuth", cfg.RequireAuth, "envFile", cfg.EnvFile)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("cannot listen", "listen", cfg.ListenAddr, "error", err)
		return 1
	}
	return 0
}

// openStore opens the cache. A failure is logged and SubSlip carries on as a
// plain proxy; it is reported in status and doctor.
func openStore(cfg *config.Config, logger *slog.Logger) (*cache.Store, string) {
	if cfg.Passthrough {
		return nil, "passthrough mode: cache not opened"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := cache.Open(ctx, cfg.DBPath)
	if err != nil {
		logger.Error("cache database unavailable; running as a plain proxy", "path", cfg.DBPath, "error", err)
		return nil, err.Error()
	}
	return store, ""
}
