package gateway

import (
	"sync"
	"time"
	"opencode2api/internal/v2"
)

// v2SessionStore manages V2 sessions for multi-turn tool-calling.
// When a free model returns tool_calls, the session is stored so the
// agentic client can send back tool results via /v1/chat/completions.
type v2SessionStore struct {
	mu      sync.Mutex
	sessions map[string]*v2SessionEntry
	ttl     time.Duration
}

type v2SessionEntry struct {
	Client    *v2.Client
	SessionID string
	LastModel string
	Expires   time.Time
}

func newV2SessionStore(ttl time.Duration) *v2SessionStore {
	return &v2SessionStore{
		sessions: make(map[string]*v2SessionEntry),
		ttl:      ttl,
	}
}

// Get retrieves a session by ID.
func (s *v2SessionStore) Get(id string) *v2SessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[id]
	if !ok || time.Now().After(e.Expires) {
		return nil
	}
	return e
}

// Set stores a session with the given ID.
func (s *v2SessionStore) Set(id string, entry *v2SessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.Expires = time.Now().Add(s.ttl)
	s.sessions[id] = entry
}

// Delete removes a session.
func (s *v2SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// gcPeriodic runs garbage collection every period.
func (s *v2SessionStore) gcPeriodic(period time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for id, entry := range s.sessions {
				if now.After(entry.Expires) {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		case <-done:
			return
		}
	}
}
