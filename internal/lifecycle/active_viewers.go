package lifecycle

import (
	"sync"
	"time"
)

// ActiveViewerTracker tracks live concurrent viewers per media key using ephemeral in-memory heartbeats.
type ActiveViewerTracker struct {
	mu       sync.RWMutex
	sessions map[string]map[string]time.Time // key -> sessionID -> lastSeen
	ttl      time.Duration
}

// NewActiveViewerTracker creates a new tracker with a session expiration TTL (default 60 seconds).
func NewActiveViewerTracker(ttl time.Duration) *ActiveViewerTracker {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	t := &ActiveViewerTracker{
		sessions: make(map[string]map[string]time.Time),
		ttl:      ttl,
	}
	go t.periodicCleanup()
	return t
}

// Heartbeat registers or refreshes a viewer session for a media file key.
func (t *ActiveViewerTracker) Heartbeat(key, sessionID string) int {
	if key == "" || sessionID == "" {
		return 0
	}
	now := time.Now()
	t.mu.Lock()
	sessMap, ok := t.sessions[key]
	if !ok {
		sessMap = make(map[string]time.Time)
		t.sessions[key] = sessMap
	}
	sessMap[sessionID] = now
	count := len(sessMap)
	t.mu.Unlock()
	return count
}

// Count returns the current active viewer count for a key.
func (t *ActiveViewerTracker) Count(key string) int {
	if key == "" {
		return 0
	}
	now := time.Now()
	threshold := now.Add(-t.ttl)

	t.mu.RLock()
	sessMap, ok := t.sessions[key]
	if !ok {
		t.mu.RUnlock()
		return 0
	}
	count := 0
	for _, seen := range sessMap {
		if seen.After(threshold) {
			count++
		}
	}
	t.mu.RUnlock()
	return count
}

// AllCounts returns a snapshot of active viewer counts for all keys with >0 active viewers.
func (t *ActiveViewerTracker) AllCounts() map[string]int {
	now := time.Now()
	threshold := now.Add(-t.ttl)

	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]int, len(t.sessions))
	for k, sessMap := range t.sessions {
		count := 0
		for _, seen := range sessMap {
			if seen.After(threshold) {
				count++
			}
		}
		if count > 0 {
			result[k] = count
		}
	}
	return result
}

func (t *ActiveViewerTracker) periodicCleanup() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		now := time.Now()
		threshold := now.Add(-t.ttl)

		t.mu.Lock()
		for k, sessMap := range t.sessions {
			for sid, seen := range sessMap {
				if !seen.After(threshold) {
					delete(sessMap, sid)
				}
			}
			if len(sessMap) == 0 {
				delete(t.sessions, k)
			}
		}
		t.mu.Unlock()
	}
}
