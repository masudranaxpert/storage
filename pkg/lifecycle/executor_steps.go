package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/fileapi"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
)

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
