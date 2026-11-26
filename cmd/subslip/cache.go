// cache.go implements `subslip cache stats` and `subslip cache purge`.
//
// Works with: main.go (dispatch, loadConfig), internal/cache. Safe to run
// while the service is running: the database is in WAL mode.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/antidamage/subslip/internal/cache"
)

func runCache(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: subslip cache stats|purge [--expired] [--env FILE]")
		return 2
	}
	action, args := args[0], args[1:]
	fs := flag.NewFlagSet("cache "+action, flag.ExitOnError)
	env := envFlag(fs)
	expired := fs.Bool("expired", false, "purge only mixes older than CACHE_TTL_HOURS")
	fs.Parse(args)

	cfg, err := loadConfig(env())
	if err != nil {
		fmt.Fprintln(os.Stderr, "subslip:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := cache.Open(ctx, cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "subslip: cannot open %s: %v\n", cfg.DBPath, err)
		return 1
	}
	defer store.Close()

	switch action {
	case "stats":
		stats, err := store.Stats(ctx, cfg.CacheTTL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "subslip:", err)
			return 1
		}
		out, _ := json.MarshalIndent(map[string]any{"path": cfg.DBPath, "stats": stats}, "", "  ")
		fmt.Println(string(out))
	case "purge":
		n, err := store.Purge(ctx, *expired, cfg.CacheTTL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "subslip:", err)
			return 1
		}
		fmt.Printf("removed %d mixes from %s\n", n, cfg.DBPath)
	default:
		fmt.Fprintf(os.Stderr, "unknown cache action %q\n", action)
		return 2
	}
	return 0
}
