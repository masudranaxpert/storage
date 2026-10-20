package lifecycle

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"stream/pkg/fileapi"
	"stream/pkg/storage"
)

// FileView is the pure snapshot representation of a file record and its job state.
type FileView struct {
	Key       string            `json:"key"`
	Filename  string            `json:"filename"`
	SizeBytes int64             `json:"size_bytes"`
	State     fileapi.FileState `json:"state"`
	Placement fileapi.Placement `json:"placement"`
	Offline   bool              `json:"offline,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// SkippedCandidate explains why an otherwise eligible file was not scheduled during Evaluate.
type SkippedCandidate struct {
	Key    string `json:"key"`
	EdgeID string `json:"edge_id"`
	Reason string `json:"reason"`
}

// Snapshot holds all inputs needed by the pure lifecycle planner for a single evaluation tick.
type Snapshot struct {
	Policy    FlowPolicy
	Tiers     []storage.TierStatus
	Files     []FileView
	States    map[string]FileState
	Active    map[string]bool
	Receivers map[string]bool
	Now       time.Time
}

type evalCandidate struct {
	file         FileView
	state        FileState
	score        float64
	lastHit      time.Time
	tierSince    time.Time
	lastActivity time.Time
	pinned       bool
}

// Evaluate runs spec §5.2 steps 1-4 and 6 as a pure function without I/O,
// returning planned tasks in priority order and skipped candidates with reasons.
func Evaluate(snap Snapshot) ([]Task, []SkippedCandidate) {
	now := snap.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	halfLife := snap.Policy.HalfLifeHours
	if halfLife <= 0 {
		halfLife = 24
	}
	maxTasks := snap.Policy.MaxTasksPerTick
	if maxTasks <= 0 {
		maxTasks = 20
	}

	simTiers := cloneTierStatuses(snap.Tiers)

	// Index online status of nodes from tier blocks when populated.
	nodeHasBlocks := make(map[string]bool)
	nodeOnline := make(map[string]bool)
	for _, ts := range simTiers {
		for _, bs := range ts.Blocks {
			if bs.Block.NodeID != "" {
				nodeHasBlocks[bs.Block.NodeID] = true
				if bs.Online {
					nodeOnline[bs.Block.NodeID] = true
				}
			}
		}
	}

	candidates := make([]evalCandidate, 0, len(snap.Files))
	for _, f := range snap.Files {
		if f.Key == "" || f.State != fileapi.StateCompleted {
			continue
		}
		if snap.Active != nil && snap.Active[f.Key] {
			continue
		}
		if f.Offline {
			continue
		}
		if f.Placement.NodeID != "" && !strings.HasPrefix(f.Placement.NodeID, "bucket:") && nodeHasBlocks[f.Placement.NodeID] && !nodeOnline[f.Placement.NodeID] {
			continue
		}

		var st FileState
		if snap.States != nil {
			st = snap.States[f.Key]
		}
		if st.Key == "" {
			st.Key = f.Key
		}

		lastHit := st.LastHitAt
		if lastHit.IsZero() {
			lastHit = f.CreatedAt
		}
		tierSince := st.TierSince
		if tierSince.IsZero() {
			tierSince = f.CreatedAt
		}
		lastAct := maxTime(lastHit, tierSince)

		candidates = append(candidates, evalCandidate{
			file:         f,
			state:        st,
			score:        st.Decayed(now, halfLife),
			lastHit:      lastHit,
			tierSince:    tierSince,
			lastActivity: lastAct,
			pinned:       st.IsPinned(now),
		})
	}

	planned := make(map[string]bool)
	var (
		pinTasks       []Task
		promoteTasks   []Task
		watermarkTasks []Task
		idleTasks      []Task
		expireTasks    []Task
		skipped        []SkippedCandidate
	)

	// 1. Pins: move active pinned files that are not yet on their pinned tier.
	pinCands := make([]evalCandidate, 0)
	for _, c := range candidates {
		if c.pinned && c.state.PinTierID != nil && c.file.Placement.TierID != *c.state.PinTierID {
			pinCands = append(pinCands, c)
		}
	}
	sortByHeatDesc(pinCands)

	for _, c := range pinCands {
		if planned[c.file.Key] {
			continue
		}
		targetTierID := *c.state.PinTierID
		targetTier := simTiers[targetTierID]
		if targetTier == nil {
			skipped = append(skipped, SkippedCandidate{
				Key:    c.file.Key,
				Reason: fmt.Sprintf("pinned target tier%d not found", targetTierID),
			})
			continue
		}

		kind := TaskManual
		if fromTier := simTiers[c.file.Placement.TierID]; fromTier != nil && targetTier.Lifecycle.SpeedRank < fromTier.Lifecycle.SpeedRank {
			kind = TaskPromote
		}

		reason := fmt.Sprintf("pinned to tier%d", targetTierID)
		if !c.state.PinUntil.IsZero() {
			reason = fmt.Sprintf("pinned to tier%d until %s", targetTierID, c.state.PinUntil.UTC().Format(time.RFC3339))
		}

		if bs := pickBlockInSimTier(targetTier, c.file.SizeBytes, snap.Receivers); bs != nil {
			applyProjectedMove(simTiers, c.file.Placement, targetTierID, bs, c.file.SizeBytes)
			planned[c.file.Key] = true
			pinTasks = append(pinTasks, buildMoveTask(c.file, kind, "", reason, targetTierID, bs, now))
			continue
		}

		// Attempt make-room demotes on targetTier if an enabled demote edge exists from targetTier.
		if roomTasks, bs, ok := tryMakeRoom(simTiers, snap, candidates, planned, targetTierID, c.file, c.score, true, now); ok {
			pinTasks = append(pinTasks, roomTasks...)
			applyProjectedMove(simTiers, c.file.Placement, targetTierID, bs, c.file.SizeBytes)
			planned[c.file.Key] = true
			pinTasks = append(pinTasks, buildMoveTask(c.file, kind, "", reason, targetTierID, bs, now))
			continue
		}

		skipped = append(skipped, SkippedCandidate{
			Key:    c.file.Key,
			Reason: "destination full",
		})
	}

	// 2. Promote: evaluate enabled promote edges in policy order, hottest files first, with make-room demotes.
	for _, edge := range snap.Policy.Edges {
		if !edge.Enabled || edge.Kind != EdgePromote {
			continue
		}
		targetTier := simTiers[edge.To]
		if targetTier == nil {
			continue
		}

		var hotCands []evalCandidate
		for _, c := range candidates {
			if planned[c.file.Key] || c.file.Placement.TierID != edge.From {
				continue
			}
			// If pinned to a different tier, do not move away from the pin tier.
			if c.pinned && c.state.PinTierID != nil && *c.state.PinTierID != edge.To {
				continue
			}
			if c.score >= edge.Trigger.HeatAtLeast {
				hotCands = append(hotCands, c)
			}
		}
		sortByHeatDesc(hotCands)

		for _, c := range hotCands {
			if planned[c.file.Key] {
				continue
			}
			reason := fmt.Sprintf("heat %.1f >= %.1f (edge %s)", c.score, edge.Trigger.HeatAtLeast, edge.ID)

			if bs := pickBlockInSimTier(targetTier, c.file.SizeBytes, snap.Receivers); bs != nil {
				applyProjectedMove(simTiers, c.file.Placement, edge.To, bs, c.file.SizeBytes)
				planned[c.file.Key] = true
				promoteTasks = append(promoteTasks, buildMoveTask(c.file, TaskPromote, edge.ID, reason, edge.To, bs, now))
				continue
			}

			if roomTasks, bs, ok := tryMakeRoom(simTiers, snap, candidates, planned, edge.To, c.file, c.score, false, now); ok {
				promoteTasks = append(promoteTasks, roomTasks...)
				applyProjectedMove(simTiers, c.file.Placement, edge.To, bs, c.file.SizeBytes)
				planned[c.file.Key] = true
				promoteTasks = append(promoteTasks, buildMoveTask(c.file, TaskPromote, edge.ID, reason, edge.To, bs, now))
				continue
			}

			skipped = append(skipped, SkippedCandidate{
				Key:    c.file.Key,
				EdgeID: edge.ID,
				Reason: "destination full",
			})
		}
	}

	// 3. Watermark demote: when From used% >= HighWatermark, demote coldest unpinned files until projected used% <= LowWatermark.
	// Evaluated before idle demote so watermark tasks take precedence under MaxTasksPerTick and update projected capacity first.
	for _, edge := range snap.Policy.Edges {
		if !edge.Enabled || edge.Kind != EdgeDemote || !edge.Trigger.OnWatermark {
			continue
		}
		fromTier := simTiers[edge.From]
		toTier := simTiers[edge.To]
		if fromTier == nil || toTier == nil || fromTier.Lifecycle.HighWatermark <= 0 {
			continue
		}
		if tierUsedPct(fromTier) < fromTier.Lifecycle.HighWatermark {
			continue
		}

		var wmCands []evalCandidate
		for _, c := range candidates {
			if planned[c.file.Key] || c.pinned || c.file.Placement.TierID != edge.From {
				continue
			}
			wmCands = append(wmCands, c)
		}
		sortByColdestFirst(wmCands)

		for _, c := range wmCands {
			if planned[c.file.Key] {
				continue
			}
			curPct := tierUsedPct(fromTier)
			if curPct <= fromTier.Lifecycle.LowWatermark {
				break
			}
			bs := pickBlockInSimTier(toTier, c.file.SizeBytes, snap.Receivers)
			if bs == nil {
				skipped = append(skipped, SkippedCandidate{
					Key:    c.file.Key,
					EdgeID: edge.ID,
					Reason: "destination full",
				})
				continue
			}
			reason := fmt.Sprintf("tier%d watermark %.1f%% >= %.1f%% -> %.1f%% (edge %s)",
				edge.From, curPct, fromTier.Lifecycle.HighWatermark, fromTier.Lifecycle.LowWatermark, edge.ID)
			applyProjectedMove(simTiers, c.file.Placement, edge.To, bs, c.file.SizeBytes)
			planned[c.file.Key] = true
			watermarkTasks = append(watermarkTasks, buildMoveTask(c.file, TaskDemote, edge.ID, reason, edge.To, bs, now))
		}
	}

	// 4. Idle demote: for each enabled demote edge with IdleDays > 0, demote unpinned files where
	// now - max(LastHitAt, TierSince) >= IdleDays and Decayed < HeatBelow.
	for _, edge := range snap.Policy.Edges {
		if !edge.Enabled || edge.Kind != EdgeDemote || edge.Trigger.IdleDays <= 0 {
			continue
		}
		// If OnWatermark is also set on the same edge, only run idle check when not gated or after watermark pass.
		if edge.Trigger.OnWatermark {
			fromTier := simTiers[edge.From]
			if fromTier == nil || fromTier.Lifecycle.HighWatermark <= 0 || tierUsedPct(fromTier) < fromTier.Lifecycle.HighWatermark {
				continue
			}
		}
		toTier := simTiers[edge.To]
		if toTier == nil {
			continue
		}

		minIdle := time.Duration(edge.Trigger.IdleDays) * 24 * time.Hour
		var idleCands []evalCandidate
		for _, c := range candidates {
			if planned[c.file.Key] || c.pinned || c.file.Placement.TierID != edge.From {
				continue
			}
			if now.Sub(c.lastActivity) < minIdle {
				continue
			}
			if edge.Trigger.HeatBelow > 0 && c.score >= edge.Trigger.HeatBelow {
				continue
			}
			idleCands = append(idleCands, c)
		}
		sortByColdestFirst(idleCands)

		for _, c := range idleCands {
			if planned[c.file.Key] {
				continue
			}
			bs := pickBlockInSimTier(toTier, c.file.SizeBytes, snap.Receivers)
			if bs == nil {
				skipped = append(skipped, SkippedCandidate{
					Key:    c.file.Key,
					EdgeID: edge.ID,
					Reason: "destination full",
				})
				continue
			}
			idleDaysActual := int(now.Sub(c.lastActivity).Hours() / 24)
			reason := fmt.Sprintf("idle %dd >= %dd, heat %.1f < %.1f (edge %s)",
				idleDaysActual, edge.Trigger.IdleDays, c.score, edge.Trigger.HeatBelow, edge.ID)
			applyProjectedMove(simTiers, c.file.Placement, edge.To, bs, c.file.SizeBytes)
			planned[c.file.Key] = true
			idleTasks = append(idleTasks, buildMoveTask(c.file, TaskDemote, edge.ID, reason, edge.To, bs, now))
		}
	}

	// 5. Expire: evaluate enabled expire edges when Policy.ExpireAcknowledged is true.
	if snap.Policy.ExpireAcknowledged {
		for _, edge := range snap.Policy.Edges {
			if !edge.Enabled || edge.Kind != EdgeExpire {
				continue
			}
			fromTier := simTiers[edge.From]
			if fromTier == nil {
				continue
			}

			// If OnWatermark is set: expire only while From tier >= HighWatermark.
			if edge.Trigger.OnWatermark {
				if fromTier.Lifecycle.HighWatermark <= 0 || tierUsedPct(fromTier) < fromTier.Lifecycle.HighWatermark {
					continue
				}
			}

			// Collect eligible expire candidates on From tier.
			var eligible []evalCandidate
			for _, c := range candidates {
				if planned[c.file.Key] || c.pinned {
					continue
				}
				if c.file.Placement.TierID != edge.From {
					continue
				}

				// If OnWatermark is set: expire only while no demote edge from From has room for this candidate.
				if edge.Trigger.OnWatermark {
					hasDemoteRoom := false
					for _, de := range snap.Policy.Edges {
						if de.Enabled && de.Kind == EdgeDemote && de.From == edge.From {
							if toTier := simTiers[de.To]; toTier != nil {
								if pickBlockInSimTier(toTier, c.file.SizeBytes, snap.Receivers) != nil {
									hasDemoteRoom = true
									break
								}
							}
						}
					}
					if hasDemoteRoom {
						continue
					}
				}

				// Check AgeDays and/or IdleDays triggers.
				if edge.Trigger.AgeDays <= 0 && edge.Trigger.IdleDays <= 0 {
					continue
				}
				matches := true
				if edge.Trigger.AgeDays > 0 {
					age := now.Sub(c.tierSince)
					if age < time.Duration(edge.Trigger.AgeDays)*24*time.Hour {
						matches = false
					}
				}
				if edge.Trigger.IdleDays > 0 {
					idle := now.Sub(c.lastActivity)
					if idle < time.Duration(edge.Trigger.IdleDays)*24*time.Hour {
						matches = false
					}
				}
				if !matches {
					continue
				}

				eligible = append(eligible, c)
			}

			// Sort oldest first.
			sortByOldestFirst(eligible)

			for _, c := range eligible {
				if planned[c.file.Key] {
					continue
				}

				reason := fmt.Sprintf("expired on tier%d (edge %s)", edge.From, edge.ID)
				if edge.Trigger.AgeDays > 0 && edge.Trigger.IdleDays > 0 {
					reason = fmt.Sprintf("expired on tier%d after %dd age and %dd idle (edge %s)", edge.From, edge.Trigger.AgeDays, edge.Trigger.IdleDays, edge.ID)
				} else if edge.Trigger.AgeDays > 0 {
					reason = fmt.Sprintf("expired on tier%d after %dd age (edge %s)", edge.From, edge.Trigger.AgeDays, edge.ID)
				} else if edge.Trigger.IdleDays > 0 {
					reason = fmt.Sprintf("expired on tier%d after %dd idle (edge %s)", edge.From, edge.Trigger.IdleDays, edge.ID)
				}

				task := Task{
					ID:        NewTaskID(),
					Key:       c.file.Key,
					Kind:      TaskExpire,
					EdgeID:    edge.ID,
					Reason:    reason,
					From:      c.file.Placement,
					To:        fileapi.Placement{TierID: TrashTierID, TierLabel: "trash"},
					State:     TaskQueued,
					Bytes:     c.file.SizeBytes,
					CreatedAt: now.UTC(),
					UpdatedAt: now.UTC(),
				}

				applyProjectedMove(simTiers, c.file.Placement, TrashTierID, nil, c.file.SizeBytes)
				planned[c.file.Key] = true
				expireTasks = append(expireTasks, task)

				// If OnWatermark is set, stop expiring once From tier drops to or below LowWatermark.
				if edge.Trigger.OnWatermark && fromTier.Lifecycle.LowWatermark > 0 {
					if tierUsedPct(fromTier) <= fromTier.Lifecycle.LowWatermark {
						break
					}
				}
			}
		}
	}

	// 6. Cap total tasks at MaxTasksPerTick in priority order: pins -> promote -> watermark -> idle -> expire.
	allTasks := make([]Task, 0, len(pinTasks)+len(promoteTasks)+len(watermarkTasks)+len(idleTasks)+len(expireTasks))
	allTasks = append(allTasks, pinTasks...)
	allTasks = append(allTasks, promoteTasks...)
	allTasks = append(allTasks, watermarkTasks...)
	allTasks = append(allTasks, idleTasks...)
	allTasks = append(allTasks, expireTasks...)

	if len(allTasks) > maxTasks {
		for _, overflow := range allTasks[maxTasks:] {
			skipped = append(skipped, SkippedCandidate{
				Key:    overflow.Key,
				EdgeID: overflow.EdgeID,
				Reason: "max_tasks_per_tick reached",
			})
		}
		allTasks = allTasks[:maxTasks]
	}

	return allTasks, skipped
}

func tryMakeRoom(
	simTiers map[int]*storage.TierStatus,
	snap Snapshot,
	candidates []evalCandidate,
	planned map[string]bool,
	targetTierID int,
	incoming FileView,
	incomingScore float64,
	ignoreVictimScore bool,
	now time.Time,
) ([]Task, *storage.BlockStatus, bool) {
	// Find an enabled demote edge leaving targetTierID.
	var demoteEdge *Edge
	for i := range snap.Policy.Edges {
		e := &snap.Policy.Edges[i]
		if e.Enabled && e.Kind == EdgeDemote && e.From == targetTierID {
			demoteEdge = e
			break
		}
	}
	if demoteEdge == nil {
		return nil, nil, false
	}

	// Collect unpinned, unplanned victims on targetTierID with score < incomingScore.
	var victims []evalCandidate
	for _, c := range candidates {
		if planned[c.file.Key] || c.pinned || c.file.Key == incoming.Key {
			continue
		}
		if c.file.Placement.TierID != targetTierID {
			continue
		}
		if !ignoreVictimScore && c.score >= incomingScore {
			continue
		}
		victims = append(victims, c)
	}
	if len(victims) == 0 {
		return nil, nil, false
	}
	sortByColdestFirst(victims)

	workTiers := cloneTierStatusMap(simTiers)
	workDestTier := workTiers[demoteEdge.To]
	workTargetTier := workTiers[targetTierID]
	if workDestTier == nil || workTargetTier == nil {
		return nil, nil, false
	}

	var roomTasks []Task
	var evictedKeys []string

	for _, v := range victims {
		bsDemote := pickBlockInSimTier(workDestTier, v.file.SizeBytes, snap.Receivers)
		if bsDemote == nil {
			continue
		}
		applyProjectedMove(workTiers, v.file.Placement, demoteEdge.To, bsDemote, v.file.SizeBytes)
		evictedKeys = append(evictedKeys, v.file.Key)
		reason := fmt.Sprintf("make room on tier%d for %s (heat %.1f < %.1f, edge %s)",
			targetTierID, incoming.Key, v.score, incomingScore, demoteEdge.ID)
		roomTasks = append(roomTasks, buildMoveTask(v.file, TaskDemote, demoteEdge.ID, reason, demoteEdge.To, bsDemote, now))

		if bsTarget := pickBlockInSimTier(workTargetTier, incoming.SizeBytes, snap.Receivers); bsTarget != nil {
			// Commit working tier projections and planned victim keys.
			for k, vTier := range workTiers {
				simTiers[k] = vTier
			}
			for _, k := range evictedKeys {
				planned[k] = true
			}
			return roomTasks, bsTarget, true
		}
	}

	return nil, nil, false
}

func buildMoveTask(f FileView, kind TaskKind, edgeID, reason string, toTierID int, bs *storage.BlockStatus, now time.Time) Task {
	return Task{
		ID:     NewTaskID(),
		Key:    f.Key,
		Kind:   kind,
		EdgeID: edgeID,
		Reason: reason,
		From:   f.Placement,
		To: fileapi.Placement{
			TierID:     toTierID,
			TierLabel:  fileapi.TierLabel(toTierID),
			NodeID:     bs.Block.NodeID,
			Path:       bs.Block.Path,
			PublicHost: bs.Block.PublicHost,
		},
		State:     TaskQueued,
		Bytes:     f.SizeBytes,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	}
}

func pickBlockInSimTier(tier *storage.TierStatus, sizeBytes int64, receivers map[string]bool) *storage.BlockStatus {
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	return fileapi.PickBlockInTier(tier, uint64(sizeBytes), receivers)
}

func applyProjectedMove(tiers map[int]*storage.TierStatus, from fileapi.Placement, toTierID int, toBlock *storage.BlockStatus, sizeBytes int64) {
	if sizeBytes <= 0 {
		return
	}
	delta := uint64(sizeBytes)

	if fromTier := tiers[from.TierID]; fromTier != nil {
		if fromTier.UsedBytes >= delta {
			fromTier.UsedBytes -= delta
		} else {
			fromTier.UsedBytes = 0
		}
		if fromTier.UsableBytes > fromTier.UsedBytes {
			fromTier.FreeBytes = fromTier.UsableBytes - fromTier.UsedBytes
		} else {
			fromTier.FreeBytes = 0
		}
		for i := range fromTier.Blocks {
			b := &fromTier.Blocks[i]
			if b.Block.NodeID == from.NodeID && (fileapi.CleanPlacementPath(b.Block.Path) == fileapi.CleanPlacementPath(from.Path) || len(fromTier.Blocks) == 1) {
				if b.UsedBytes >= delta {
					b.UsedBytes -= delta
				} else {
					b.UsedBytes = 0
				}
				if b.UsableBytes > b.UsedBytes {
					b.FreeBytes = b.UsableBytes - b.UsedBytes
				} else {
					b.FreeBytes = 0
				}
				if b.UsableBytes > 0 {
					b.UsedPercent = (float64(b.UsedBytes) / float64(b.UsableBytes)) * 100.0
				}
				break
			}
		}
	}

	if toTier := tiers[toTierID]; toTier != nil {
		toTier.UsedBytes += delta
		if toTier.UsableBytes > toTier.UsedBytes {
			toTier.FreeBytes = toTier.UsableBytes - toTier.UsedBytes
		} else {
			toTier.FreeBytes = 0
		}
		for i := range toTier.Blocks {
			b := &toTier.Blocks[i]
			if toBlock != nil && b.Block.NodeID == toBlock.Block.NodeID && fileapi.CleanPlacementPath(b.Block.Path) == fileapi.CleanPlacementPath(toBlock.Block.Path) {
				b.UsedBytes += delta
				if b.UsableBytes > b.UsedBytes {
					b.FreeBytes = b.UsableBytes - b.UsedBytes
				} else {
					b.FreeBytes = 0
				}
				if b.UsableBytes > 0 {
					b.UsedPercent = (float64(b.UsedBytes) / float64(b.UsableBytes)) * 100.0
				}
				break
			}
		}
	}
}

func tierUsedPct(t *storage.TierStatus) float64 {
	if t == nil || t.UsableBytes == 0 {
		return 0
	}
	return (float64(t.UsedBytes) / float64(t.UsableBytes)) * 100.0
}

func cloneTierStatuses(tiers []storage.TierStatus) map[int]*storage.TierStatus {
	out := make(map[int]*storage.TierStatus, len(tiers))
	for _, t := range tiers {
		cp := t
		cp.Mediums = append([]string(nil), t.Mediums...)
		cp.Blocks = make([]storage.BlockStatus, len(t.Blocks))
		copy(cp.Blocks, t.Blocks)
		for i := range cp.Blocks {
			if cp.Blocks[i].UsableBytes == 0 && cp.Blocks[i].TotalBytes > 0 {
				cp.Blocks[i].UsableBytes = cp.Blocks[i].TotalBytes
			} else if cp.Blocks[i].UsableBytes == 0 && cp.Blocks[i].FreeBytes+cp.Blocks[i].UsedBytes > 0 {
				cp.Blocks[i].UsableBytes = cp.Blocks[i].FreeBytes + cp.Blocks[i].UsedBytes
			}
		}
		if cp.UsableBytes == 0 {
			for _, b := range cp.Blocks {
				cp.UsableBytes += b.UsableBytes
			}
		}
		out[t.ID] = &cp
	}
	return out
}

func cloneTierStatusMap(in map[int]*storage.TierStatus) map[int]*storage.TierStatus {
	out := make(map[int]*storage.TierStatus, len(in))
	for k, t := range in {
		if t == nil {
			continue
		}
		cp := *t
		cp.Mediums = append([]string(nil), t.Mediums...)
		cp.Blocks = make([]storage.BlockStatus, len(t.Blocks))
		copy(cp.Blocks, t.Blocks)
		out[k] = &cp
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func sortByHeatDesc(cands []evalCandidate) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if !cands[i].lastHit.Equal(cands[j].lastHit) {
			return cands[i].lastHit.After(cands[j].lastHit)
		}
		return cands[i].file.Key < cands[j].file.Key
	})
}

func sortByColdestFirst(cands []evalCandidate) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		if !cands[i].lastHit.Equal(cands[j].lastHit) {
			return cands[i].lastHit.Before(cands[j].lastHit)
		}
		if !cands[i].tierSince.Equal(cands[j].tierSince) {
			return cands[i].tierSince.Before(cands[j].tierSince)
		}
		return cands[i].file.Key < cands[j].file.Key
	})
}

func sortByOldestFirst(cands []evalCandidate) {
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].tierSince.Equal(cands[j].tierSince) {
			return cands[i].tierSince.Before(cands[j].tierSince)
		}
		if !cands[i].file.CreatedAt.Equal(cands[j].file.CreatedAt) {
			return cands[i].file.CreatedAt.Before(cands[j].file.CreatedAt)
		}
		return cands[i].file.Key < cands[j].file.Key
	})
}
