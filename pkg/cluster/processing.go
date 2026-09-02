package cluster

import (
	"fmt"
	"strings"

	"stream/pkg/telemetry"
)

// ProcessingProfile is a node's admin-configured processing reservation
// (Kubernetes-requests style). Zero-value fields mean "no requirement".
// Enabled defaults to true for nodes without explicit configuration.
type ProcessingProfile struct {
	NodeID               string `json:"node_id"`
	Enabled              bool   `json:"enabled"`
	ReservedCPUCores     int    `json:"reserved_cpu_cores"`
	ReservedRAMBytes     uint64 `json:"reserved_ram_bytes"`
	PreferredDiskType    string `json:"preferred_disk_type"` // NVME/SSD/HDD, "" = any
	ReservedStorageBytes uint64 `json:"reserved_storage_bytes"`
}

// Eligibility reports whether a node may take processing jobs right now,
// with human-readable reasons when it may not.
type Eligibility struct {
	NodeID           string               `json:"node_id"`
	Eligible         bool                 `json:"eligible"`
	Reasons          []string             `json:"reasons,omitempty"`
	Profile          ProcessingProfile    `json:"profile"`
	FreeRAMBytes     uint64               `json:"free_ram_bytes"`
	FreeCPUCores     int                  `json:"free_cpu_cores"`
	ScratchFreeBytes uint64               `json:"scratch_free_bytes"`
	Disks            []telemetry.DiskStat `json:"disks"`
}

// DefaultProfile keeps unconfigured nodes schedulable with no reservation.
func DefaultProfile(nodeID string) ProcessingProfile {
	return ProcessingProfile{NodeID: nodeID, Enabled: true}
}

// CheckEligibility evaluates a node's live telemetry against a reservation.
// A node is eligible only while free CPU, RAM, and scratch storage (on the
// preferred disk or type when set) each cover the requested amounts.
func CheckEligibility(rec *NodeRecord, profile ProcessingProfile) Eligibility {
	m := rec.Metrics
	e := Eligibility{
		NodeID:           m.NodeID,
		Profile:          profile,
		FreeCPUCores:     m.CPU.Cores,
		FreeRAMBytes:     m.Memory.AvailableBytes,
		ScratchFreeBytes: scratchFree(m.Disks, profile.PreferredDiskType),
		Disks:            m.Disks,
	}

	if !profile.Enabled {
		e.Reasons = append(e.Reasons, "processing disabled for this node")
		return e
	}

	if rec.Status != StatusOnline {
		e.Reasons = append(e.Reasons, fmt.Sprintf("node is %s, not online", rec.Status))
		return e
	}

	if profile.ReservedCPUCores > m.CPU.Cores {
		e.Reasons = append(e.Reasons, fmt.Sprintf("requests %d CPU cores, node has %d",
			profile.ReservedCPUCores, m.CPU.Cores))
	}

	if profile.ReservedRAMBytes > m.Memory.AvailableBytes {
		e.Reasons = append(e.Reasons, fmt.Sprintf("requests %s RAM, only %s free",
			telemetry.FormatBytes(profile.ReservedRAMBytes), telemetry.FormatBytes(m.Memory.AvailableBytes)))
	}

	if profile.ReservedStorageBytes > 0 {
		if e.ScratchFreeBytes == 0 {
			e.Reasons = append(e.Reasons, fmt.Sprintf("no free space on %s for scratch work",
				diskLabel(profile.PreferredDiskType)))
		} else if profile.ReservedStorageBytes > e.ScratchFreeBytes {
			e.Reasons = append(e.Reasons, fmt.Sprintf("requests %s scratch storage, only %s free on %s",
				telemetry.FormatBytes(profile.ReservedStorageBytes),
				telemetry.FormatBytes(e.ScratchFreeBytes),
				diskLabel(profile.PreferredDiskType)))
		}
	}

	e.Eligible = len(e.Reasons) == 0
	return e
}

func scratchFree(disks []telemetry.DiskStat, preferred string) uint64 {
	var best uint64
	pref := strings.TrimSpace(preferred)
	for _, d := range disks {
		if pref != "" {
			// Check if preferred matches exact disk path (e.g. "D:", "/mnt/hdd") OR disk type (e.g. "NVME", "SSD", "HDD")
			if !strings.EqualFold(d.Path, pref) && !strings.EqualFold(d.DiskType, pref) {
				continue
			}
		}
		if d.FreeBytes > best {
			best = d.FreeBytes
		}
	}
	return best
}

func diskLabel(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "any disk"
	}
	return kind
}

// EvaluateProcessing maps node records to eligibility reports using the
// supplied profiles. Nodes without a profile fall back to the enabled
// default so every online worker stays schedulable.
func EvaluateProcessing(nodes []*NodeRecord, profiles map[string]ProcessingProfile) map[string]Eligibility {
	out := make(map[string]Eligibility, len(nodes))
	for _, n := range nodes {
		profile, ok := profiles[n.Metrics.NodeID]
		if !ok {
			profile = DefaultProfile(n.Metrics.NodeID)
		}
		out[n.Metrics.NodeID] = CheckEligibility(n, profile)
	}
	return out
}
