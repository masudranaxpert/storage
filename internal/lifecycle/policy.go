// Package lifecycle implements automatic tier placement, heat decay, promotion,
// demotion, and opt-in expiration across storage tiers.
package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"stream/internal/storage"
)

// EdgeKind identifies the action of a directed rule between tiers.
type EdgeKind string

const (
	EdgePromote EdgeKind = "promote"
	EdgeDemote  EdgeKind = "demote"
	EdgeExpire  EdgeKind = "expire"
)

// TrashTierID is the virtual sink tier ID for expire edges.
const TrashTierID = -1

// Trigger defines the condition under which an edge fires.
type Trigger struct {
	HeatAtLeast float64 `json:"heat_at_least,omitempty"` // promote: decayed score threshold
	HeatBelow   float64 `json:"heat_below,omitempty"`    // demote hysteresis: only if score < this
	IdleDays    int     `json:"idle_days,omitempty"`     // demote/expire: no hit for N days
	AgeDays     int     `json:"age_days,omitempty"`      // expire: on this tier for N days (TierSince)
	OnWatermark bool    `json:"on_watermark,omitempty"`  // demote/expire: run only when From tier >= HighWatermark, until <= LowWatermark
}

// Edge connects a source tier to a destination tier (or TrashTierID for expire).
type Edge struct {
	ID      string   `json:"id"` // "e_" + 8 random URL-safe chars
	Kind    EdgeKind `json:"kind"`
	From    int      `json:"from"`
	To      int      `json:"to"` // TrashTierID for expire
	Trigger Trigger  `json:"trigger"`
	Enabled bool     `json:"enabled"`
}

// FlowPolicy is the single persisted document (flow:policy) defining ingest
// priority and automated lifecycle edges.
type FlowPolicy struct {
	Version            int       `json:"version"` // optimistic concurrency: PUT must send current version
	Ingest             []int     `json:"ingest"`  // tier priority for new files
	Edges              []Edge    `json:"edges"`
	HalfLifeHours      float64   `json:"half_life_hours"`     // heat decay, default 24
	TickSeconds        int       `json:"tick_seconds"`        // evaluator period, default 60
	MaxTasksPerTick    int       `json:"max_tasks_per_tick"`  // default 20
	ExpireAcknowledged bool      `json:"expire_acknowledged"` // must be true for any expire edge to run
	UpdatedAt          time.Time `json:"updated_at"`
}

