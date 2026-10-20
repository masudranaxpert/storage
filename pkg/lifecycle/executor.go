package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"stream/pkg/cache"
	"stream/pkg/cluster"
	"stream/pkg/fileapi"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
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

// ExecutorOption configures an Executor instance.
type ExecutorOption func(*Executor)

// WithExecutorSecret sets the shared cluster secret used to authenticate agent calls.
func WithExecutorSecret(secret string) ExecutorOption {
	return func(ex *Executor) {
		ex.secret = secret
	}
}

// WithCoordinatorURL sets the base URL agents use to post task progress back to the coordinator.
func WithCoordinatorURL(baseURL string) ExecutorOption {
	return func(ex *Executor) {
		ex.coordinatorURL = strings.TrimRight(baseURL, "/")
	}
}

// WithExecutorBucketResolver injects remote S3/R2 bucket config resolution.
func WithExecutorBucketResolver(fn func(id string) (*storage.S3Config, error)) ExecutorOption {
	return func(ex *Executor) {
		ex.bucketResolver = fn
	}
}

// WithExecutorUsageFunc injects live per-node/per-block media byte usage for destination block selection.
func WithExecutorUsageFunc(fn func() map[string]uint64) ExecutorOption {
	return func(ex *Executor) {
		if fn != nil {
			ex.usage = fn
		}
	}
}

// WithExecutorBackoffs overrides the retry backoff schedule (default 1m, 5m, 15m).
func WithExecutorBackoffs(backoffs []time.Duration) ExecutorOption {
	return func(ex *Executor) {
		if len(backoffs) > 0 {
			ex.backoffs = append([]time.Duration(nil), backoffs...)
		}
	}
}

// WithExecutorConcurrency overrides the global (default 4) and per-node (default 2) task limits.
func WithExecutorConcurrency(globalLimit, perNodeLimit int) ExecutorOption {
	return func(ex *Executor) {
		if globalLimit > 0 {
			ex.globalLimit = globalLimit
		}
		if perNodeLimit > 0 {
			ex.perNodeLimit = perNodeLimit
		}
	}
}

// WithExecutorTimeout overrides the per-task timeout (default 6h).
func WithExecutorTimeout(d time.Duration) ExecutorOption {
	return func(ex *Executor) {
		if d > 0 {
			ex.taskTimeout = d
		}
	}
}

// WithExecutorPollInterval overrides the scheduler/copy poll interval.
func WithExecutorPollInterval(d time.Duration) ExecutorOption {
	return func(ex *Executor) {
		if d > 0 {
			ex.pollInterval = d
		}
	}
}

