package fileapi

import (
	"fmt"

	"stream/pkg/cluster"
	"stream/pkg/storage"
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

// WorkerCandidate filters the live cluster down to nodes allowed to run
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
		allowed[n.Metrics.NodeID] = true
	}
	return allowed
}

// PickProcessingWorker chooses the VPS that runs download + remux. The
// block owner is preferred when it is itself a processing worker (zero-copy
// fast path); otherwise the eligible worker with the most free disk wins so
// the scratch folder (raw + CMAF, roughly 2.2x the source size) fits.
func PickProcessingWorker(candidates []*cluster.NodeRecord, blockOwnerID string, requiredBytes uint64) *cluster.NodeRecord {
	var best *cluster.NodeRecord
	var bestFree uint64
	for i := range candidates {
		c := candidates[i]
		if c.Metrics.NodeID == blockOwnerID {
			return c
		}
		var free uint64
		for _, d := range c.Metrics.Disks {
			free += d.FreeBytes
		}
		if requiredBytes > 0 && free < requiredBytes*2 {
			continue // scratch cannot hold raw + CMAF comfortably
		}
		if best == nil || free > bestFree {
			best, bestFree = c, free
		}
	}
	return best
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
		if allowedNodes != nil && !allowedNodes[bs.Block.NodeID] {
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
