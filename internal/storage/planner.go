package storage

import (
	"fmt"
	"path/filepath"
	"strings"
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
var DefaultPolicy = PlacementPolicy{HDDTierID: IngestTierID, SSDTierID: 1, HotTierID: 0}

// PickBlockInTier selects the block inside one tier with the most usable
// headroom that is online, owned by an allowed node, and fits requiredBytes.
// Returns nil when nothing fits.
func PickBlockInTier(tier *TierStatus, requiredBytes uint64, allowedNodes map[string]bool) *BlockStatus {
	if tier == nil {
		return nil
	}
	var best *BlockStatus
	for i := range tier.Blocks {
		bs := tier.Blocks[i]
		if !bs.Online {
			continue
		}
		if allowedNodes != nil && !allowedNodes[bs.Block.NodeID] && !strings.HasPrefix(bs.Block.NodeID, "bucket:") {
			continue
		}
		if FreeOf(bs) < requiredBytes {
			continue
		}
		if best == nil || FreeOf(bs) > FreeOf(*best) {
			cp := bs
			best = &cp
		}
	}
	return best
}

// FreeOf is the usable free space on a resolved block.
func FreeOf(bs BlockStatus) uint64 {
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
