package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"stream/pkg/cache"
	"stream/pkg/cluster"
	"stream/pkg/fileapi"
	"stream/pkg/storage"
)

// ExpireConfirmPhrase is the exact confirmation phrase required to enable an expire edge.
const ExpireConfirmPhrase = "DELETE FILES"

var (
	// ErrVersionConflict indicates that the submitted FlowPolicy version does not match the stored version.
	ErrVersionConflict = errors.New("policy version conflict")
	// ErrExpireConfirmRequired indicates that an expire edge was enabled without the confirmation phrase.
	ErrExpireConfirmRequired = errors.New("confirm phrase \"DELETE FILES\" required to enable expire rule")
	// ErrNoTierFits indicates that no tier in the ingest order can accommodate the simulated file size.
	ErrNoTierFits = errors.New("insufficient storage: no ingest tier fits requested size")
)

// SimulateResult describes where a new file of a given size would land.
type SimulateResult struct {
	TierID int    `json:"tier_id"`
	NodeID string `json:"node_id"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// TierFlowStats summarizes file counts and hot file counts for one tier.
type TierFlowStats struct {
	FileCount int `json:"file_count"`
	HotCount  int `json:"hot_count"`
}

// PreviewResult contains the non-mutating output of Evaluate for GET /api/flows/preview.
type PreviewResult struct {
	Tasks   []Task             `json:"tasks"`
	Skipped []SkippedCandidate `json:"skipped"`
}

// HotFile represents a file with elevated heat score or explicit pin.
type HotFile struct {
	Key           string     `json:"key"`
	Filename      string     `json:"filename"`
	Score         float64    `json:"score"`
	TotalHits     int64      `json:"total_hits"`
	ActiveViewers int        `json:"active_viewers"`
	TierID        int        `json:"tier_id"`
	NodeID        string     `json:"node_id"`
	Path          string     `json:"path"`
	PinnedTier    *int       `json:"pinned_tier,omitempty"`
	PinUntil      *time.Time `json:"pin_until,omitempty"`
	LastHitAt     time.Time  `json:"last_hit_at"`
	SizeBytes     int64      `json:"size_bytes"`
}

// NextAction describes the next scheduled or projected lifecycle transition for a single file.
type NextAction struct {
	Action string `json:"action"`
	EdgeID string `json:"edge_id,omitempty"`
	ETA    string `json:"eta,omitempty"`
	Reason string `json:"reason"`
}

// PlacementView is the response payload for GET /api/v1/files/{key}/placement.
type PlacementView struct {
	Placement  fileapi.Placement `json:"placement"`
	State      FileState         `json:"state"`
	ActiveTask *Task             `json:"active_task"`
	Next       NextAction        `json:"next"`
}

// DemandRequest is the JSON request body for POST /api/v1/files/{key}/demand.
type DemandRequest struct {
	Level    string  `json:"level"`
	TierID   *int    `json:"tier_id,omitempty"`
	TTLHours float64 `json:"ttl_hours,omitempty"`
}

// DemandResponse is the JSON response body for POST /api/v1/files/{key}/demand.
type DemandResponse struct {
	Key         string     `json:"key"`
	PinnedTier  *int       `json:"pinned_tier"`
	PinUntil    *time.Time `json:"pin_until,omitempty"`
	CurrentTier int        `json:"current_tier"`
	TaskID      string     `json:"task_id,omitempty"`
}

// Option configures an Engine instance.
func New(store Store, tiers *storage.Manager, coord *cluster.Coordinator, files *fileapi.Service, opts ...Option) *Engine {
	e := &Engine{
		store:  store,
		tiers:  tiers,
		coord:  coord,
		files:  files,
		usage:  func() map[string]uint64 { return map[string]uint64{} },
		now:    time.Now,
		kickCh: make(chan struct{}, 1),
	}
	if js, ok := store.(fileapi.JobStore); ok {
		e.jobStore = js
	}
	for _, opt := range opts {
		opt(e)
	}

	e.hits = NewHitRecorder(store, 10*time.Second)
	e.viewers = NewActiveViewerTracker(60 * time.Second)

	exOpts := []ExecutorOption{
		WithExecutorUsageFunc(e.usage),
		WithExecutorClock(e.now),
		WithExecutorSecret(e.secret),
	}
	exOpts = append(exOpts, e.executorOpts...)
	e.executor = NewExecutor(store, e.jobStore, tiers, coord, exOpts...)

	var loaded *FlowPolicy
	if store != nil {
		p, err := store.GetFlowPolicy()
		if err != nil {
			log.Printf("[Lifecycle] load flow policy failed: %v", err)
		} else {
			loaded = p
		}
	}

	if loaded != nil {
		e.policy = clonePolicy(*loaded)
	} else {
		def := DefaultPolicy(e.tierList())
		def.UpdatedAt = e.now().UTC()
		e.policy = def
		if store != nil {
			if err := store.SaveFlowPolicy(&def); err != nil {
				log.Printf("[Lifecycle] persist default flow policy failed: %v", err)
			}
		}
	}

	return e
}

// Executor returns the underlying relocation task executor.
func (e *Engine) Executor() *Executor {
	return e.executor
}

// Hits returns the buffered HitRecorder.
func (e *Engine) Hits() *HitRecorder {
	return e.hits
}

// RecordHit increments the in-memory play counter for key.
func (e *Engine) RecordHit(key string) {
	if e.hits != nil {
		e.hits.RecordHit(key)
	}
}

// Viewers returns the active viewer tracker.
func (e *Engine) Viewers() *ActiveViewerTracker {
	return e.viewers
}

// RecordViewerHeartbeat registers a live viewer session for key.
func (e *Engine) RecordViewerHeartbeat(key, sessionID string) int {
	if e.viewers != nil {
		return e.viewers.Heartbeat(key, sessionID)
	}
	return 0
}

// FlushHits flushes any buffered play counts into Store using the current clock and half-life.
func (e *Engine) FlushHits() error {
	if e.hits == nil {
		return nil
	}
	return e.hits.Flush(e.now().UTC(), e.GetPolicy().HalfLifeHours)
}

// Kick triggers an immediate asynchronous evaluation tick.
func (e *Engine) Kick() {
	select {
	case e.kickCh <- struct{}{}:
	default:
	}
}

// OnFileDeleted cleans up any active lifecycle relocation task and deletes
// the file's lifecycle state record when a file is deleted from the cluster.
func (e *Engine) OnFileDeleted(key string) {
	if e == nil {
		return
	}
	if e.store != nil {
		if activeID, err := e.store.GetActiveLifecycleTask(key); err == nil && activeID != "" {
			if e.executor != nil {
				_, _ = e.executor.Cancel(activeID)
			}
			_ = e.store.DeleteActiveLifecycleTask(key)
		}
		_ = e.store.DeleteLifecycleState(key)
	}
	if e.hits != nil {
		e.hits.mu.Lock()
		delete(e.hits.counts, key)
		e.hits.mu.Unlock()
	}
}

// Run starts the hit flusher, evaluator ticker loop, and relocation executor until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if e.executor != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.executor.Run(ctx)
		}()
	}
	if e.hits != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.hits.Run(ctx, e.now, func() float64 { return e.GetPolicy().HalfLifeHours })
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		e.runEvalLoop(ctx)
	}()

	wg.Wait()
}

func (e *Engine) runEvalLoop(ctx context.Context) {
	tickDur := func() time.Duration {
		sec := e.GetPolicy().TickSeconds
		if sec <= 0 {
			sec = 60
		}
		return time.Duration(sec) * time.Second
	}

	currentDur := tickDur()
	ticker := time.NewTicker(currentDur)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.kickCh:
			e.RunTick()
			if nextDur := tickDur(); nextDur != currentDur {
				currentDur = nextDur
				ticker.Reset(currentDur)
			}
		case <-ticker.C:
			e.RunTick()
			if nextDur := tickDur(); nextDur != currentDur {
				currentDur = nextDur
				ticker.Reset(currentDur)
			}
		}
	}
}

// RunTick flushes pending hits, clears expired pins, evaluates the active policy, and enqueues tasks.
func (e *Engine) RunTick() ([]Task, []SkippedCandidate) {
	_ = e.FlushHits()
	now := e.now().UTC()
	e.clearExpiredPins(now)

	snap := e.buildSnapshot(now)
	tasks, skipped := Evaluate(snap)
	if e.executor != nil {
		for i := range tasks {
			t := tasks[i]
			if enq, err := e.executor.Enqueue(&t); err == nil && enq != nil {
				tasks[i] = *enq
			}
		}
	}
	return tasks, skipped
}

// Preview runs Evaluate against a live snapshot without persisting or enqueueing tasks.
func (e *Engine) Preview() PreviewResult {
	return e.PreviewPolicy(e.GetPolicy())
}

// PreviewPolicy runs Evaluate against a live snapshot using a provided draft policy.
func (e *Engine) PreviewPolicy(p FlowPolicy) PreviewResult {
	_ = e.FlushHits()
	now := e.now().UTC()
	snap := e.buildSnapshot(now)
	snap.Policy = p
	tasks, skipped := Evaluate(snap)
	if tasks == nil {
		tasks = []Task{}
	}
	if skipped == nil {
		skipped = []SkippedCandidate{}
	}
	return PreviewResult{
		Tasks:   tasks,
		Skipped: skipped,
	}
}

// ListHotFiles returns up to limit files with elevated heat scores or active pins.
func (e *Engine) ListHotFiles(limit int) ([]HotFile, error) {
	if limit <= 0 {
		limit = 50
	}
	_ = e.FlushHits()
	now := e.now().UTC()
	policy := e.GetPolicy()

	if e.store == nil {
		return []HotFile{}, nil
	}
	states, err := e.store.ListLifecycleStates()
	if err != nil {
		return nil, err
	}

	var jobs []*fileapi.FileJob
	var recs []*fileapi.FileRecord
	if e.jobStore != nil {
		jobs, _ = e.jobStore.ListFileJobs()
		recs, _ = e.jobStore.ListFileRecords()
	}

	jobByKey := make(map[string]*fileapi.FileJob, len(jobs))
	for _, j := range jobs {
		if j != nil {
			jobByKey[j.Key] = j
		}
	}
	recByKey := make(map[string]*fileapi.FileRecord, len(recs))
	for _, r := range recs {
		if r != nil {
			recByKey[r.Key] = r
		}
	}

	hot := make([]HotFile, 0, len(states))
	for _, st := range states {
		if st == nil {
			continue
		}
		job := jobByKey[st.Key]
		if job == nil || job.State != fileapi.StateCompleted {
			continue
		}
		score := st.Decayed(now, policy.HalfLifeHours)
		activeCount := 0
		if e.viewers != nil {
			activeCount = e.viewers.Count(st.Key)
		}
		if score <= 0.05 && st.PinTierID == nil && activeCount == 0 {
			continue
		}
		rec := recByKey[st.Key]
		hf := HotFile{
			Key:           st.Key,
			Score:         score,
			TotalHits:     st.TotalHits,
			ActiveViewers: activeCount,
			TierID:        job.Placement.TierID,
			NodeID:        job.Placement.NodeID,
			Path:          job.Placement.Path,
			PinnedTier:    st.PinTierID,
			LastHitAt:     st.LastHitAt,
		}
		if !st.PinUntil.IsZero() {
			hf.PinUntil = &st.PinUntil
		}
		if rec != nil {
			hf.Filename = rec.Filename
			hf.SizeBytes = rec.SizeBytes
		}
		hot = append(hot, hf)
	}

	if e.viewers != nil {
		seenKeys := make(map[string]bool, len(hot))
		for _, h := range hot {
			seenKeys[h.Key] = true
		}
		for key, count := range e.viewers.AllCounts() {
			if !seenKeys[key] && count > 0 {
				job := jobByKey[key]
				if job != nil && job.State == fileapi.StateCompleted {
					rec := recByKey[key]
					hf := HotFile{
						Key:           key,
						ActiveViewers: count,
						TierID:        job.Placement.TierID,
						NodeID:        job.Placement.NodeID,
						Path:          job.Placement.Path,
					}
					if rec != nil {
						hf.Filename = rec.Filename
						hf.SizeBytes = rec.SizeBytes
					}
					hot = append(hot, hf)
				}
			}
		}
	}

	sort.Slice(hot, func(i, j int) bool {
		return hot[i].Score > hot[j].Score
	})

	if len(hot) > limit {
		hot = hot[:limit]
	}
	return hot, nil
}

func (e *Engine) clearExpiredPins(now time.Time) {
	if e.store == nil {
		return
	}
	states, err := e.store.ListLifecycleStates()
	if err != nil {
		return
	}
	for _, st := range states {
		if st == nil || st.PinTierID == nil {
			continue
		}
		if !st.PinUntil.IsZero() && !now.Before(st.PinUntil) {
			st.PinTierID = nil
			st.PinUntil = time.Time{}
			_ = e.store.SaveLifecycleState(st)
		}
	}
}

func (e *Engine) buildSnapshot(now time.Time) Snapshot {
	policy := e.GetPolicy()

	var nodes []*cluster.NodeRecord
	if e.coord != nil {
		nodes = e.coord.GetNodes()
	}

	var tiers []storage.TierStatus
	if e.tiers != nil {
		tiers = e.tiers.Resolve(nodes, e.usage())
	}

	nodeOnline := make(map[string]bool, len(nodes))
	hasNodeStatus := false
	for _, n := range nodes {
		if n.Status != "" {
			hasNodeStatus = true
		}
		if n.Status == cluster.StatusOnline {
			nodeOnline[n.Metrics.NodeID] = true
		}
	}

	var receivers map[string]bool
	if hasNodeStatus {
		receivers = fileapi.ReceiverCandidates(nodes)
	}

	recByKey := make(map[string]*fileapi.FileRecord)
	if e.jobStore != nil {
		if recs, err := e.jobStore.ListFileRecords(); err == nil {
			for _, r := range recs {
				if r != nil {
					recByKey[r.Key] = r
				}
			}
		}
	}

	var files []FileView
	if e.jobStore != nil {
		if jobs, err := e.jobStore.ListFileJobs(); err == nil {
			for _, j := range jobs {
				if j == nil {
					continue
				}
				rec := recByKey[j.Key]
				sizeBytes := j.TotalBytes
				var filename string
				createdAt := j.CreatedAt
				if rec != nil {
					if rec.SizeBytes > 0 {
						sizeBytes = rec.SizeBytes
					}
					filename = rec.Filename
					if !rec.CreatedAt.IsZero() {
						createdAt = rec.CreatedAt
					}
				}
				offline := false
				if hasNodeStatus && j.Placement.NodeID != "" && !strings.HasPrefix(j.Placement.NodeID, "bucket:") && !nodeOnline[j.Placement.NodeID] {
					offline = true
				}
				files = append(files, FileView{
					Key:       j.Key,
					Filename:  filename,
					SizeBytes: sizeBytes,
					State:     j.State,
					Placement: j.Placement,
					Offline:   offline,
					CreatedAt: createdAt,
				})
			}
		}
	}

	states := make(map[string]FileState)
	if e.store != nil {
		if list, err := e.store.ListLifecycleStates(); err == nil {
			for _, st := range list {
				if st != nil {
					states[st.Key] = *st
				}
			}
		}
	}

	active := make(map[string]bool)
	if e.store != nil {
		if actMap, err := e.store.ListActiveLifecycleTasks(); err == nil {
			for k, taskID := range actMap {
				if taskID == "" {
					continue
				}
				if tk, err := e.store.GetLifecycleTask(taskID); err == nil && tk != nil && !tk.State.IsTerminal() {
					active[k] = true
				}
			}
		}
	}

	return Snapshot{
		Policy:    policy,
		Tiers:     tiers,
		Files:     files,
		States:    states,
		Active:    active,
		Receivers: receivers,
		Now:       now,
	}
}

// FilePlacement builds the placement, decayed heat state, active task, and next projected action for a single file.
func (e *Engine) FilePlacement(key string) (*PlacementView, error) {
	if e.jobStore == nil {
		return nil, ErrFileNotFound
	}
	_ = e.FlushHits()

	job, err := e.jobStore.GetFileJob(key)
	if err != nil || job == nil {
		return nil, ErrFileNotFound
	}
	rec, _ := e.jobStore.GetFileRecord(key)
	now := e.now().UTC()
	policy := e.GetPolicy()

	var st FileState
	if e.store != nil {
		if loaded, _ := e.store.GetLifecycleState(key); loaded != nil {
			st = *loaded
		}
	}
	if st.Key == "" {
		st.Key = key
	}
	if st.TierSince.IsZero() {
		if rec != nil && !rec.CreatedAt.IsZero() {
			st.TierSince = rec.CreatedAt
		} else {
			st.TierSince = job.CreatedAt
		}
	}
	if st.LastHitAt.IsZero() && rec != nil {
		st.LastHitAt = rec.CreatedAt
	}
	if st.History == nil {
		st.History = []Move{}
	}
	st.Score = st.Decayed(now, policy.HalfLifeHours)
	st.ScoreAt = now

	var activeTask *Task
	if e.store != nil {
		if activeID, _ := e.store.GetActiveLifecycleTask(key); activeID != "" {
			if tk, _ := e.store.GetLifecycleTask(activeID); tk != nil && !tk.State.IsTerminal() {
				activeTask = tk
			}
		}
	}

	next := e.computeNextAction(job, rec, st, activeTask, now)
	return &PlacementView{
		Placement:  job.Placement,
		State:      st,
		ActiveTask: activeTask,
		Next:       next,
	}, nil
}

func (e *Engine) computeNextAction(job *fileapi.FileJob, rec *fileapi.FileRecord, st FileState, activeTask *Task, now time.Time) NextAction {
	if activeTask != nil {
		return NextAction{
			Action: string(activeTask.Kind),
			EdgeID: activeTask.EdgeID,
			ETA:    "in_progress",
			Reason: activeTask.Reason,
		}
	}
	if job.State != fileapi.StateCompleted {
		return NextAction{
			Action: "none",
			Reason: fmt.Sprintf("file is %s", job.State),
		}
	}

	// Run Evaluate scoped to this file against current cluster capacity.
	snap := e.buildSnapshot(now)
	var singleFile []FileView
	for _, f := range snap.Files {
		if f.Key == job.Key {
			singleFile = append(singleFile, f)
			break
		}
	}
	if len(singleFile) == 0 {
		createdAt := job.CreatedAt
		sizeBytes := job.TotalBytes
		if rec != nil {
			if !rec.CreatedAt.IsZero() {
				createdAt = rec.CreatedAt
			}
			if rec.SizeBytes > 0 {
				sizeBytes = rec.SizeBytes
			}
		}
		singleFile = []FileView{{
			Key:       job.Key,
			SizeBytes: sizeBytes,
			State:     job.State,
			Placement: job.Placement,
			CreatedAt: createdAt,
		}}
	}
	singleSnap := snap
	singleSnap.Files = singleFile
	tasks, skipped := Evaluate(singleSnap)
	if len(tasks) > 0 {
		t := tasks[0]
		return NextAction{
			Action: string(t.Kind),
			EdgeID: t.EdgeID,
			ETA:    "next_tick",
			Reason: t.Reason,
		}
	}
	if len(skipped) > 0 {
		sk := skipped[0]
		return NextAction{
			Action: "skipped",
			EdgeID: sk.EdgeID,
			Reason: sk.Reason,
		}
	}

	if st.IsPinned(now) && st.PinTierID != nil {
		eta := ""
		if !st.PinUntil.IsZero() {
			eta = st.PinUntil.UTC().Format(time.RFC3339)
		}
		return NextAction{
			Action: "pinned",
			ETA:    eta,
			Reason: fmt.Sprintf("pinned to tier%d", *st.PinTierID),
		}
	}

	// Project upcoming idle demote ETA if an idle demote edge leaves the current tier.
	for _, edge := range snap.Policy.Edges {
		if edge.Enabled && edge.Kind == EdgeDemote && edge.From == job.Placement.TierID && edge.Trigger.IdleDays > 0 {
			lastAct := maxTime(st.LastHitAt, st.TierSince)
			dueAt := lastAct.Add(time.Duration(edge.Trigger.IdleDays) * 24 * time.Hour)
			return NextAction{
				Action: string(EdgeDemote),
				EdgeID: edge.ID,
				ETA:    dueAt.UTC().Format(time.RFC3339),
				Reason: fmt.Sprintf("idle demote to tier%d after %dd", edge.To, edge.Trigger.IdleDays),
			}
		}
	}

	return NextAction{
		Action: "none",
		Reason: fmt.Sprintf("ingest/resident on tier%d", job.Placement.TierID),
	}
}

// SetDemand handles POST /api/v1/files/{key}/demand per spec §5.4.
func (e *Engine) SetDemand(key string, req DemandRequest) (*DemandResponse, error) {
	if e.jobStore == nil {
		return nil, ErrFileNotFound
	}
	_ = e.FlushHits()

	job, err := e.jobStore.GetFileJob(key)
	if err != nil || job == nil {
		return nil, ErrFileNotFound
	}
	if job.State != fileapi.StateCompleted {
		return nil, ErrFileNotCompleted
	}

	level := strings.ToLower(strings.TrimSpace(req.Level))
	if level != "high" && level != "normal" {
		return nil, &ValidationError{
			Errors: []FieldError{{Field: "level", Message: "level must be 'high' or 'normal'"}},
		}
	}

	now := e.now().UTC()
	var st *FileState
	if e.store != nil {
		st, _ = e.store.GetLifecycleState(key)
	}
	if st == nil {
		st = &FileState{
			Key:       key,
			TierSince: now,
		}
	}

	if level == "normal" {
		st.PinTierID = nil
		st.PinUntil = time.Time{}
		if e.store != nil {
			if err := e.store.SaveLifecycleState(st); err != nil {
				return nil, err
			}
		}
		return &DemandResponse{
			Key:         key,
			PinnedTier:  nil,
			CurrentTier: job.Placement.TierID,
		}, nil
	}

	if req.TTLHours < 0 || req.TTLHours > 720 {
		return nil, &ValidationError{
			Errors: []FieldError{{Field: "ttl_hours", Message: "ttl_hours must be between 1 and 720"}},
		}
	}
	ttlHours := req.TTLHours
	if ttlHours == 0 {
		ttlHours = 24
	}

	tiers := e.tierList()
	tierByID := make(map[int]storage.Tier, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}

	policy := e.GetPolicy()
	var targetTierID int
	var boostHeat float64

	if req.TierID != nil {
		if _, ok := tierByID[*req.TierID]; !ok {
			return nil, ErrTierNotFound
		}
		targetTierID = *req.TierID
	} else {
		targetTierID, boostHeat = resolveFastestPromoteTarget(job.Placement.TierID, policy, tiers)
	}

	for _, edge := range policy.Edges {
		if edge.Enabled && edge.Kind == EdgePromote && (edge.From == job.Placement.TierID || edge.To == targetTierID) {
			if edge.Trigger.HeatAtLeast > boostHeat {
				boostHeat = edge.Trigger.HeatAtLeast
			}
		}
	}

	decayed := st.Decayed(now, policy.HalfLifeHours)
	if decayed < boostHeat {
		st.Score = boostHeat
	} else {
		st.Score = decayed
	}
	st.ScoreAt = now

	pinTier := targetTierID
	pinUntil := now.Add(time.Duration(ttlHours * float64(time.Hour)))
	st.PinTierID = &pinTier
	st.PinUntil = pinUntil
	if st.TierSince.IsZero() {
		st.TierSince = now
	}

	if e.store != nil {
		if err := e.store.SaveLifecycleState(st); err != nil {
			return nil, err
		}
	}

	// Run an evaluation tick immediately so the pin/promote task is queued right away, plus kick the background loop.
	e.RunTick()
	e.Kick()

	var taskID string
	if e.store != nil {
		if activeID, _ := e.store.GetActiveLifecycleTask(key); activeID != "" {
			if tk, _ := e.store.GetLifecycleTask(activeID); tk != nil && !tk.State.IsTerminal() {
				taskID = activeID
			}
		}
	}

	return &DemandResponse{
		Key:         key,
		PinnedTier:  &pinTier,
		PinUntil:    &pinUntil,
		CurrentTier: job.Placement.TierID,
		TaskID:      taskID,
	}, nil
}

func (e *Engine) Store() Store {
	return e.store
}

// HitBufferStats reports pending play hits waiting to be flushed to the store.
func (e *Engine) HitBufferStats() cache.Snapshot {
	if e.hits == nil {
		return cache.Snapshot{Name: "play_hit_buffer"}
	}
	return e.hits.Stats()
}

// InFlightStats reports relocation tasks held in memory by the executor.
func (e *Engine) InFlightStats() cache.Snapshot {
	if e.executor == nil {
		return cache.Snapshot{Name: "relocation_inflight"}
	}
	return e.executor.InFlightStats()
}

// ListAudits returns recorded expire audit entries up to limit, ordered newest first.
func (e *Engine) ListAudits(limit int) ([]*AuditEntry, error) {
	if e.store == nil {
		return []*AuditEntry{}, nil
	}
	return e.store.ListLifecycleAudits(limit)
}

// Simulate evaluates where a new file of sizeBytes would be placed under the active ingest policy.
func (e *Engine) Simulate(sizeBytes int64) (*SimulateResult, error) {
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	required := uint64(sizeBytes)

	var nodes []*cluster.NodeRecord
	if e.coord != nil {
		nodes = e.coord.GetNodes()
	}

	var statuses []storage.TierStatus
	if e.tiers != nil {
		statuses = e.tiers.Resolve(nodes, e.usage())
	}

	statusByID := make(map[int]*storage.TierStatus, len(statuses))
	for i := range statuses {
		statusByID[statuses[i].ID] = &statuses[i]
	}

	var receivers map[string]bool
	for _, n := range nodes {
		if n.Status != "" {
			receivers = fileapi.ReceiverCandidates(nodes)
			break
		}
	}

	ingest := e.IngestOrder()
	var skipped []string
	for idx, tierID := range ingest {
		st := statusByID[tierID]
		if st == nil {
			skipped = append(skipped, fmt.Sprintf("tier%d (missing)", tierID))
			continue
		}
		bs := fileapi.PickBlockInTier(st, required, receivers)
		if bs == nil {
			skipped = append(skipped, fmt.Sprintf("tier%d (full or offline)", tierID))
			continue
		}
		reason := fmt.Sprintf("ingest priority #%d (tier%d: %s)", idx+1, st.ID, st.Name)
		if len(skipped) > 0 {
			reason = fmt.Sprintf("%s; fallback after %s", reason, strings.Join(skipped, ", "))
		}
		return &SimulateResult{
			TierID: st.ID,
			NodeID: bs.Block.NodeID,
			Path:   bs.Block.Path,
			Reason: reason,
		}, nil
	}

	return nil, ErrNoTierFits
}

// MoveFile validates a manual move request, selects a destination block in targetTierID, and enqueues the relocation task.
func (e *Engine) MoveFile(key string, targetTierID int) (*Task, error) {
	if e.jobStore == nil || e.executor == nil {
		return nil, fmt.Errorf("lifecycle executor not configured")
	}
	job, err := e.jobStore.GetFileJob(key)
	if err != nil || job == nil {
		return nil, ErrFileNotFound
	}
	if job.State != fileapi.StateCompleted {
		return nil, ErrFileNotCompleted
	}
	if job.Placement.TierID == targetTierID {
		return nil, ErrSameTier
	}

	var nodes []*cluster.NodeRecord
	if e.coord != nil {
		nodes = e.coord.GetNodes()
	}
	var statuses []storage.TierStatus
	if e.tiers != nil {
		statuses = e.tiers.Resolve(nodes, e.usage())
	}

	var targetTier *storage.TierStatus
	for i := range statuses {
		if statuses[i].ID == targetTierID {
			targetTier = &statuses[i]
			break
		}
	}
	if targetTier == nil {
		return nil, ErrTierNotFound
	}

	var fileBytes int64
	if job.TotalBytes > 0 {
		fileBytes = job.TotalBytes
	} else if rec, err := e.jobStore.GetFileRecord(key); err == nil && rec != nil && rec.SizeBytes > 0 {
		fileBytes = rec.SizeBytes
	}

	var receivers map[string]bool
	for _, n := range nodes {
		if n.Status != "" {
			receivers = fileapi.ReceiverCandidates(nodes)
			break
		}
	}

	required := uint64(float64(fileBytes) * 1.05)
	bs := fileapi.PickBlockInTier(targetTier, required, receivers)
	if bs == nil && fileBytes > 0 {
		bs = fileapi.PickBlockInTier(targetTier, uint64(fileBytes), receivers)
	}
	if bs == nil {
		hasOnlineCandidate := false
		for _, b := range targetTier.Blocks {
			if b.Online && (receivers == nil || receivers[b.Block.NodeID]) {
				hasOnlineCandidate = true
				break
			}
		}
		if !hasOnlineCandidate {
			return nil, fmt.Errorf("target tier %s has no online nodes or disks available (destination nodes are offline)", targetTier.Name)
		}
		return nil, ErrNoTierFits
	}

	pubHost := bs.Block.PublicHost
	if pubHost == "" && targetTier != nil {
		pubHost = targetTier.PublicHost
	}

	task := &Task{
		Key:    key,
		Kind:   TaskManual,
		Reason: fmt.Sprintf("manual move to tier%d (%s)", targetTier.ID, targetTier.Name),
		From:   job.Placement,
		To: fileapi.Placement{
			TierID:     targetTier.ID,
			TierLabel:  fileapi.TierLabel(targetTier.ID),
			NodeID:     bs.Block.NodeID,
			Path:       bs.Block.Path,
			PublicHost: pubHost,
		},
		Bytes: fileBytes,
	}
	return e.executor.Enqueue(task)
}
