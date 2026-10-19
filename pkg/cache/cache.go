// Package cache defines the process-wide budget for in-memory key-value
// caches and aggregates their live usage for the settings dashboard.
package cache

import (
	"sort"
	"sync"
)

// MaxTotalBytes is the global ceiling for all in-memory key-value caches
// combined. Traffic-scaled caches (e.g. the play deduper) evict their
// oldest entries first when they exceed the budget; the remaining caches
// are structurally bounded (node/file counts) and report usage only.
const MaxTotalBytes int64 = 50 << 20 // 50 MB

// Snapshot is one cache's live usage.
type Snapshot struct {
	Name    string `json:"name"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
	Evicted int64  `json:"evicted,omitempty"` // cumulative oldest-entry evictions
}

// Reporter returns a cache's current usage.
type Reporter func() Snapshot

// Registry aggregates named reporters. The zero value is ready to use;
// re-registering a name replaces its reporter (idempotent wiring).
type Registry struct {
	mu        sync.Mutex
	reporters map[string]Reporter
}

// Default is the process-wide registry backing the settings dashboard.
var Default Registry

// Register adds or replaces the reporter for name.
func (r *Registry) Register(name string, rep Reporter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reporters == nil {
		r.reporters = make(map[string]Reporter)
	}
	r.reporters[name] = rep
}

// Snapshot returns all registered caches sorted by name plus the combined
// byte total.
func (r *Registry) Snapshot() ([]Snapshot, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Snapshot, 0, len(r.reporters))
	var total int64
	for name, rep := range r.reporters {
		snap := Snapshot{Name: name}
		if rep != nil {
			snap = rep()
		}
		if snap.Name == "" {
			snap.Name = name
		}
		out = append(out, snap)
		total += snap.Bytes
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, total
}
