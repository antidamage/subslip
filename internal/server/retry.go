// retry.go asks the upstream once more when a build came back empty (spec
// 4.11). Octo, for one, answers 0 tracks when its YouTube search shim fails
// and has the full radio a few seconds later; the answer itself cannot tell
// that apart from a seed nothing is similar to.
//
// Works with: playhook.go (afterBackgroundFlight, backgroundJob,
// backgroundFailed), instantmix.go (finishAfterMiss), backoff.go.
//
// One retry per (user, song) per hour, EMPTY_RETRY_SECONDS after the empty
// answer, through the background slots and cap. It is skipped while the user
// is resting and when a fresh mix exists by then. It never retries itself
// and never counts toward the backoff. It does not change what the request
// that got the empty answer saw.
package server

import (
	"context"
	"sync"
	"time"

	"github.com/antidamage/subslip/internal/subsonic"
)

const retryMemory = time.Hour

type retryMark struct {
	at     time.Time
	filled bool
}

type retries struct {
	mu    sync.Mutex
	marks map[string]*retryMark
}

func newRetries() *retries { return &retries{marks: map[string]*retryMark{}} }

// claim records a retry for key, or reports false if one ran within the hour.
func (r *retries) claim(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.marks[key]; ok && now.Sub(m.at) < retryMemory {
		return false
	}
	if len(r.marks) > 1000 {
		for k, m := range r.marks {
			if now.Sub(m.at) >= retryMemory {
				delete(r.marks, k)
			}
		}
	}
	r.marks[key] = &retryMark{at: now}
	return true
}

// active reports whether key's retry is the one now running (within the hour).
func (r *retries) active(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.marks[key]
	return ok && now.Sub(m.at) < retryMemory
}

// fill marks key's retry as having stored a mix; true the first time only.
func (r *retries) fill(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.marks[key]
	if !ok || m.filled || now.Sub(m.at) >= retryMemory {
		return false
	}
	m.filled = true
	return true
}

// afterEmpty schedules the one retry for (user, id) after an empty build.
func (s *Server) afterEmpty(user, id string, a subsonic.Auth) {
	if s.cfg.EmptyRetry <= 0 || s.store == nil || s.cfg.Passthrough {
		return
	}
	key := user + "\x00" + id
	if s.retries.active(key, s.now()) {
		return // this was the retry; it does not retry again
	}
	time.AfterFunc(s.cfg.EmptyRetry, func() {
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("recovered from a panic while queueing an empty retry", "user", user, "song", id)
			}
		}()
		if s.backoff.resting(user) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		entry, err := s.store.Get(ctx, user, id)
		cancel()
		if err == nil && entry.Age(s.now()) < s.cfg.CacheRefresh {
			return
		}
		if !s.retries.claim(key, s.now()) {
			return
		}
		s.rec.Count("empty-retry")
		s.rec.Record("empty-retry", user, id, "the upstream answered with no tracks; asking once more")
		s.sched.Enqueue(user, id, s.backgroundJob(user, id, a, true))
	})
}

// isRetry reports whether (user, id)'s current build is the empty retry.
func (s *Server) isRetry(user, id string) bool {
	return s.retries.active(user+"\x00"+id, s.now())
}

// retryFilled counts a retry that stored a mix.
func (s *Server) retryFilled(user, id string) {
	if s.retries.fill(user+"\x00"+id, s.now()) {
		s.rec.Count("empty-retry-filled")
	}
}
