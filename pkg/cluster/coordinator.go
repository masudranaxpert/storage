package cluster

import (
	"sort"
	"strings"
	"sync"
	"time"

	"stream/pkg/telemetry"
)

// DataStore interface allows persistence layer decoupling (BadgerDB).
type DataStore interface {
	SaveNode(record *NodeRecord) error
	GetNode(nodeID string) (*NodeRecord, error)
	GetAllNodes() ([]*NodeRecord, error)
	// DeleteNodeRecord drops only the telemetry record; the admin config
	// (enabled flag, quotas) must survive decommission so ghost agents
	// cannot re-register.
	DeleteNodeRecord(nodeID string) error
}

// Coordinator manages cluster membership, node heartbeats, and resource pooling.
type Coordinator struct {
	mu               sync.RWMutex
	nodes            map[string]*NodeRecord
	staleThreshold   time.Duration
	offlineThreshold time.Duration
	store            DataStore
}

// NewCoordinator initializes a cluster coordinator instance with optional persistence store.
func NewCoordinator(staleThreshold, offlineThreshold time.Duration, store DataStore) *Coordinator {
	if staleThreshold <= 0 {
		staleThreshold = 15 * time.Second
	}
	if offlineThreshold <= 0 {
		offlineThreshold = 30 * time.Second
	}

	c := &Coordinator{
		nodes:            make(map[string]*NodeRecord),
		staleThreshold:   staleThreshold,
		offlineThreshold: offlineThreshold,
		store:            store,
	}

	// Restore persisted nodes as offline until a live heartbeat arrives.
	// Coordinator/master and desktop OS records are purged — only Linux VPS
	// agents belong in the storage/compute pool.
	if store != nil {
		if persisted, err := store.GetAllNodes(); err == nil {
			for _, rec := range persisted {
				if !IsPoolVPS(rec.Metrics) {
					_ = store.DeleteNodeRecord(rec.Metrics.NodeID)
					continue
				}
				rec.Status = StatusOffline // Mark offline until a live heartbeat arrives
				c.nodes[rec.Metrics.NodeID] = rec
			}
		}
	}

	return c
}

// RegisterHeartbeat records incoming telemetry from an agent, marks it online, and saves to BadgerDB.
// Non-VPS agents (coordinator name, Windows/macOS desktops) are rejected (nil).
func (c *Coordinator) RegisterHeartbeat(metrics telemetry.NodeMetrics) *NodeRecord {
	if !IsPoolVPS(metrics) {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record, exists := c.nodes[metrics.NodeID]
	if !exists {
		record = &NodeRecord{}
		c.nodes[metrics.NodeID] = record
	}

	record.Metrics = metrics
	record.Status = StatusOnline
	record.LastSeen = time.Now().UTC()

	if c.store != nil {
		_ = c.store.SaveNode(record)
	}

	return record
}

// sortNodes guarantees deterministic ordering so node cards never jitter or change positions randomly.
func sortNodes(list []*NodeRecord) {
	sort.Slice(list, func(i, j int) bool {
		return list[i].Metrics.NodeID < list[j].Metrics.NodeID
	})
}

// GetNodes returns snapshots of all pool VPS nodes in deterministic sorted order.
// The coordinator runner is never included.
func (c *Coordinator) GetNodes() []*NodeRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()

	list := make([]*NodeRecord, 0, len(c.nodes))
	for _, n := range c.nodes {
		if !IsPoolVPS(n.Metrics) {
			continue
		}
		copied := *n
		list = append(list, &copied)
	}
	sortNodes(list)
	return list
}

// RemoveNode unregisters a node from cluster memory and drops its persisted
// telemetry record. The persisted admin config is intentionally kept so a
// removed (decommissioned) node can never re-register with stale heartbeats.
func (c *Coordinator) RemoveNode(nodeID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, exists := c.nodes[nodeID]
	delete(c.nodes, nodeID)
	if c.store != nil {
		_ = c.store.DeleteNodeRecord(nodeID)
	}
	return exists
}

