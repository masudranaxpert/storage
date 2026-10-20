package lifecycle

import (
	"strings"

	"stream/pkg/storage"
)

func resolveFastestPromoteTarget(startTierID int, policy FlowPolicy, tiers []storage.Tier) (int, float64) {
	tierByID := make(map[int]storage.Tier, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}

	visited := map[int]bool{startTierID: true}
	queue := []int{startTierID}
	bestTierID := -1
	bestRank := 0
	var maxHeat float64

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, edge := range policy.Edges {
			if !edge.Enabled || edge.Kind != EdgePromote || edge.From != curr {
				continue
			}
			if edge.Trigger.HeatAtLeast > maxHeat {
				maxHeat = edge.Trigger.HeatAtLeast
			}
			t, ok := tierByID[edge.To]
			if !ok {
				continue
			}
			if bestTierID == -1 || t.Lifecycle.SpeedRank < bestRank || (t.Lifecycle.SpeedRank == bestRank && t.ID < bestTierID) {
				bestTierID = t.ID
				bestRank = t.Lifecycle.SpeedRank
			}
			if !visited[edge.To] {
				visited[edge.To] = true
				queue = append(queue, edge.To)
			}
		}
	}

	if bestTierID != -1 {
		return bestTierID, maxHeat
	}

	// No promote edges from startTierID: pick the lowest SpeedRank tier overall.
	for _, t := range tiers {
		if bestTierID == -1 || t.Lifecycle.SpeedRank < bestRank || (t.Lifecycle.SpeedRank == bestRank && t.ID < bestTierID) {
			bestTierID = t.ID
			bestRank = t.Lifecycle.SpeedRank
		}
	}
	if bestTierID == -1 {
		bestTierID = startTierID
	}
	return bestTierID, maxHeat
}

func (e *Engine) tierList() []storage.Tier {
	if e.tiers != nil {
		return e.tiers.List()
	}
	return storage.DefaultTiers
}

func clonePolicy(p FlowPolicy) FlowPolicy {
	out := p
	if p.Ingest != nil {
		out.Ingest = append([]int(nil), p.Ingest...)
	}
	if p.Edges != nil {
		out.Edges = append([]Edge(nil), p.Edges...)
	} else {
		out.Edges = []Edge{}
	}
	return out
}

// GetPolicy returns a copy of the active FlowPolicy.
func (e *Engine) GetPolicy() FlowPolicy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return clonePolicy(e.policy)
}

// IngestOrder returns the ordered slice of tier IDs for new file placement.
func (e *Engine) IngestOrder() []int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]int(nil), e.policy.Ingest...)
}

// SavePolicy validates and persists a new FlowPolicy using optimistic concurrency.
func (e *Engine) SavePolicy(p FlowPolicy, expectedVersion int, confirm string) (FlowPolicy, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if expectedVersion != e.policy.Version {
		return FlowPolicy{}, ErrVersionConflict
	}

	if p.HalfLifeHours == 0 {
		p.HalfLifeHours = 24
	}
	if p.TickSeconds == 0 {
		p.TickSeconds = 60
	}
	if p.MaxTasksPerTick == 0 {
		p.MaxTasksPerTick = 20
	}
	for i := range p.Edges {
		if p.Edges[i].ID == "" {
			p.Edges[i].ID = NewEdgeID()
		}
	}

	tiers := e.tierList()
	if fieldErrs := Validate(p, tiers); len(fieldErrs) > 0 {
		return FlowPolicy{}, &ValidationError{Errors: fieldErrs}
	}

	prevEnabledExpire := make(map[string]bool)
	oldExpireEdges := make(map[string]Edge)
	for _, oldEdge := range e.policy.Edges {
		if oldEdge.Kind == EdgeExpire {
			oldExpireEdges[oldEdge.ID] = oldEdge
			if oldEdge.Enabled {
				prevEnabledExpire[oldEdge.ID] = true
			}
		}
	}

	hasEnabledExpire := false
	newlyEnabledExpireID := ""
	firstEnabledExpireID := ""
	tightenedExpireID := ""

	for _, edge := range p.Edges {
		if edge.Kind == EdgeExpire && edge.Enabled {
			hasEnabledExpire = true
			if firstEnabledExpireID == "" {
				firstEnabledExpireID = edge.ID
			}
			if !prevEnabledExpire[edge.ID] {
				newlyEnabledExpireID = edge.ID
			} else {
				oldEdge, ok := oldExpireEdges[edge.ID]
				if !ok {
					for _, oe := range oldExpireEdges {
						if oe.Enabled && oe.From == edge.From {
							oldEdge = oe
							ok = true
							break
						}
					}
				}
				if ok && oldEdge.Enabled {
					tightened := false
					if edge.Trigger.AgeDays > 0 && oldEdge.Trigger.AgeDays > 0 && edge.Trigger.AgeDays < oldEdge.Trigger.AgeDays {
						tightened = true
					}
					if edge.Trigger.IdleDays > 0 && oldEdge.Trigger.IdleDays > 0 && edge.Trigger.IdleDays < oldEdge.Trigger.IdleDays {
						tightened = true
					}
					if oldEdge.Trigger.AgeDays == 0 && edge.Trigger.AgeDays > 0 {
						tightened = true
					}
					if oldEdge.Trigger.IdleDays == 0 && edge.Trigger.IdleDays > 0 {
						tightened = true
					}
					if oldEdge.Trigger.OnWatermark && !edge.Trigger.OnWatermark {
						tightened = true
					}
					if tightened && tightenedExpireID == "" {
						tightenedExpireID = edge.ID
					}
				}
			}
		}
	}

	if hasEnabledExpire {
		needsConfirm := !e.policy.ExpireAcknowledged || newlyEnabledExpireID != "" || tightenedExpireID != ""
		if needsConfirm && strings.TrimSpace(confirm) != ExpireConfirmPhrase {
			edgeID := newlyEnabledExpireID
			if edgeID == "" {
				edgeID = tightenedExpireID
			}
			if edgeID == "" {
				edgeID = firstEnabledExpireID
			}
			return FlowPolicy{}, &ValidationError{
				Cause: ErrExpireConfirmRequired,
				Errors: []FieldError{
					{
						EdgeID:  edgeID,
						Field:   "expire_confirm",
						Message: ErrExpireConfirmRequired.Error(),
					},
				},
			}
		}
		p.ExpireAcknowledged = true
	} else {
		p.ExpireAcknowledged = false
	}

	p.Version = e.policy.Version + 1
	p.UpdatedAt = e.now().UTC()

	if e.store != nil {
		if err := e.store.SaveFlowPolicy(&p); err != nil {
			return FlowPolicy{}, err
		}
	}
	e.policy = clonePolicy(p)
	return clonePolicy(p), nil
}

// Store returns the underlying lifecycle store.
