// busy.go answers an Instant Mix the flood rule turns away (spec 5.4): the
// request is proxied to the upstream byte for byte, exactly as without
// SubSlip, and the answer is copied as it streams past. After the client has
// it, a qualifying answer is stored, so the song is a hit next time at no
// extra upstream cost.
//
// Works with: instantmix.go (calls answerBusy; serveHit counts hits on these
// entries), proxy.go (passthrough), internal/upstream (Decode, Songs),
// internal/mix (Tidy).
package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/mix"
	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// busySource marks a cache entry stored from a busy pass-through.
const busySource = "upstream-busy"

// busyCopyLimit is the most of a busy answer SubSlip keeps a copy of.
const busyCopyLimit = 2 << 20

func (s *Server) answerBusy(w http.ResponseWriter, r *http.Request, req *subsonic.Request, format, user, id string, count int) {
	if format != "json" || count < s.cfg.MaxMixTracks {
		s.rec.Count("busy-skipped") // XML or a short count: proxied only
		s.passthrough(w, r, "busy")
		return
	}
	cw := &copyWriter{ResponseWriter: w}
	s.passthrough(cw, r, "busy")
	if (cw.status != 0 && cw.status != http.StatusOK) || cw.over || cw.buf.Len() == 0 {
		s.rec.Count("busy-skipped")
		return
	}
	body, err := decodedBody(cw.buf.Bytes(), w.Header().Get("Content-Encoding"))
	if err != nil {
		s.rec.Count("busy-skipped")
		return
	}
	go s.storeBusy(user, id, req.Method, body)
}

// storeBusy stores a copied busy answer when it is a real mix and nothing
// fresh is stored for the song yet. It never overwrites.
func (s *Server) storeBusy(user, id, method string, body []byte) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("recovered from a panic while storing a busy answer", "user", user, "song", id)
			s.rec.Record("panic", user, id, "storing a busy answer")
		}
	}()
	envelope, err := upstream.Decode(body)
	if err != nil {
		s.rec.Count("busy-skipped")
		return
	}
	songs := mix.Tidy(upstream.Songs(envelope, subsonic.ElementFor(method)), id, s.cfg.MaxMixTracks)
	if len(songs) < s.cfg.MinMixTracks {
		s.rec.Count("busy-skipped")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if entry, err := s.store.Get(ctx, user, id); err == nil && entry.Age(s.now()) < s.cfg.CacheTTL {
		return // a fresh mix is already stored; a busy copy never replaces it
	}
	err = s.store.Put(ctx, &cache.Mix{
		User: user, SongID: id, Songs: songs, Meta: subsonic.MetaFrom(envelope),
		Source: busySource, BuiltAt: s.now(),
	})
	if err != nil {
		s.log.Warn("could not store a busy answer", "user", user, "song", id, "error", err)
		s.rec.Count("cache-error")
		return
	}
	s.rec.Count("busy-stored")
	s.rec.Record("busy-stored", user, id, fmt.Sprintf("%d tracks from a passed-through answer", len(songs)))
}

// decodedBody undoes a gzip Content-Encoding; any other encoding is refused.
func decodedBody(raw []byte, encoding string) ([]byte, error) {
	switch encoding {
	case "", "identity":
		return raw, nil
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(io.LimitReader(zr, busyCopyLimit))
	}
	return nil, fmt.Errorf("content encoding %q not read", encoding)
}

// copyWriter passes everything through unchanged and keeps a copy of the
// body, up to busyCopyLimit.
type copyWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	over   bool
}

func (c *copyWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *copyWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if !c.over {
		if c.buf.Len()+len(b) > busyCopyLimit {
			c.over = true
			c.buf.Reset()
		} else {
			c.buf.Write(b)
		}
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController (the reverse proxy's flushing) reach
// the underlying writer.
func (c *copyWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
