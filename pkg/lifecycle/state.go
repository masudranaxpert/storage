package lifecycle

import (
	"container/list"
	"context"
	"math"
	"sync"
	"time"

	"stream/pkg/cache"
)

// Move records one completed tier transition in a file's history.
type Move struct {
	From   int       `json:"from"`
	To     int       `json:"to"`
	Kind   EdgeKind  `json:"kind"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// FileState tracks per-file heat, residency timestamp, pin state, and recent moves.
type FileState struct {
	Key       string    `json:"key"`
	Score     float64   `json:"score"` // decayed heat at ScoreAt
	ScoreAt   time.Time `json:"score_at"`
	LastHitAt time.Time `json:"last_hit_at"` // zero -> use FileRecord.CreatedAt
	TotalHits int64     `json:"total_hits"`
	TierSince time.Time `json:"tier_since"` // set on ingest completion and on every switch
	PinTierID *int      `json:"pin_tier_id,omitempty"`
	PinUntil  time.Time `json:"pin_until,omitempty"`
	History   []Move    `json:"history"` // last 20 moves (from, to, kind, reason, at)
}

// Decayed computes the file's heat score decayed exponentially to now:
// Score * 2^(-(now - ScoreAt) / halfLifeHours).
func (s FileState) Decayed(now time.Time, halfLifeHours float64) float64 {
	if s.Score <= 0 {
		return 0
	}
	if halfLifeHours <= 0 {
		halfLifeHours = 24
	}
	if s.ScoreAt.IsZero() || !now.After(s.ScoreAt) {
		return s.Score
	}
	elapsedHours := now.Sub(s.ScoreAt).Hours()
	return s.Score * math.Pow(2, -elapsedHours/halfLifeHours)
}

// IsPinned reports whether the file has an active pin at now.
func (s FileState) IsPinned(now time.Time) bool {
	if s.PinTierID == nil {
		return false
	}
	return s.PinUntil.IsZero() || now.Before(s.PinUntil)
}

// HitRecorder buffers play hits in memory and flushes decayed score updates to Store periodically.
type HitRecorder struct {
	mu            sync.Mutex
	counts        map[string]int
	store         Store
	flushInterval time.Duration
}

// NewHitRecorder creates a buffered HitRecorder with a default 10s flush interval.
func NewHitRecorder(store Store, flushInterval time.Duration) *HitRecorder {
	if flushInterval <= 0 {
		flushInterval = 10 * time.Second
	}
	return &HitRecorder{
		counts:        make(map[string]int),
		store:         store,
		flushInterval: flushInterval,
	}
}

// RecordHit increments the in-memory hit counter for key.
func (h *HitRecorder) RecordHit(key string) {
	if key == "" {
		return
	}
	h.mu.Lock()
	h.counts[key]++
	h.mu.Unlock()
}

// Pending returns the buffered hit count for key that has not yet been flushed.
func (h *HitRecorder) Pending(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts[key]
}

// Flush decays each buffered file's score to now, adds the pending hits, updates LastHitAt/TotalHits, and saves to Store.
func (h *HitRecorder) Flush(now time.Time, halfLifeHours float64) error {
	h.mu.Lock()
	if len(h.counts) == 0 {
		h.mu.Unlock()
		return nil
	}
	batch := h.counts
	h.counts = make(map[string]int)
	h.mu.Unlock()

	if h.store == nil {
		return nil
	}
	if halfLifeHours <= 0 {
		halfLifeHours = 24
	}
	now = now.UTC()

	var firstErr error
	for key, hits := range batch {
		if hits <= 0 {
			continue
		}
		st, err := h.store.GetLifecycleState(key)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if st == nil {
			st = &FileState{
				Key:       key,
				TierSince: now,
			}
		}
		decayed := st.Decayed(now, halfLifeHours)
		st.Score = decayed + float64(hits)
		st.ScoreAt = now
		st.LastHitAt = now
		st.TotalHits += int64(hits)
		if st.TierSince.IsZero() {
			st.TierSince = now
		}
		if err := h.store.SaveLifecycleState(st); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Stats reports the pending hit buffer's live usage for the cache registry.
func (h *HitRecorder) Stats() cache.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	var bytes int64
	for key := range h.counts {
		bytes += int64(len(key)) + 16
	}
	return cache.Snapshot{Name: "play_hit_buffer", Entries: len(h.counts), Bytes: bytes}
}

// Run flushes buffered hits every flushInterval until ctx is cancelled, performing one final flush on shutdown.
func (h *HitRecorder) Run(ctx context.Context, nowFn func() time.Time, halfLifeFn func() float64) {
	if nowFn == nil {
		nowFn = time.Now
	}
	if halfLifeFn == nil {
		halfLifeFn = func() float64 { return 24 }
	}
	ticker := time.NewTicker(h.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = h.Flush(nowFn(), halfLifeFn())
			return
		case <-ticker.C:
			_ = h.Flush(nowFn(), halfLifeFn())
		}
	}
}

type dedupeEntry struct {
	compositeKey string
	seenAt       time.Time
}

// dedupeEntryOverhead estimates the per-entry memory cost beyond the key
// string: string header, timestamp, map bucket slot, and list element.
const dedupeEntryOverhead int64 = 128

// PlayDeduper deduplicates play events per (clientIP, fileKey) within a
// configurable TTL using a bounded LRU. It enforces both an entry-count cap
// and a byte budget, evicting the oldest entries first.
type PlayDeduper struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	budget   int64
	bytes    int64
	evicted  int64
	ll       *list.List
	items    map[string]*list.Element
}

// DeduperOption customises a PlayDeduper at construction.
type DeduperOption func(*PlayDeduper)

// WithBudget overrides the memory budget (bytes) for the deduper.
func WithBudget(bytes int64) DeduperOption {
	return func(d *PlayDeduper) {
		if bytes > 0 {
			d.budget = bytes
		}
	}
}

// NewPlayDeduper constructs a bounded LRU deduplicator (default capacity
// 50,000, default TTL 30 minutes, default budget cache.MaxTotalBytes).
func NewPlayDeduper(capacity int, ttl time.Duration, opts ...DeduperOption) *PlayDeduper {
	if capacity <= 0 {
		capacity = 50000
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	d := &PlayDeduper{
		capacity: capacity,
		ttl:      ttl,
		budget:   cache.MaxTotalBytes,
		ll:       list.New(),
		items:    make(map[string]*list.Element, 256),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

// Snapshot reports the deduper's live usage for the cache registry.
func (d *PlayDeduper) Snapshot() cache.Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cache.Snapshot{Name: "play_deduper", Entries: d.ll.Len(), Bytes: d.bytes, Evicted: d.evicted}
}

// evictOldestLocked removes the least-recently-used entry.
func (d *PlayDeduper) evictOldestLocked() {
	oldest := d.ll.Back()
	if oldest == nil {
		return
	}
	entry := oldest.Value.(*dedupeEntry)
	d.bytes -= int64(len(entry.compositeKey)) + dedupeEntryOverhead
	d.ll.Remove(oldest)
	delete(d.items, entry.compositeKey)
	d.evicted++
}

// ShouldCount returns true if (clientIP, key) has not been counted within the TTL window at now, and records the play.
func (d *PlayDeduper) ShouldCount(clientIP, key string, now time.Time) bool {
	if key == "" {
		return false
	}
	composite := clientIP + "|" + key

	d.mu.Lock()
	defer d.mu.Unlock()

	if elem, ok := d.items[composite]; ok {
		entry := elem.Value.(*dedupeEntry)
		if now.Sub(entry.seenAt) < d.ttl {
			d.ll.MoveToFront(elem)
			return false
		}
		entry.seenAt = now
		d.ll.MoveToFront(elem)
		return true
	}

	for d.ll.Len() >= d.capacity {
		d.evictOldestLocked()
	}

	elem := d.ll.PushFront(&dedupeEntry{
		compositeKey: composite,
		seenAt:       now,
	})
	d.items[composite] = elem
	d.bytes += int64(len(composite)) + dedupeEntryOverhead

	// Enforce the memory budget: shed the oldest entries first.
	for d.bytes > d.budget && d.ll.Len() > 1 {
		d.evictOldestLocked()
	}
	return true
}
