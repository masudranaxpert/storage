package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"stream/pkg/cache"
	"stream/pkg/cluster"
	"stream/pkg/fileapi"
	"stream/pkg/storage"
)

var (
	// ErrTaskAlreadyActive is returned when a file already has a non-terminal lifecycle task.
	ErrTaskAlreadyActive = errors.New("a lifecycle task is already active for this file")
	// ErrTaskNotFound is returned when a task ID does not exist.
	ErrTaskNotFound = errors.New("lifecycle task not found")
	// ErrCannotCancel is returned when attempting to cancel a task past the verifying state.
	ErrCannotCancel = errors.New("task can only be cancelled in queued, copying, or verifying state")
	// ErrPlacementChanged is returned when FileJob.Placement changed while a relocation was in flight.
	ErrPlacementChanged = errors.New("placement changed")
	// ErrFileNotFound is returned when the requested file key does not exist.
	ErrFileNotFound = errors.New("file not found")
	// ErrFileNotCompleted is returned when attempting to relocate a file that is not in completed state.
	ErrFileNotCompleted = errors.New("only completed files can be relocated")
	// ErrSameTier is returned when the target tier matches the file's current tier.
	ErrSameTier = errors.New("file is already on the target tier")
	// ErrTierNotFound is returned when the requested tier ID does not exist.
	ErrTierNotFound = errors.New("target tier not found")
)

type inFlightTask struct {
	cancel     context.CancelFunc
	progressCh chan ProgressPayload
	srcNode    string
	dstNode    string
}

// Executor runs the persisted relocation state machine with global and per-node concurrency limits.
type Executor struct {
	mu             sync.Mutex
	store          Store
	jobStore       fileapi.JobStore
	tiers          *storage.Manager
	coord          *cluster.Coordinator
	usage          func() map[string]uint64
	now            func() time.Time
	client         *http.Client
	secret         string
	coordinatorURL string
	bucketResolver func(id string) (*storage.S3Config, error)

	globalLimit  int
	perNodeLimit int
	taskTimeout  time.Duration
	maxAttempts  int
	backoffs     []time.Duration
	pollInterval time.Duration

	queue     []*Task
	inFlight  map[string]*inFlightTask
	cancelled map[string]bool
	running   int
	nodeBusy  map[string]int
	wakeCh    chan struct{}
	wg        sync.WaitGroup
}

// NewExecutor creates a new lifecycle task executor with spec §5.3 defaults.
func NewExecutor(store Store, jobStore fileapi.JobStore, tiers *storage.Manager, coord *cluster.Coordinator, opts ...ExecutorOption) *Executor {
	ex := &Executor{
		store:        store,
		jobStore:     jobStore,
		tiers:        tiers,
		coord:        coord,
		usage:        func() map[string]uint64 { return map[string]uint64{} },
		now:          time.Now,
		client:       &http.Client{Timeout: 30 * time.Second},
		globalLimit:  4,
		perNodeLimit: 2,
		taskTimeout:  6 * time.Hour,
		maxAttempts:  3,
		backoffs:     []time.Duration{1 * time.Minute, 5 * time.Minute, 15 * time.Minute},
		pollInterval: 50 * time.Millisecond,
		inFlight:     make(map[string]*inFlightTask),
		cancelled:    make(map[string]bool),
		nodeBusy:     make(map[string]int),
		wakeCh:       make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(ex)
	}
	return ex
}

// SetSecret updates the cluster secret used for outgoing agent requests.
func (ex *Executor) SetSecret(secret string) {
	ex.mu.Lock()
	ex.secret = secret
	ex.mu.Unlock()
}

// SetCoordinatorURL updates the coordinator base URL used to build progress callbacks.
func (ex *Executor) SetCoordinatorURL(baseURL string) {
	ex.mu.Lock()
	ex.coordinatorURL = strings.TrimRight(baseURL, "/")
	ex.mu.Unlock()
}

func (ex *Executor) wake() {
	select {
	case ex.wakeCh <- struct{}{}:
	default:
	}
}

func samePlacement(a, b fileapi.Placement) bool {
	return a.TierID == b.TierID &&
		a.NodeID == b.NodeID &&
		fileapi.CleanPlacementPath(a.Path) == fileapi.CleanPlacementPath(b.Path)
}

