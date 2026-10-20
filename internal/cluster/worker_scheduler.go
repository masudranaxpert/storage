package cluster

import (
	"fmt"

	"stream/internal/telemetry"
)

// ErrNoWorker is returned when no processing VPS satisfies the requirements.
var ErrNoWorker = fmt.Errorf("no processing worker online: need at least one enabled VPS with aria2c and ffmpeg installed")

// WorkerCandidates filters the live cluster down to nodes allowed to run
// file jobs: online, admin processing-enabled, with aria2c + ffmpeg ready,
// and reachable as a dispatch target (the coordinator process itself has no
// ingest agent, so master nodes are excluded).
func WorkerCandidates(nodes []*NodeRecord, profiles map[string]ProcessingProfile) []*NodeRecord {
	out := make([]*NodeRecord, 0)
	for _, n := range nodes {
		if n.Status != StatusOnline {
			continue
		}
		if IsCoordinatorNode(n.Metrics.NodeID) {
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
func ReceiverCandidates(nodes []*NodeRecord) map[string]bool {
	allowed := make(map[string]bool)
	for _, n := range nodes {
		if n.Status != StatusOnline {
			continue
		}
		if IsCoordinatorNode(n.Metrics.NodeID) {
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
// fast path); otherwise the eligible worker with the most free disk wins.
func PickProcessingWorker(candidates []*NodeRecord, blockOwnerID string, requiredBytes uint64) *NodeRecord {
	w, _ := PickProcessingWorkerWithLimits(candidates, blockOwnerID, requiredBytes, nil, nil)
	return w
}

// PickProcessingWorkerWithLimits selects an eligible worker considering active concurrency and scratch disk limits.
func PickProcessingWorkerWithLimits(candidates []*NodeRecord, blockOwnerID string, requiredBytes uint64,
	profiles map[string]ProcessingProfile, activeJobs map[string]int) (*NodeRecord, error) {
	if len(candidates) == 0 {
		return nil, ErrNoWorker
	}
	var atConcurrencyLimit int
	var atDiskLimit int
	var best *NodeRecord
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