// WithExecutorClock injects a custom clock for deterministic tests.
func WithExecutorClock(fn func() time.Time) ExecutorOption {
	return func(ex *Executor) {
		if fn != nil {
			ex.now = fn
		}
	}
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

func (ex *Executor) executeTask(ctx context.Context, t *Task, fl *inFlightTask) {
	if ex.isCancelled(t.ID) {
		ex.cleanupDestination(t)
		return
	}

	if t.Kind == TaskExpire {
		ex.executeExpireTask(ctx, t, fl)
		return
	}

	// Phase 1: Copying & Verifying (with up to maxAttempts retries).
	if t.State == TaskQueued || t.State == TaskCopying || t.State == TaskVerifying {
		var lastErr error
		for t.Attempts < ex.maxAttempts {
			if ex.isCancelled(t.ID) {
				ex.cleanupDestination(t)
				return
			}
			if ctx.Err() != nil {
				return
			}

			t.Attempts++
			if t.StartedAt.IsZero() {
				t.StartedAt = ex.now().UTC()
			}
			t.State = TaskCopying
			t.Error = ""
			ex.saveTask(t)

			if err := ex.resolveDestinationBlock(t); err != nil {
				lastErr = err
			} else {
				lastErr = ex.doCopy(ctx, t, fl)
			}

			if ex.isCancelled(t.ID) {
				ex.cancelAgentRelocate(t)
				ex.cleanupDestination(t)
				return
			}
			if ctx.Err() != nil {
				return
			}

			if lastErr == nil {
				t.State = TaskVerifying
				ex.saveTask(t)

				if ex.isCancelled(t.ID) {
					ex.cleanupDestination(t)
					return
				}
				if ctx.Err() != nil {
					return
				}

				lastErr = ex.doVerify(ctx, t)
				if ex.isCancelled(t.ID) {
					ex.cleanupDestination(t)
					return
				}
				if ctx.Err() != nil {
					return
				}
			}

			if lastErr == nil {
				break
			}

			// Attempt failed: remove partial destination copy and back off if retries remain.
			ex.cleanupDestination(t)
			t.Error = lastErr.Error()
			ex.saveTask(t)

			if t.Attempts >= ex.maxAttempts {
				t.State = TaskFailed
				ex.saveTask(t)
				ex.clearActive(t.Key)
				return
			}

			if !ex.sleepWithContext(ctx, t.ID, ex.backoffDuration(t.Attempts)) {
				if ex.isCancelled(t.ID) {
					ex.cleanupDestination(t)
				}
				return
			}
		}

		t.State = TaskSwitching
		t.Error = ""
		ex.saveTask(t)
	}

	if ctx.Err() != nil {
		return
	}

	// Phase 2: Switching (atomic metadata pointer update after comparing Placement == From).
	if t.State == TaskSwitching {
		if err := ex.doSwitch(t); err != nil {
			// Placement changed or job missing: fail without deleting source.
			ex.cleanupDestination(t)
			t.State = TaskFailed
			t.Error = err.Error()
			ex.saveTask(t)
			ex.clearActive(t.Key)
			return
		}
		t.State = TaskCleanup
		ex.saveTask(t)
	}

	if ctx.Err() != nil {
		return
	}

	// Phase 3: Cleanup (delete old source copy via ingest-delete).
	if t.State == TaskCleanup {
		var cleanErr error
		for attempt := 1; attempt <= ex.maxAttempts; attempt++ {
			if ctx.Err() != nil {
				return
			}
			cleanErr = ex.doCleanupSource(ctx, t)
			if cleanErr == nil {
				break
			}
			t.Error = "cleanup retry: " + cleanErr.Error()
			ex.saveTask(t)
			if attempt < ex.maxAttempts {
				if !ex.sleepWithContext(ctx, t.ID, ex.backoffDuration(attempt)) {
					return
				}
			}
		}
		t.State = TaskDone
		t.Progress = 100
		t.Speed = "Ready"
		if cleanErr != nil {
			t.Error = "cleanup warning: " + cleanErr.Error()
		} else {
			t.Error = ""
		}
		ex.saveTask(t)
		ex.clearActive(t.Key)
	}
}

func (ex *Executor) executeExpireTask(ctx context.Context, t *Task, fl *inFlightTask) {
	if ex.isCancelled(t.ID) {
		t.State = TaskCancelled
		ex.saveTask(t)
		ex.clearActive(t.Key)
		return
	}

	if t.StartedAt.IsZero() {
		t.StartedAt = ex.now().UTC()
	}
	t.State = TaskCleanup
	ex.saveTask(t)

	// Step 1: Write audit row FIRST (fail task if the write fails).
	var filename string
	var sizeBytes int64 = t.Bytes
	if ex.jobStore != nil {
		if rec, _ := ex.jobStore.GetFileRecord(t.Key); rec != nil {
			filename = rec.Filename
			if rec.SizeBytes > 0 {
				sizeBytes = rec.SizeBytes
			}
		}
	}
	audit := &AuditEntry{
		Key:      t.Key,
		Filename: filename,
		Size:     sizeBytes,
		Tier:     t.From.TierID,
		Node:     t.From.NodeID,
		Path:     t.From.Path,
		EdgeID:   t.EdgeID,
		Reason:   t.Reason,
		At:       ex.now().UTC(),
	}
	if ex.store != nil {
		if err := ex.store.SaveLifecycleAudit(audit); err != nil {
			t.State = TaskFailed
			t.Error = fmt.Sprintf("failed writing audit row: %v", err)
			ex.saveTask(t)
			ex.clearActive(t.Key)
			return
		}
	}

	// Step 2: Agent ingest-delete on current Placement with retries.
	var delErr error
	for attempt := 1; attempt <= ex.maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if ex.isCancelled(t.ID) {
			t.State = TaskCancelled
			ex.saveTask(t)
			ex.clearActive(t.Key)
			return
		}
		t.Attempts = attempt
		delErr = ex.callIngestDelete(ctx, t.From.NodeID, t.Key, t.From.Path)
		if delErr == nil {
			break
		}
		t.Error = fmt.Sprintf("expire delete attempt %d failed: %v", attempt, delErr)
		ex.saveTask(t)
		if attempt < ex.maxAttempts {
			if !ex.sleepWithContext(ctx, t.ID, ex.backoffDuration(attempt)) {
				if ex.isCancelled(t.ID) {
					t.State = TaskCancelled
					ex.saveTask(t)
					ex.clearActive(t.Key)
				}
				return
			}
		}
	}

	// If agent delete fails, keep DB rows and fail task.
	if delErr != nil {
		t.State = TaskFailed
		t.Error = fmt.Sprintf("expire delete failed: %v", delErr)
		ex.saveTask(t)
		ex.clearActive(t.Key)
		return
	}

	// Step 3: Agent delete succeeded -> delete FileJob, FileRecord, lcstate.
	if ex.jobStore != nil {
		_ = ex.jobStore.DeleteFileJob(t.Key)
		_ = ex.jobStore.DeleteFileRecord(t.Key)
	}
	if ex.store != nil {
		_ = ex.store.DeleteLifecycleState(t.Key)
	}

	t.State = TaskDone
	t.Progress = 100
	t.Speed = "Deleted"
	t.Error = ""
	ex.saveTask(t)
	ex.clearActive(t.Key)
}