// GetPoolSummary aggregates resources from active VPS nodes in the cluster.
// Coordinator/master is excluded from counts and the node list.
func (c *Coordinator) GetPoolSummary() *ClusterPoolSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	summary := &ClusterPoolSummary{
		Nodes: make([]*NodeRecord, 0, len(c.nodes)),
	}

	var totalCPULoad float64

	for _, node := range c.nodes {
		if !IsPoolVPS(node.Metrics) {
			continue
		}
		copied := *node
		summary.Nodes = append(summary.Nodes, &copied)
		summary.TotalNodes++

		if node.Status == StatusOffline {
			summary.OfflineNodes++
			continue
		}

		summary.ActiveNodes++
		summary.TotalCPUCores += node.Metrics.CPU.Cores
		totalCPULoad += node.Metrics.CPU.UsedPercent
		summary.TotalRAMBytes += node.Metrics.Memory.TotalBytes
		summary.AvailableRAMBytes += node.Metrics.Memory.AvailableBytes

		for _, d := range node.Metrics.Disks {
			summary.TotalDrives++
			summary.TotalStorageBytes += d.TotalBytes
			summary.FreeStorageBytes += d.FreeBytes
			summary.UsedStorageBytes += d.UsedBytes

			switch strings.ToUpper(d.DiskType) {
			case "NVME":
				summary.NVMe.TotalBytes += d.TotalBytes
				summary.NVMe.FreeBytes += d.FreeBytes
				summary.NVMe.UsedBytes += d.UsedBytes
				summary.NVMe.DriveCount++
			case "HDD":
				summary.HDD.TotalBytes += d.TotalBytes
				summary.HDD.FreeBytes += d.FreeBytes
				summary.HDD.UsedBytes += d.UsedBytes
				summary.HDD.DriveCount++
			default:
				// SSD or other fast flash storage
				summary.SSD.TotalBytes += d.TotalBytes
				summary.SSD.FreeBytes += d.FreeBytes
				summary.SSD.UsedBytes += d.UsedBytes
				summary.SSD.DriveCount++
			}
		}
	}

	if summary.ActiveNodes > 0 {
		summary.AvgCPULoadPercent = totalCPULoad / float64(summary.ActiveNodes)
	}

	if summary.TotalStorageBytes > 0 {
		summary.StorageUsedPercent = float64(summary.UsedStorageBytes) / float64(summary.TotalStorageBytes) * 100.0
	}
	if summary.NVMe.TotalBytes > 0 {
		summary.NVMe.UsedPercent = float64(summary.NVMe.UsedBytes) / float64(summary.NVMe.TotalBytes) * 100.0
	}
	if summary.SSD.TotalBytes > 0 {
		summary.SSD.UsedPercent = float64(summary.SSD.UsedBytes) / float64(summary.SSD.TotalBytes) * 100.0
	}
	if summary.HDD.TotalBytes > 0 {
		summary.HDD.UsedPercent = float64(summary.HDD.UsedBytes) / float64(summary.HDD.TotalBytes) * 100.0
	}

	sortNodes(summary.Nodes)
	return summary
}

// StartReaper periodically audits node freshness and flags stale/offline workers.
func (c *Coordinator) StartReaper(interval time.Duration, stopCh <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.auditNodes()
		case <-stopCh:
			return
		}
	}
}

// auditNodes updates health states based on time elapsed since last heartbeat.
func (c *Coordinator) auditNodes() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now().UTC()
	for _, node := range c.nodes {
		elapsed := now.Sub(node.LastSeen)
		if elapsed >= c.offlineThreshold {
			node.Status = StatusOffline
		} else if elapsed >= c.staleThreshold {
			node.Status = StatusStale
		} else {
			node.Status = StatusOnline
		}
	}
}