// FieldError describes a single validation error on a FlowPolicy or Edge.
type FieldError struct {
	EdgeID  string `json:"edge_id,omitempty"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError wraps one or more FieldErrors returned when a policy is invalid.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
	Cause  error        `json:"-"`
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "policy validation failed"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, fe := range e.Errors {
		if fe.EdgeID != "" {
			parts = append(parts, fmt.Sprintf("%s.%s: %s", fe.EdgeID, fe.Field, fe.Message))
		} else {
			parts = append(parts, fmt.Sprintf("%s: %s", fe.Field, fe.Message))
		}
	}
	return strings.Join(parts, "; ")
}

func (e *ValidationError) Unwrap() error {
	return e.Cause
}

// NewEdgeID generates an edge identifier of the form "e_" + 8 hex characters.
func NewEdgeID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("e_%08x", time.Now().UnixNano()&0xffffffff)
	}
	return "e_" + hex.EncodeToString(b[:])
}

func effectiveSpeedRank(t storage.Tier) int {
	if t.ID != 0 && t.Lifecycle.SpeedRank == 0 && t.Lifecycle.HighWatermark == 0 && t.Lifecycle.LowWatermark == 0 {
		switch t.ID {
		case 1:
			return 1
		case 2:
			return 2
		default:
			return 10 + t.ID
		}
	}
	return t.Lifecycle.SpeedRank
}

// DefaultPolicy builds the initial policy reproducing current placement order:
// [defaultTier, 1, ...remaining tiers ordered by SpeedRank].
func DefaultPolicy(tiers []storage.Tier) FlowPolicy {
	if len(tiers) == 0 {
		tiers = storage.DefaultTiers
	}

	seen := make(map[int]bool, len(tiers))
	ingest := make([]int, 0, len(tiers))

	// 1. Default tier first (or IngestTierID=2 fallback, or first tier).
	defaultID := -1
	for _, t := range tiers {
		if t.Default {
			defaultID = t.ID
			break
		}
	}
	if defaultID < 0 {
		for _, t := range tiers {
			if t.ID == storage.IngestTierID {
				defaultID = t.ID
				break
			}
		}
	}
	if defaultID < 0 && len(tiers) > 0 {
		defaultID = tiers[0].ID
	}
	if defaultID >= 0 {
		ingest = append(ingest, defaultID)
		seen[defaultID] = true
	}

	// 2. Warm SSD tier (ID 1) next if present and not already first.
	for _, t := range tiers {
		if t.ID == 1 && !seen[1] {
			ingest = append(ingest, 1)
			seen[1] = true
			break
		}
	}

	// 3. Remaining tiers ordered by SpeedRank ascending, tie-broken by ID.
	remaining := make([]storage.Tier, 0, len(tiers))
	for _, t := range tiers {
		if !seen[t.ID] {
			remaining = append(remaining, t)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		ri, rj := effectiveSpeedRank(remaining[i]), effectiveSpeedRank(remaining[j])
		if ri != rj {
			return ri < rj
		}
		return remaining[i].ID < remaining[j].ID
	})
	for _, t := range remaining {
		ingest = append(ingest, t.ID)
	}

	return FlowPolicy{
		Version:            1,
		Ingest:             ingest,
		Edges:              []Edge{},
		HalfLifeHours:      24,
		TickSeconds:        60,
		MaxTasksPerTick:    20,
		ExpireAcknowledged: false,
		UpdatedAt:          time.Now().UTC(),
	}
}

// Validate checks a FlowPolicy against the configured tiers and returns all
// violations found (empty slice when valid).
func Validate(p FlowPolicy, tiers []storage.Tier) []FieldError {
	var errs []FieldError

	tierByID := make(map[int]storage.Tier, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}

	if len(p.Ingest) == 0 {
		errs = append(errs, FieldError{
			Field:   "ingest",
			Message: "ingest tier order must not be empty",
		})
	} else {
		for _, tid := range p.Ingest {
			if _, ok := tierByID[tid]; !ok {
				errs = append(errs, FieldError{
					Field:   "ingest",
					Message: fmt.Sprintf("unknown tier %d in ingest list", tid),
				})
			}
		}
	}

	enabledPromoteFrom := make(map[int]int)
	promoteAdj := make(map[int][]Edge)
	demoteAdj := make(map[int][]Edge)

	for _, e := range p.Edges {
		fromTier, fromOK := tierByID[e.From]
		if !fromOK {
			errs = append(errs, FieldError{
				EdgeID:  e.ID,
				Field:   "from",
				Message: fmt.Sprintf("unknown source tier %d", e.From),
			})
		}

		switch e.Kind {
		case EdgePromote:
			toTier, toOK := tierByID[e.To]
			if !toOK {
				errs = append(errs, FieldError{
					EdgeID:  e.ID,
					Field:   "to",
					Message: fmt.Sprintf("unknown target tier %d", e.To),
				})
			} else if fromOK {
				if effectiveSpeedRank(toTier) >= effectiveSpeedRank(fromTier) {
					errs = append(errs, FieldError{
						EdgeID:  e.ID,
						Field:   "to",
						Message: fmt.Sprintf("promote target tier %d (speed_rank %d) must be faster than source tier %d (speed_rank %d)", e.To, effectiveSpeedRank(toTier), e.From, effectiveSpeedRank(fromTier)),
					})
				}
			}
			if e.Enabled {
				enabledPromoteFrom[e.From]++
				if enabledPromoteFrom[e.From] > 1 {
					errs = append(errs, FieldError{
						EdgeID:  e.ID,
						Field:   "from",
						Message: fmt.Sprintf("at most one enabled promote edge allowed from tier %d", e.From),
					})
				}
			}
			if fromOK && toOK {
				promoteAdj[e.From] = append(promoteAdj[e.From], e)
			}

		case EdgeDemote:
			toTier, toOK := tierByID[e.To]
			if !toOK {
				errs = append(errs, FieldError{
					EdgeID:  e.ID,
					Field:   "to",
					Message: fmt.Sprintf("unknown target tier %d", e.To),
				})
			} else if fromOK {
				if effectiveSpeedRank(toTier) <= effectiveSpeedRank(fromTier) {
					errs = append(errs, FieldError{
						EdgeID:  e.ID,
						Field:   "to",
						Message: fmt.Sprintf("demote target tier %d (speed_rank %d) must be slower than source tier %d (speed_rank %d)", e.To, effectiveSpeedRank(toTier), e.From, effectiveSpeedRank(fromTier)),
					})
				}
			}
			if e.Trigger.IdleDays <= 0 && !e.Trigger.OnWatermark {
				errs = append(errs, FieldError{
					EdgeID:  e.ID,
					Field:   "trigger",
					Message: "demote edge requires idle_days > 0 or on_watermark",
				})
			}
			if fromOK && toOK {
				demoteAdj[e.From] = append(demoteAdj[e.From], e)
			}

		case EdgeExpire:
			if e.To != TrashTierID {
				errs = append(errs, FieldError{
					EdgeID:  e.ID,
					Field:   "to",
					Message: fmt.Sprintf("expire edge target must be TrashTierID (%d)", TrashTierID),
				})
			}
			if e.Trigger.AgeDays <= 0 && e.Trigger.IdleDays <= 0 {
				errs = append(errs, FieldError{
					EdgeID:  e.ID,
					Field:   "trigger",
					Message: "expire edge requires age_days > 0 or idle_days > 0",
				})
			}

		default:
			errs = append(errs, FieldError{
				EdgeID:  e.ID,
				Field:   "kind",
				Message: fmt.Sprintf("unknown edge kind %q", e.Kind),
			})
		}
	}

	// Detect cycles within promote edges and within demote edges independently.
	errs = append(errs, detectEdgeCycles(promoteAdj, EdgePromote)...)
	errs = append(errs, detectEdgeCycles(demoteAdj, EdgeDemote)...)

	// Hysteresis check: promote HeatAtLeast must be > paired demote HeatBelow.
	for _, pe := range p.Edges {
		if pe.Kind != EdgePromote {
			continue
		}
		for _, de := range p.Edges {
			if de.Kind != EdgeDemote {
				continue
			}
			paired := (de.From == pe.To && de.To == pe.From) || (de.From == pe.To && de.Trigger.HeatBelow > 0)
			if !paired {
				continue
			}
			if pe.Trigger.HeatAtLeast <= de.Trigger.HeatBelow {
				errs = append(errs, FieldError{
					EdgeID:  pe.ID,
					Field:   "trigger.heat_at_least",
					Message: fmt.Sprintf("promote heat_at_least (%.2f) must be greater than paired demote heat_below (%.2f)", pe.Trigger.HeatAtLeast, de.Trigger.HeatBelow),
				})
			}
		}
	}

	return errs
}

func detectEdgeCycles(adj map[int][]Edge, kind EdgeKind) []FieldError {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[int]int)
	var errs []FieldError

	nodes := make([]int, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)

	var dfs func(u int)
	dfs = func(u int) {
		state[u] = visiting
		for _, edge := range adj[u] {
			v := edge.To
			if state[v] == visiting {
				errs = append(errs, FieldError{
					EdgeID:  edge.ID,
					Field:   "to",
					Message: fmt.Sprintf("cycle detected in %s edges involving tier %d -> %d", kind, u, v),
				})
				continue
			}
			if state[v] == unvisited {
				dfs(v)
			}
		}
		state[u] = visited
	}

	for _, n := range nodes {
		if state[n] == unvisited {
			dfs(n)
		}
	}
	return errs
}