func (ex *Executor) resolveDestinationBlock(t *Task) error {
	if t.To.NodeID != "" {
		if t.To.TierLabel == "" {
			t.To.TierLabel = fileapi.TierLabel(t.To.TierID)
		}
		return nil
	}
	if ex.tiers == nil {
		return fmt.Errorf("tier manager not configured")
	}
	var nodes []*cluster.NodeRecord
	if ex.coord != nil {
		nodes = ex.coord.GetNodes()
	}
	statuses := ex.tiers.Resolve(nodes, ex.usage())
	var targetTier *storage.TierStatus
	for i := range statuses {
		if statuses[i].ID == t.To.TierID {
			targetTier = &statuses[i]
			break
		}
	}
	if targetTier == nil {
		return fmt.Errorf("destination tier %d not found", t.To.TierID)
	}

	var receivers map[string]bool
	for _, n := range nodes {
		if n.Status != "" {
			receivers = fileapi.ReceiverCandidates(nodes)
			break
		}
	}
	required := uint64(float64(t.Bytes) * 1.05)
	bs := fileapi.PickBlockInTier(targetTier, required, receivers)
	if bs == nil {
		return fmt.Errorf("no eligible block in tier %d", t.To.TierID)
	}
	pubHost := bs.Block.PublicHost
	if pubHost == "" && targetTier != nil {
		pubHost = targetTier.PublicHost
	}
	t.To = fileapi.Placement{
		TierID:     targetTier.ID,
		TierLabel:  fileapi.TierLabel(targetTier.ID),
		NodeID:     bs.Block.NodeID,
		Path:       bs.Block.Path,
		PublicHost: pubHost,
	}
	ex.saveTask(t)
	return nil
}

func (ex *Executor) nodeByID(nodeID string) *cluster.NodeRecord {
	if ex.coord == nil || nodeID == "" {
		return nil
	}
	for _, n := range ex.coord.GetNodes() {
		if n.Metrics.NodeID == nodeID {
			return n
		}
	}
	return nil
}

func (ex *Executor) getBucketConfig(id string) (*storage.S3Config, error) {
	if ex.bucketResolver != nil {
		return ex.bucketResolver(id)
	}
	return nil, fmt.Errorf("no bucket resolver configured on executor")
}

