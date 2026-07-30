package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// Idempotency (INT-07): agents retry on timeout; a retried write —
// capture above all — silently duplicates work and re-spends LLM budget.
// Write routes accept Idempotency-Key; a repeat key replays the stored
// response verbatim without re-dispatching.
//
// v1 store: in-memory, bounded (LRU, cap 1000, TTL 24h). Keys do NOT
// survive restart and are not shared across processes — documented in
// docs/guides/http-api.md (spec §Idempotency; 03 sanctions in-memory for
// v1 provided the restart semantics are documented).

const (
	idemMaxEntries = 1000
	idemTTL        = 24 * time.Hour
)

type idemEntry struct {
	status int
	header http.Header
	body   []byte
	stored time.Time
}

type idemCall struct {
	done  chan struct{}
	entry idemEntry
}

type idemStore struct {
	mu       sync.Mutex
	entries  map[string]idemEntry
	order    []string // insertion order, for bounded eviction
	inflight map[string]*idemCall
	now      func() time.Time
}

func newIdemStore() *idemStore {
	return &idemStore{
		entries:  map[string]idemEntry{},
		inflight: map[string]*idemCall{},
		now:      time.Now,
	}
}

// get returns the stored response for key, if present and fresh. Hits
// refresh LRU position; expired entries are reclaimed lazily.
func (s *idemStore) get(key string) (idemEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return idemEntry{}, false
	}
	if s.now().Sub(e.stored) > idemTTL {
		s.removeLocked(key)
		return idemEntry{}, false
	}
	s.moveToTailLocked(key)
	return e, true
}

// removeLocked deletes key from both the map and the order slice.
func (s *idemStore) removeLocked(key string) {
	delete(s.entries, key)
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

// moveToTailLocked marks key as most-recently-used.
func (s *idemStore) moveToTailLocked(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(append(s.order[:i], s.order[i+1:]...), key)
			return
		}
	}
}

// begin reports whether this caller leads the dispatch for key. Followers
// wait on the leader's call and replay its stored response.
func (s *idemStore) begin(key string) (c *idemCall, leader bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.inflight[key]; ok {
		return c, false
	}
	c = &idemCall{done: make(chan struct{})}
	s.inflight[key] = c
	return c, true
}

// beginOrReplay atomically resolves a key under one lock: a fresh stored
// entry returns (entry, true, nil, false) for replay; an in-flight call
// marks the caller follower (zero, false, c, false); otherwise the caller
// leads (zero, false, c, true). Folding the freshness check into the same
// lock closes the get→begin window where a leader finishing between the two
// calls could elect a second leader (double dispatch — the CI-observed
// flake in TestIdempotency_ConcurrentSameKeySingleDispatch).
func (s *idemStore) beginOrReplay(key string) (idemEntry, bool, *idemCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		if s.now().Sub(e.stored) <= idemTTL {
			s.moveToTailLocked(key)
			return e, true, nil, false
		}
		s.removeLocked(key)
	}
	if c, ok := s.inflight[key]; ok {
		return idemEntry{}, false, c, false
	}
	c := &idemCall{done: make(chan struct{})}
	s.inflight[key] = c
	return idemEntry{}, false, c, true
}

// finish stores the leader's response (bounded, LRU-refreshed) and wakes
// followers.
func (s *idemStore) finish(key string, c *idemCall, e idemEntry) {
	s.mu.Lock()
	if _, ok := s.entries[key]; ok {
		s.removeLocked(key)
	}
	s.order = append(s.order, key)
	s.entries[key] = e
	for len(s.order) > idemMaxEntries {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
	c.entry = e
	delete(s.inflight, key)
	close(c.done)
	s.mu.Unlock()
}

// responseRecorder captures a handler's response for storage and replay.
type responseRecorder struct {
	header http.Header
	status int
	body   []byte
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: http.Header{}}
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) { r.status = status }

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, b...)
	return len(b), nil
}

func replayStored(w http.ResponseWriter, e idemEntry) {
	for k, vs := range e.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Idempotent-Replay", "true")
	w.WriteHeader(e.status)
	_, _ = w.Write(e.body)
}

// idempotent wraps a write handler with Idempotency-Key replay. Requests
// without a key dispatch normally. Concurrent same-key requests are
// deduplicated in flight: followers wait and replay the leader's result
// (stdlib mutex + map — deliberately not x/sync/singleflight so the REST
// layer adds no new direct dependency).
func (rt *Router) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		key := req.Header.Get("Idempotency-Key")
		if key == "" {
			next(w, req)
			return
		}
		stored, replay, c, leader := rt.idem.beginOrReplay(key)
		if replay {
			replayStored(w, stored)
			return
		}
		if !leader {
			// Free the goroutine if our own client hangs up while waiting.
			select {
			case <-c.done:
				replayStored(w, c.entry)
			case <-req.Context().Done():
			}
			return
		}
		// A panicking handler must not wedge the key: finish with a stored
		// 500 (followers replay it, later retries get an answer) before the
		// panic propagates to the server's per-connection recovery.
		defer func() {
			if p := recover(); p != nil {
				log.Printf("api: handler panic under idempotency key: %v", p)
				body, _ := json.Marshal(errorEnvelope{Error: apiError{
					Code:    CodeInternal,
					Message: "handler failed",
				}})
				rt.idem.finish(key, c, idemEntry{
					status: http.StatusInternalServerError,
					header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
					body:   body,
					stored: rt.idem.now(),
				})
				panic(p)
			}
		}()
		rec := newResponseRecorder()
		next(rec, req)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		e := idemEntry{status: rec.status, header: rec.header, body: rec.body, stored: rt.idem.now()}
		rt.idem.finish(key, c, e)
		for k, vs := range rec.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body)
	}
}
