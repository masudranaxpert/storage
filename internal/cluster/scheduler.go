package cluster

import (
	"errors"
	"sort"
)

// ScoredNode holds node telemetry and computed load suitability score.
type ScoredNode struct {
	NodeID   string
	Score    float64
	CPULoad  float64
	FreeRAM  uint64
	FreeDisk uint64
}

// SelectOptimalWorker evaluates cluster nodes and returns the least-loaded
// node for media ingest.
func SelectOptimalWorker(nodes []*NodeRecord) (string, error) {
	if len(nodes) == 0 {
		return "", errors.New("no active cluster nodes available")
	}

	scored := make([]ScoredNode, 0, len(nodes))

	for _, n := range nodes {
		if n.Status != StatusOnline {
			continue
		}

		var totalFreeDisk uint64
		for _, d := range n.Metrics.Disks {
			totalFreeDisk += d.FreeBytes
		}

		// Lower score = less loaded. CPU weighs double because remux and
		// normalization are CPU-bound.
		cpuLoad := n.Metrics.CPU.UsedPercent
		score := (cpuLoad * 2.0) + n.Metrics.Memory.UsedPercent

		// Prefer dedicated VPS workers over any leftover coordinator-named agent.
		if IsCoordinatorNode(n.Metrics.NodeID) {
			score += 15.0
		}

		scored = append(scored, ScoredNode{
			NodeID:   n.Metrics.NodeID,
			Score:    score,
			CPULoad:  cpuLoad,
			FreeRAM:  n.Metrics.Memory.AvailableBytes,
			FreeDisk: totalFreeDisk,
		})
	}

	if len(scored) == 0 {
		return "", errors.New("no online nodes found in cluster")
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score < scored[j].Score
	})

	return scored[0].NodeID, nil
}

// SelectProcessingWorker returns the least-loaded node among those currently
// eligible under their processing reservations. When no node is explicitly
// reserved for processing it falls back to SelectOptimalWorker so ingest
// still lands somewhere.
func SelectProcessingWorker(nodes []*NodeRecord, profiles map[string]ProcessingProfile) (string, error) {
	eligible := make([]*NodeRecord, 0, len(nodes))
	anyConfigured := false

	for _, n := range nodes {
		profile, ok := profiles[n.Metrics.NodeID]
		if !ok {
			profile = DefaultProfile(n.Metrics.NodeID)
		} else if profile.Enabled {
			anyConfigured = true
		}
		if e := CheckEligibility(n, profile); e.Eligible {
			eligible = append(eligible, n)
		}
	}

	if len(eligible) == 0 {
		if !anyConfigured {
			return SelectOptimalWorker(nodes)
		}
		return "", errors.New("no processing-eligible node: reservations exceed live free resources")
	}

	return SelectOptimalWorker(eligible)
}
