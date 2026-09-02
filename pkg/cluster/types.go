package cluster

import (
	"strings"
	"time"

	"stream/pkg/telemetry"
)

// NodeStatus defines the operational health state of a node.
type NodeStatus string

const (
	StatusOnline  NodeStatus = "online"
	StatusStale   NodeStatus = "stale"
	StatusOffline NodeStatus = "offline"
)

// IsCoordinatorNode reports whether nodeID is the coordinator runner, not a
// storage/compute VPS. The coordinator never joins the resource pool.
func IsCoordinatorNode(nodeID string) bool {
	id := strings.ToLower(strings.TrimSpace(nodeID))
	if id == "" {
		return false
	}
	if id == "local-master" {
		return true
	}
	return strings.Contains(id, "(master)")
}

// IsPoolVPS reports whether telemetry belongs to a real Linux VPS worker.
// Desktop OSes (Windows/macOS) never join the storage/compute pool — those
// drives are local coordinator machines, not pooled VPS storage.
func IsPoolVPS(m telemetry.NodeMetrics) bool {
	if IsCoordinatorNode(m.NodeID) {
		return false
	}
	osName := strings.ToLower(strings.TrimSpace(m.OS))
	if osName == "" {
		osName = strings.ToLower(strings.TrimSpace(m.Platform))
	}
	switch {
	case strings.Contains(osName, "windows"),
		strings.Contains(osName, "darwin"),
		strings.Contains(osName, "macos"):
		return false
	default:
		return true
	}
}

// NodeRecord stores current telemetry and connection metadata for a cluster node.
type NodeRecord struct {
	Metrics  telemetry.NodeMetrics `json:"metrics"`
	Status   NodeStatus            `json:"status"`
	LastSeen time.Time             `json:"last_seen"`
}

// MediumSummary holds aggregated metrics for a specific storage medium (NVMe, SSD, HDD).
type MediumSummary struct {
	TotalBytes  uint64  `json:"total_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
	DriveCount  int     `json:"drive_count"`
}

// ClusterPoolSummary represents the aggregated compute and storage capacity of the cluster.
type ClusterPoolSummary struct {
	TotalNodes         int           `json:"total_nodes"`
	ActiveNodes        int           `json:"active_nodes"`
	OfflineNodes       int           `json:"offline_nodes"`
	TotalDrives        int           `json:"total_drives"`
	TotalStorageBytes  uint64        `json:"total_storage_bytes"`
	FreeStorageBytes   uint64        `json:"free_storage_bytes"`
	UsedStorageBytes   uint64        `json:"used_storage_bytes"`
	StorageUsedPercent float64       `json:"storage_used_percent"`
	TotalRAMBytes      uint64        `json:"total_ram_bytes"`
	AvailableRAMBytes  uint64        `json:"available_ram_bytes"`
	TotalCPUCores      int           `json:"total_cpu_cores"`
	AvgCPULoadPercent  float64       `json:"avg_cpu_load_percent"`

	// Storage medium tier breakdowns
	NVMe MediumSummary `json:"nvme"`
	SSD  MediumSummary `json:"ssd"`
	HDD  MediumSummary `json:"hdd"`

	Nodes []*NodeRecord `json:"nodes"`
}