func (ex *Executor) doCopy(ctx context.Context, t *Task, fl *inFlightTask) error {
	isDstBucket := strings.HasPrefix(t.To.NodeID, "bucket:")
	isSrcBucket := strings.HasPrefix(t.From.NodeID, "bucket:")

	var (
		reqTargetBase string
		dstBase       string
		srcBase       string
		dstS3, srcS3  *storage.S3Config
		srcNode       *cluster.NodeRecord
		dstNode       *cluster.NodeRecord
		err           error
	)

	if isDstBucket {
		bucketID := strings.TrimPrefix(t.To.NodeID, "bucket:")
		dstS3, err = ex.getBucketConfig(bucketID)
		if err != nil || dstS3 == nil {
			return fmt.Errorf("destination bucket %s not found: %w", bucketID, err)
		}
	} else {
		dstNode = ex.nodeByID(t.To.NodeID)
		if dstNode == nil {
			return fmt.Errorf("destination node %s not found", t.To.NodeID)
		}
		dstBase = fileapi.AgentBaseURL(dstNode)
	}

	if isSrcBucket {
		bucketID := strings.TrimPrefix(t.From.NodeID, "bucket:")
		srcS3, err = ex.getBucketConfig(bucketID)
		if err != nil || srcS3 == nil {
			return fmt.Errorf("source bucket %s not found: %w", bucketID, err)
		}
		reqTargetBase = dstBase // Destination node downloads from S3
	} else {
		srcNode = ex.nodeByID(t.From.NodeID)
		if srcNode == nil {
			return fmt.Errorf("source node %s not found", t.From.NodeID)
		}
		srcBase = fileapi.AgentBaseURL(srcNode)
		reqTargetBase = srcBase
	}

	// Drain any stale progress messages from a prior attempt.
	for {
		select {
		case <-fl.progressCh:
		default:
			goto drained
		}
	}
drained:

	ex.mu.Lock()
	coordURL := ex.coordinatorURL
	secret := ex.secret
	ex.mu.Unlock()

	var progressURL string
	if coordURL != "" {
		progressURL = fmt.Sprintf("%s/api/lifecycle/tasks/%s/progress", coordURL, url.PathEscape(t.ID))
	}

	reqBody, _ := json.Marshal(cluster.RelocateRequest{
		TaskID:      t.ID,
		Key:         t.Key,
		SrcDir:      t.From.Path,
		FinalNodeID: t.To.NodeID,
		FinalDir:    t.To.Path,
		FinalAddr:   dstBase,
		ProgressURL: progressURL,
		S3:          dstS3,
		SrcS3:       srcS3,
	})

	var srcFiles int
	if srcBase != "" {
		_, srcFiles, _ = ex.fetchMediaSize(ctx, srcBase, t.Key, t.From.Path)
	}
	if srcFiles <= 0 {
		srcFiles = 1
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqTargetBase+"/api/v1/relocate", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Cluster-Secret", secret)
	}

	resp, err := ex.client.Do(req)
	if err != nil {
		return fmt.Errorf("relocate request failed: %w", err)
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("agent at %s returned 404 (needs upgrade to %s with /api/v1/relocate): %s", srcBase, telemetry.CurrentVersion, strings.TrimSpace(string(bodyBytes)))
		}
		return fmt.Errorf("agent relocate returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	ticker := time.NewTicker(ex.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p := <-fl.progressCh:
			if p.State == "failed" || p.Error != "" {
				return fmt.Errorf("relocate failed: %s", p.Error)
			}
			if p.State == "copied" || p.State == "completed" {
				return nil
			}
		case <-ticker.C:
			if ex.isCancelled(t.ID) {
				return context.Canceled
			}
			if progressURL == "" {
				if _, dstFiles, err := ex.fetchMediaSize(ctx, dstBase, t.Key, t.To.Path); err == nil && dstFiles >= srcFiles {
					return nil
				}
			}
		}
	}
}

