// Package logging builds SubSlip's slog logger. Every string attribute and
// every message passes through internal/redact, so a URL with credentials in
// it (for example inside a net/http error) cannot reach a log line.
//
// Used by cmd/subslip. The reverse proxy's and http.Server's own error logs
// are bridged to the same handler by internal/server via StdLogger.
package logging

import (
	"context"
	"io"
	"log"
	"log/slog"
	"os"

	"github.com/antidamage/subslip/internal/redact"
)

// New returns a logger writing to stderr, and also to file when it is set.
// format is "text" or "json"; level is debug, info, warn or error. The second
// result closes the file.
func New(level, format, file string) (*slog.Logger, func() error, error) {
	var out io.Writer = os.Stderr
	closeFn := func() error { return nil }
	if file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, closeFn, err
		}
		out = io.MultiWriter(os.Stderr, f)
		closeFn = f.Close
	}
	return NewWithWriter(out, level, format), closeFn, nil
}

// NewWithWriter is New with an explicit writer, for tests.
func NewWithWriter(out io.Writer, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level), ReplaceAttr: redactAttr}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(out, opts)
	} else {
		h = slog.NewTextHandler(out, opts)
	}
	return slog.New(redactingHandler{h})
}

// StdLogger adapts logger for APIs that want a *log.Logger.
func StdLogger(logger *slog.Logger, level slog.Level) *log.Logger {
	return slog.NewLogLogger(logger.Handler(), level)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, redact.String(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, redact.String(err.Error()))
		}
	}
	return a
}

// redactingHandler also redacts the record's message, which ReplaceAttr does
// not see.
type redactingHandler struct{ slog.Handler }

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, redact.String(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool { clean.AddAttrs(a); return true })
	return h.Handler.Handle(ctx, clean)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return redactingHandler{h.Handler.WithAttrs(attrs)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{h.Handler.WithGroup(name)}
}
