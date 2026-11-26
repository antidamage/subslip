// Package diag keeps the in-memory record behind /subslip/status: counters by
// outcome, the last 100 events, and the clients seen with the first request
// shape each one sent.
//
// The status document and its handler are in internal/server/status.go.
// Everything written here goes through internal/redact first. Used by
// internal/server.
package diag

import (
	"sort"
	"sync"
	"time"

	"github.com/antidamage/subslip/internal/redact"
)

// ringSize is how many events status keeps.
const ringSize = 100

// Event is one decision SubSlip made.
type Event struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	User   string    `json:"user,omitempty"`
	Song   string    `json:"song,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Client is one client name (the Subsonic "c" parameter).
type Client struct {
	Name       string    `json:"name"`
	FirstSeen  time.Time `json:"firstSeen"`
	LastSeen   time.Time `json:"lastSeen"`
	Requests   int64     `json:"requests"`
	FirstShape string    `json:"firstInstantMixShape,omitempty"`
}

// Recorder is safe for concurrent use.
type Recorder struct {
	mu       sync.Mutex
	started  time.Time
	counters map[string]int64
	ring     []Event
	next     int
	clients  map[string]*Client
}

// NewRecorder starts an empty record.
func NewRecorder() *Recorder {
	return &Recorder{started: time.Now(), counters: map[string]int64{}, clients: map[string]*Client{}}
}

// Count adds one to counter name (an X-SubSlip outcome or an internal event).
func (r *Recorder) Count(name string) {
	r.mu.Lock()
	r.counters[name]++
	r.mu.Unlock()
}

// Record adds an event to the ring. Detail is redacted.
func (r *Recorder) Record(kind, user, song, detail string) {
	e := Event{At: time.Now(), Kind: kind, User: user, Song: song, Detail: redact.String(detail)}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ring) < ringSize {
		r.ring = append(r.ring, e)
		return
	}
	r.ring[r.next] = e
	r.next = (r.next + 1) % ringSize
}

// SeeClient notes a request from client name. shape, when not empty, is kept
// as that client's first Instant Mix request shape.
func (r *Recorder) SeeClient(name, shape string) {
	if name == "" {
		name = "(no c parameter)"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[name]
	if !ok {
		if len(r.clients) >= 200 {
			return
		}
		c = &Client{Name: name, FirstSeen: now}
		r.clients[name] = c
	}
	c.LastSeen = now
	c.Requests++
	if c.FirstShape == "" && shape != "" {
		c.FirstShape = shape
	}
}

// Snapshot is a copy of the record.
type Snapshot struct {
	Started  time.Time        `json:"started"`
	Counters map[string]int64 `json:"counters"`
	Events   []Event          `json:"recentEvents"`
	Clients  []Client         `json:"clientsSeen"`
}

// Snapshot copies the record, newest event first.
func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{Started: r.started, Counters: map[string]int64{}}
	for k, v := range r.counters {
		s.Counters[k] = v
	}
	n := len(r.ring)
	for i := 0; i < n; i++ {
		s.Events = append(s.Events, r.ring[(r.next-1-i+2*n)%n])
	}
	for _, c := range r.clients {
		s.Clients = append(s.Clients, *c)
	}
	sort.Slice(s.Clients, func(i, j int) bool { return s.Clients[i].LastSeen.After(s.Clients[j].LastSeen) })
	return s
}