func (ex *Executor) doVerify(ctx context.Context, t *Task) error {
	isDstBucket := strings.HasPrefix(t.To.NodeID, "bucket:")
	isSrcBucket := strings.HasPrefix(t.From.NodeID, "bucket:")

	if isDstBucket {
		bucketID := strings.TrimPrefix(t.To.NodeID, "bucket:")
		cfg, err := ex.getBucketConfig(bucketID)
		if err != nil || cfg == nil {
			return fmt.Errorf("verify destination bucket %s not found: %w", bucketID, err)
		}
		prefix := t.Key
		if t.To.Path != "" && t.To.Path != "/" && t.To.Path != "." {
			prefix = strings.Trim(t.To.Path, "/") + "/" + t.Key
		}
		size, err := storage.HeadObjectS3(ctx, *cfg, prefix+"/metadata.json")
		if err != nil || size <= 0 {
			objs, listErr := cluster.ListObjectsS3(ctx, cluster.S3Config{
				Endpoint: cfg.Endpoint, Region: cfg.Region, Name: cfg.Name, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey,
			}, prefix)
			if listErr == nil && len(objs) > 0 {
				size = int64(objs[0].Size)
				err = nil
			} else {
				return fmt.Errorf("verify in bucket failed: media object not found on S3: %w", err)
			}
		}
		t.Bytes = size
		ex.saveTask(t)
		return nil
	}

	if isSrcBucket {
		dstNode := ex.nodeByID(t.To.NodeID)
		if dstNode == nil {
			return fmt.Errorf("destination node %s not found", t.To.NodeID)
		}
		dstBase := fileapi.AgentBaseURL(dstNode)
		dstSize, dstFiles, err := ex.fetchMediaSize(ctx, dstBase, t.Key, t.To.Path)
		if err != nil {
			return fmt.Errorf("destination media-size probe failed: %w", err)
		}
		if dstSize <= 0 || dstFiles <= 0 {
			return fmt.Errorf("verify destination size invalid: %d bytes (%d files)", dstSize, dstFiles)
		}
		t.Bytes = dstSize
		ex.saveTask(t)
		return nil
	}

	srcNode := ex.nodeByID(t.From.NodeID)
	if srcNode == nil {
		return fmt.Errorf("source node %s not found", t.From.NodeID)
	}
	dstNode := ex.nodeByID(t.To.NodeID)
	if dstNode == nil {
		return fmt.Errorf("destination node %s not found", t.To.NodeID)
	}

	srcBase := fileapi.AgentBaseURL(srcNode)
	dstBase := fileapi.AgentBaseURL(dstNode)

	srcSize, srcFiles, err := ex.fetchMediaSize(ctx, srcBase, t.Key, t.From.Path)
	if err != nil {
		return fmt.Errorf("source media-size probe failed: %w", err)
	}
	dstSize, dstFiles, err := ex.fetchMediaSize(ctx, dstBase, t.Key, t.To.Path)
	if err != nil {
		return fmt.Errorf("destination media-size probe failed: %w", err)
	}
	if dstSize != srcSize || dstSize <= 0 || (srcFiles > 0 && dstFiles != srcFiles) {
		return fmt.Errorf("verify size mismatch: source=%d bytes (%d files), dest=%d bytes (%d files)", srcSize, srcFiles, dstSize, dstFiles)
	}

	headURL := fmt.Sprintf("%s/media/%s/metadata.json", dstBase, url.PathEscape(t.Key))
	headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil)
	if err != nil {
		return err
	}
	ex.mu.Lock()
	secret := ex.secret
	ex.mu.Unlock()
	if secret != "" {
		headReq.Header.Set("X-Cluster-Secret", secret)
	}
	headResp, err := ex.client.Do(headReq)
	if err != nil {
		return fmt.Errorf("destination metadata.json probe failed: %w", err)
	}
	_, _ = io.Copy(io.Discard, headResp.Body)
	_ = headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		return fmt.Errorf("destination metadata.json returned %d", headResp.StatusCode)
	}

	t.Bytes = dstSize
	ex.saveTask(t)
	return nil
}

func (ex *Executor) fetchMediaSize(ctx context.Context, baseURL, key, dir string) (int64, int, error) {
	u := fmt.Sprintf("%s/api/v1/media-size?key=%s&dir=%s",
		strings.TrimRight(baseURL, "/"), url.QueryEscape(key), url.QueryEscape(dir))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, err
	}
	ex.mu.Lock()
	secret := ex.secret
	ex.mu.Unlock()
	if secret != "" {
		req.Header.Set("X-Cluster-Secret", secret)
	}
	resp, err := ex.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, 0, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var ms cluster.MediaSizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return 0, 0, err
	}
	return ms.Bytes, ms.Files, nil
}

