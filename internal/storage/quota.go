package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
)

var (
	// ErrQuotaExceeded is returned when an ingest operation would breach the allocated storage quota.
	ErrQuotaExceeded = errors.New("storage quota exceeded")
)

// QuotaGuard tracks allocated vs used storage on an agent node.
// It performs atomic pre-flight reservations to guarantee that incoming
// downloads and ingests never exceed the admin-allocated quota.
type QuotaGuard struct {
	mu                sync.RWMutex
	allocatedMaxBytes uint64 // 0 means unlimited
	usedBytes         uint64
	reservedBytes     uint64
}

// NewQuotaGuard initializes a QuotaGuard with an allocated quota ceiling.
func NewQuotaGuard(allocatedMaxBytes uint64) *QuotaGuard {
	return &QuotaGuard{
		allocatedMaxBytes: allocatedMaxBytes,
	}
}

// SetAllocatedMaxBytes updates the maximum storage quota allowed for this node.
func (q *QuotaGuard) SetAllocatedMaxBytes(maxBytes uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.allocatedMaxBytes = maxBytes
}

// AllocatedMaxBytes returns the current storage ceiling (0 = unlimited).
func (q *QuotaGuard) AllocatedMaxBytes() uint64 {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.allocatedMaxBytes
}

// UpdateUsedBytes recalculates or sets the current disk space used by media/scratch.
func (q *QuotaGuard) UpdateUsedBytes(used uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.usedBytes = used
}

// UsedBytes returns total bytes currently occupied by stored media.
func (q *QuotaGuard) UsedBytes() uint64 {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.usedBytes
}

// Reserve checks if the incoming payload fits within the quota ceiling and
// reserves the headroom in-flight. Returns ErrQuotaExceeded if capacity is breached.
func (q *QuotaGuard) Reserve(incomingBytes uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.allocatedMaxBytes > 0 && (q.usedBytes+q.reservedBytes+incomingBytes) > q.allocatedMaxBytes {
		return fmt.Errorf("%w: allocated %d B, in-use %d B, reserved %d B, requested %d B",
			ErrQuotaExceeded, q.allocatedMaxBytes, q.usedBytes, q.reservedBytes, incomingBytes)
	}
	q.reservedBytes += incomingBytes
	return nil
}

// Release releases a previously reserved in-flight allocation.
func (q *QuotaGuard) Release(incomingBytes uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.reservedBytes >= incomingBytes {
		q.reservedBytes -= incomingBytes
	} else {
		q.reservedBytes = 0
	}
}

// Commit converts a temporary reservation into permanent usedBytes.
func (q *QuotaGuard) Commit(incomingBytes uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.reservedBytes >= incomingBytes {
		q.reservedBytes -= incomingBytes
	} else {
		q.reservedBytes = 0
	}
	q.usedBytes += incomingBytes
}

// Reclaim adjusts usedBytes downwards when files are deleted.
func (q *QuotaGuard) Reclaim(freedBytes uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.usedBytes >= freedBytes {
		q.usedBytes -= freedBytes
	} else {
		q.usedBytes = 0
	}
}

// AvailableBytes returns the remaining write headroom before reaching quota.
func (q *QuotaGuard) AvailableBytes() uint64 {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.allocatedMaxBytes == 0 {
		return ^uint64(0) // Unlimited
	}
	totalProjected := q.usedBytes + q.reservedBytes
	if totalProjected >= q.allocatedMaxBytes {
		return 0
	}
	return q.allocatedMaxBytes - totalProjected
}

// ScanDirSize scans a directory tree and returns total disk usage in bytes.
func ScanDirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err == nil {
				total += uint64(info.Size())
			}
		}
		return nil
	})
	return total, err
}