// Enqueue persists a new task and schedules it if no other task is active for its file key.
func (ex *Executor) Enqueue(t *Task) (*Task, error) {
	if t == nil || t.Key == "" {
		return nil, fmt.Errorf("task key is required")
	}

	ex.mu.Lock()
	defer ex.mu.Unlock()

	if ex.store != nil {
		activeID, err := ex.store.GetActiveLifecycleTask(t.Key)
		if err != nil {
			return nil, err
		}
		if activeID != "" && activeID != t.ID {
			existing, _ := ex.store.GetLifecycleTask(activeID)
			if existing == nil || !existing.State.IsTerminal() {
				return nil, ErrTaskAlreadyActive
			}
		}
	}

	now := ex.now().UTC()
	if t.ID == "" {
		t.ID = NewTaskID()
	}
	if t.State == "" {
		t.State = TaskQueued
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now

	cp := *t
	if ex.store != nil {
		if err := ex.store.SaveLifecycleTask(&cp); err != nil {
			return nil, err
		}
		if err := ex.store.SetActiveLifecycleTask(cp.Key, cp.ID); err != nil {
			return nil, err
		}
	}

	ex.enqueueLocked(&cp)
	ex.wake()
	out := cp
	return &out, nil
}

func (ex *Executor) enqueueLocked(t *Task) {
	for _, existing := range ex.queue {
		if existing.ID == t.ID {
			return
		}
	}
	if _, running := ex.inFlight[t.ID]; running {
		return
	}
	cp := *t
	ex.queue = append(ex.queue, &cp)
}

// RecoverAndRun loads non-terminal tasks from the store per spec §5.3 and runs the dispatch loop until ctx is done.
func (ex *Executor) Run(ctx context.Context) {
	ex.recoverTasks()

	ticker := time.NewTicker(ex.pollInterval)
	defer ticker.Stop()

	ex.dispatchReady(ctx)
	for {
		select {
		case <-ctx.Done():
			ex.wg.Wait()
			return
		case <-ex.wakeCh:
			ex.dispatchReady(ctx)
		case <-ticker.C:
			ex.dispatchReady(ctx)
		}
	}
}

func (ex *Executor) recoverTasks() {
	if ex.store == nil {
		return
	}
	tasks, err := ex.store.ListLifecycleTasks()
	if err != nil {
		return
	}

	// Process oldest tasks first.
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	ex.mu.Lock()
	defer ex.mu.Unlock()

	for _, t := range tasks {
		if t == nil || t.State.IsTerminal() {
			continue
		}
		switch t.State {
		case TaskCopying, TaskVerifying:
			t.State = TaskCopying
			t.Attempts = 0
			t.Error = ""
			_ = ex.store.SaveLifecycleTask(t)
		case TaskSwitching:
			if ex.jobStore != nil {
				if job, err := ex.jobStore.GetFileJob(t.Key); err == nil && job != nil && samePlacement(job.Placement, t.To) {
					t.State = TaskCleanup
					_ = ex.store.SaveLifecycleTask(t)
				}
			}
		case TaskCleanup, TaskQueued:
		}
		_ = ex.store.SetActiveLifecycleTask(t.Key, t.ID)
		ex.enqueueLocked(t)
	}
}

func (ex *Executor) dispatchReady(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	ex.mu.Lock()
	defer ex.mu.Unlock()

	var remaining []*Task
	for _, t := range ex.queue {
		if ex.cancelled[t.ID] || t.State.IsTerminal() {
			continue
		}
		if ex.running >= ex.globalLimit {
			remaining = append(remaining, t)
			continue
		}
		srcNode := t.From.NodeID
		dstNode := t.To.NodeID
		if srcNode != "" && ex.nodeBusy[srcNode] >= ex.perNodeLimit {
			remaining = append(remaining, t)
			continue
		}
		if dstNode != "" && dstNode != srcNode && ex.nodeBusy[dstNode] >= ex.perNodeLimit {
			remaining = append(remaining, t)
			continue
		}

		ex.running++
		if srcNode != "" {
			ex.nodeBusy[srcNode]++
		}
		if dstNode != "" && dstNode != srcNode {
			ex.nodeBusy[dstNode]++
		}

		taskCtx, cancel := context.WithTimeout(ctx, ex.taskTimeout)
		flight := &inFlightTask{
			cancel:     cancel,
			progressCh: make(chan ProgressPayload, 32),
			srcNode:    srcNode,
			dstNode:    dstNode,
		}
		ex.inFlight[t.ID] = flight

		taskCopy := *t
		ex.wg.Add(1)
		go func(tk Task, fl *inFlightTask) {
			defer ex.wg.Done()
			defer func() {
				fl.cancel()
				ex.mu.Lock()
				ex.running--
				if fl.srcNode != "" {
					ex.nodeBusy[fl.srcNode]--
				}
				if fl.dstNode != "" && fl.dstNode != fl.srcNode {
					ex.nodeBusy[fl.dstNode]--
				}
				delete(ex.inFlight, tk.ID)
				ex.mu.Unlock()
				ex.wake()
			}()
			ex.executeTask(taskCtx, &tk, fl)
		}(taskCopy, flight)
	}
	ex.queue = remaining
}

func (ex *Executor) isCancelled(taskID string) bool {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return ex.cancelled[taskID]
}

func (ex *Executor) saveTask(t *Task) {
	t.UpdatedAt = ex.now().UTC()
	if ex.store != nil {
		_ = ex.store.SaveLifecycleTask(t)
	}
}

func (ex *Executor) clearActive(key string) {
	if ex.store != nil {
		_ = ex.store.DeleteActiveLifecycleTask(key)
	}
}

func (ex *Executor) backoffDuration(attempt int) time.Duration {
	if len(ex.backoffs) == 0 {
		return 10 * time.Millisecond
	}
	idx := attempt - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ex.backoffs) {
		idx = len(ex.backoffs) - 1
	}
	return ex.backoffs[idx]
}