func (ex *Executor) doSwitch(t *Task) error {
	if ex.jobStore == nil {
		return fmt.Errorf("job store not configured")
	}
	job, err := ex.jobStore.GetFileJob(t.Key)
	if err != nil || job == nil {
		return fmt.Errorf("file job %s not found", t.Key)
	}
	if samePlacement(job.Placement, t.To) {
		return nil
	}
	if !samePlacement(job.Placement, t.From) {
		return ErrPlacementChanged
	}

	if t.To.TierLabel == "" {
		t.To.TierLabel = fileapi.TierLabel(t.To.TierID)
	}
	if t.To.PublicHost == "" && ex.tiers != nil {
		for _, tier := range ex.tiers.List() {
			if tier.ID != t.To.TierID {
				continue
			}
			for _, b := range tier.Blocks {
				if b.NodeID == t.To.NodeID && fileapi.CleanPlacementPath(b.Path) == fileapi.CleanPlacementPath(t.To.Path) {
					t.To.PublicHost = b.PublicHost
					break
				}
			}
			if t.To.PublicHost == "" {
				t.To.PublicHost = tier.PublicHost
			}
			break
		}
	}

	job.Placement = t.To
	if err := ex.jobStore.SaveFileJob(job); err != nil {
		return err
	}

	if ex.store != nil {
		st, _ := ex.store.GetLifecycleState(t.Key)
		if st == nil {
			st = &FileState{Key: t.Key}
		}
		now := ex.now().UTC()
		st.TierSince = now
		st.History = append(st.History, Move{
			From:   t.From.TierID,
			To:     t.To.TierID,
			Kind:   EdgeKind(t.Kind),
			Reason: t.Reason,
			At:     now,
		})
		if len(st.History) > 20 {
			st.History = st.History[len(st.History)-20:]
		}
		_ = ex.store.SaveLifecycleState(st)
	}
	return nil
}

func (ex *Executor) doCleanupSource(ctx context.Context, t *Task) error {
	if t.From.NodeID == t.To.NodeID && fileapi.CleanPlacementPath(t.From.Path) == fileapi.CleanPlacementPath(t.To.Path) {
		return nil
	}
	return ex.callIngestDelete(ctx, t.From.NodeID, t.Key, t.From.Path)
}

func (ex *Executor) cleanupDestination(t *Task) {
	if t.To.NodeID == "" {
		return
	}
	if t.From.NodeID == t.To.NodeID && fileapi.CleanPlacementPath(t.From.Path) == fileapi.CleanPlacementPath(t.To.Path) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ex.callIngestDelete(ctx, t.To.NodeID, t.Key, t.To.Path)
}

func (ex *Executor) cancelAgentRelocate(t *Task) {
	srcNode := ex.nodeByID(t.From.NodeID)
	if srcNode == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := fmt.Sprintf("%s/api/v1/relocate/cancel?task_id=%s",
		strings.TrimRight(fileapi.AgentBaseURL(srcNode), "/"), url.QueryEscape(t.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return
	}
	ex.mu.Lock()
	secret := ex.secret
	ex.mu.Unlock()
	if secret != "" {
		req.Header.Set("X-Cluster-Secret", secret)
	}
	resp, err := ex.client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func (ex *Executor) callIngestDelete(ctx context.Context, nodeID, key, dir string) error {
	if strings.HasPrefix(nodeID, "bucket:") {
		bucketID := strings.TrimPrefix(nodeID, "bucket:")
		cfg, err := ex.getBucketConfig(bucketID)
		if err != nil || cfg == nil {
			return fmt.Errorf("bucket %s not found for deletion: %w", bucketID, err)
		}
		prefix := key
		if dir != "" && dir != "/" && dir != "." {
			prefix = strings.Trim(dir, "/") + "/" + key
		}
		return storage.DeleteFolderS3(ctx, *cfg, prefix)
	}

	node := ex.nodeByID(nodeID)
	if node == nil {
		return fmt.Errorf("node %s not found", nodeID)
	}
	u := fmt.Sprintf("%s/api/v1/ingest-delete?key=%s&dir=%s",
		strings.TrimRight(fileapi.AgentBaseURL(node), "/"), url.QueryEscape(key), url.QueryEscape(dir))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	ex.mu.Lock()
	secret := ex.secret
	ex.mu.Unlock()
	if secret != "" {
		req.Header.Set("X-Cluster-Secret", secret)
	}
	resp, err := ex.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ingest-delete on %s returned %d", nodeID, resp.StatusCode)
	}
	return nil
}

// Cancel cancels a task if it is in queued, copying, or verifying state, cleaning up any partial destination folder.
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
