package fileapi

import (
	"fmt"
	"path/filepath"
	"strings"

	"stream/pkg/cluster"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
)

// RequiredHeadroom returns the disk space a file needs on its target block:
// the original size plus 50% for CMAF remux working space.
func RequiredHeadroom(sizeBytes int64) uint64 {
	if sizeBytes <= 0 {
		return 0
	}
	return uint64(sizeBytes) + uint64(sizeBytes)/2
}

// PlacementPolicy is the tier priority for new files:
// Tier 2 (HDD) first, overflow to Tier 1 (SSD), Tier 0 (NVMe) is a
// dedicated hot tier that never receives new ingest.
type PlacementPolicy struct {
	HDDTierID int
	SSDTierID int
	HotTierID int // reserved; never placed on
}

// DefaultPolicy matches the seeded system tiers.
var DefaultPolicy = PlacementPolicy{HDDTierID: storage.IngestTierID, SSDTierID: 1, HotTierID: 0}

// WorkerCandidates filters the live cluster down to nodes allowed to run
// file jobs: online, admin processing-enabled, with aria2c + ffmpeg ready,
// and reachable as a dispatch target (the coordinator process itself has no
// ingest agent, so master nodes are excluded).
func WorkerCandidates(nodes []*cluster.NodeRecord, profiles map[string]cluster.ProcessingProfile) []*cluster.NodeRecord {
	out := make([]*cluster.NodeRecord, 0)
	for _, n := range nodes {
		if n.Status != cluster.StatusOnline {
			continue
		}
		if cluster.IsCoordinatorNode(n.Metrics.NodeID) {
			continue
		}
		profile, ok := profiles[n.Metrics.NodeID]
		if !ok || !profile.Enabled {
			continue
		}
		caps := n.Metrics.Capabilities
		if !caps.HasAria2c || !caps.HasFFmpeg {
			continue
		}
		// Gate: Nodes pending upgrade must complete upgrade before accepting new jobs
		if caps.Version != "" && caps.Version != telemetry.CurrentVersion {
			continue
		}
		out = append(out, n)
	}
	return out
}

// ReceiverCandidates filters the cluster down to nodes that can STORE a
// finished file: any online agent node (it only needs its HTTP agent to
// accept a transfer and serve the byte-range stream). Master is excluded
// because the coordinator process runs no ingest agent. Storage-only VPSes
// qualify even without aria2c/ffmpeg — that is the point of decoupled
// placement.
func ReceiverCandidates(nodes []*cluster.NodeRecord) map[string]bool {
	allowed := make(map[string]bool)
	for _, n := range nodes {
		if n.Status != cluster.StatusOnline {
			continue
		}
		if cluster.IsCoordinatorNode(n.Metrics.NodeID) {
			continue
		}
		// Gate: Nodes pending upgrade must complete upgrade before accepting storage placement
		if n.Metrics.Capabilities.Version != "" && n.Metrics.Capabilities.Version != telemetry.CurrentVersion {
			continue
		}
		allowed[n.Metrics.NodeID] = true
	}
	return allowed
}

// PickProcessingWorker chooses the VPS that runs download + remux. The
// block owner is preferred when it is itself a processing worker (zero-copy
// fast path); otherwise the eligible worker with the most free disk wins so
// PickProcessingWorker chooses the VPS that runs download + remux.
func PickProcessingWorker(candidates []*cluster.NodeRecord, blockOwnerID string, requiredBytes uint64) *cluster.NodeRecord {
	w, _ := PickProcessingWorkerWithLimits(candidates, blockOwnerID, requiredBytes, nil, nil)
	return w
}

// PickProcessingWorkerWithLimits selects an eligible worker considering active concurrency and scratch disk limits.
func PickProcessingWorkerWithLimits(candidates []*cluster.NodeRecord, blockOwnerID string, requiredBytes uint64,
	profiles map[string]cluster.ProcessingProfile, activeJobs map[string]int) (*cluster.NodeRecord, error) {
	if len(candidates) == 0 {
		return nil, ErrNoWorker
	}
	var atConcurrencyLimit int
	var atDiskLimit int
	var best *cluster.NodeRecord
	var bestFree uint64

	for _, c := range candidates {
		nodeID := c.Metrics.NodeID
		maxConcurrent := 2
		if prof, ok := profiles[nodeID]; ok && prof.MaxConcurrentIngests > 0 {
			maxConcurrent = prof.MaxConcurrentIngests
		}
		if active := activeJobs[nodeID]; active >= maxConcurrent {
			atConcurrencyLimit++
			continue
		}

		var free uint64
		for _, d := range c.Metrics.Disks {
			free += d.FreeBytes
		}
		if requiredBytes > 0 && free < requiredBytes*2 {
			atDiskLimit++
			continue
		}

		if nodeID == blockOwnerID {
			return c, nil
		}
		if best == nil || free > bestFree {
			best, bestFree = c, free
		}
	}

	if best != nil {
		return best, nil
	}
	if atConcurrencyLimit == len(candidates) {
		return nil, fmt.Errorf("all processing workers reached their concurrency limit (max concurrent ingests active)")
	}
	if atDiskLimit > 0 {
		return nil, fmt.Errorf("insufficient scratch disk space on processing workers for %d bytes (need ~2x for raw + CMAF)", requiredBytes)
	}
	return nil, ErrNoWorker
}

// ErrNoWorker is returned when no processing VPS satisfies the requirements.
var ErrNoWorker = fmt.Errorf("no processing worker online: need at least one enabled VPS with aria2c and ffmpeg installed")

// PickBlockInTier selects the block inside one tier with the most usable
// headroom that is online, owned by an allowed node, and fits requiredBytes.
// Returns nil when nothing fits.
func PickBlockInTier(tier *storage.TierStatus, requiredBytes uint64, allowedNodes map[string]bool) *storage.BlockStatus {
	if tier == nil {
		return nil
	}
	var best *storage.BlockStatus
	for i := range tier.Blocks {
		bs := tier.Blocks[i]
		if !bs.Online {
			continue
		}
		if allowedNodes != nil && !allowedNodes[bs.Block.NodeID] && !strings.HasPrefix(bs.Block.NodeID, "bucket:") {
			continue
		}
		if freeOf(bs) < requiredBytes {
			continue
		}
		if best == nil || freeOf(bs) > freeOf(*best) {
			cp := bs
			best = &cp
		}
	}
	return best
}

// freeOf is the usable free space on a resolved block.
func freeOf(bs storage.BlockStatus) uint64 {
	if bs.UsedBytes >= bs.UsableBytes {
		return 0
	}
	return bs.UsableBytes - bs.UsedBytes
}

// TierLabel renders the short public id of a tier, e.g. "tier2".
func TierLabel(id int) string {
	return fmt.Sprintf("tier%d", id)
}

// CleanPlacementPath normalizes root and relative mounts ("/", ".") to an empty
// string so agents store files in their configured, permission-owned primary media directory.
func CleanPlacementPath(path string) string {
	cleaned := filepath.Clean(path)
	if cleaned == "/" || cleaned == "." || path == "" {
		return ""
	}
	return cleaned
}