func (ex *Executor) sleepWithContext(ctx context.Context, taskID string, d time.Duration) bool {
	if d <= 0 {
		return !ex.isCancelled(taskID) && ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return !ex.isCancelled(taskID)
		case <-ticker.C:
			if ex.isCancelled(taskID) {
				return false
			}
		}
	}
}

func (ex *Executor) Cancel(taskID string) (*Task, error) {
	if ex.store == nil {
		return nil, ErrTaskNotFound
	}
	t, err := ex.store.GetLifecycleTask(taskID)
	if err != nil || t == nil {
		return nil, ErrTaskNotFound
	}
	if t.State != TaskQueued && t.State != TaskCopying && t.State != TaskVerifying {
		return nil, ErrCannotCancel
	}

	ex.mu.Lock()
	ex.cancelled[taskID] = true
	var kept []*Task
	for _, q := range ex.queue {
		if q.ID != taskID {
			kept = append(kept, q)
		}
	}
	ex.queue = kept
	fl := ex.inFlight[taskID]
	ex.mu.Unlock()

	if fl != nil && fl.cancel != nil {
		fl.cancel()
	}
	ex.cancelAgentRelocate(t)
	ex.cleanupDestination(t)

	t.State = TaskCancelled
	t.UpdatedAt = ex.now().UTC()
	_ = ex.store.SaveLifecycleTask(t)
	ex.clearActive(t.Key)
	return t, nil
}

// ApplyProgress records an agent progress report for a running relocation task.
func (ex *Executor) ApplyProgress(taskID string, p ProgressPayload) error {
	if ex.store == nil {
		return ErrTaskNotFound
	}
	t, err := ex.store.GetLifecycleTask(taskID)
	if err != nil || t == nil {
		return ErrTaskNotFound
	}
	if !t.State.IsTerminal() {
		if p.ProgressPercent > 0 {
			t.Progress = p.ProgressPercent
		}
		if p.Speed != "" {
			t.Speed = p.Speed
		}
		if p.TotalBytes > 0 {
			t.Bytes = p.TotalBytes
		}
		if p.Error != "" {
			t.Error = p.Error
		}
		ex.saveTask(t)
	}

	ex.mu.Lock()
	fl := ex.inFlight[taskID]
	ex.mu.Unlock()
	if fl != nil {
		select {
		case fl.progressCh <- p:
		default:
			// Ensure terminal events ("copied", "completed", "failed") are never dropped.
			if p.State == "copied" || p.State == "completed" || p.State == "failed" || p.Error != "" {
				select {
				case <-fl.progressCh:
				default:
				}
				select {
				case fl.progressCh <- p:
				default:
				}
			}
		}
	}
	return nil
}

// ListTasks returns persisted tasks filtered by state ("active", "done", "failed", or "") and capped at limit.
func (ex *Executor) ListTasks(stateFilter string, limit int) ([]*Task, error) {
	if ex.store == nil {
		return []*Task{}, nil
	}
	all, err := ex.store.ListLifecycleTasks()
	if err != nil {
		return nil, err
	}
	filter := strings.ToLower(strings.TrimSpace(stateFilter))
	out := make([]*Task, 0, len(all))
	for _, t := range all {
		if t == nil {
			continue
		}
		switch filter {
		case "", "all":
			out = append(out, t)
		case "active":
			if !t.State.IsTerminal() {
				out = append(out, t)
			}
		case "done":
			if t.State == TaskDone {
				out = append(out, t)
			}
		case "failed":
			if t.State == TaskFailed || t.State == TaskCancelled {
				out = append(out, t)
			}
		default:
			if string(t.State) == filter {
				out = append(out, t)
			}
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// InFlightStats reports live relocation tasks held in memory.
func (e *Executor) InFlightStats() cache.Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	var bytes int64
	for id := range e.inFlight {
		bytes += int64(len(id)) + 200
	}
	for id := range e.cancelled {
		bytes += int64(len(id)) + 16
	}
	return cache.Snapshot{Name: "relocation_inflight", Entries: len(e.inFlight), Bytes: bytes}
}
