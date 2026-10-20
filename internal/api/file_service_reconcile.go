package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"stream/internal/cache"
	"stream/internal/cluster"
	"stream/internal/storage"
)

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepStaleJobs()
		}
	}
}

// sweepStaleJobs scans active jobs and marks them failed if their worker goes offline for > 3m.
// Without a backing store (degraded in-memory coordinator) there is nothing to sweep.
func (s *Service) sweepStaleJobs() {
	if s.store == nil {
		return
	}
	jobs, err := s.store.ListFileJobs()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, job := range jobs {
		if job.State == StateCompleted || job.State == StateFailed {
			continue
		}
		if now.Sub(job.UpdatedAt) > 3*time.Minute {
			worker := s.nodeByID(job.NodeID)
			if worker == nil || worker.Status == cluster.StatusOffline {
				job.State = StateFailed
				job.Error = fmt.Sprintf("worker node '%s' went offline or timed out", job.NodeID)
				job.UpdatedAt = now
				_ = s.store.SaveFileJob(job)
				fmt.Printf("[FileAPI Watchdog] ⚠️ File job '%s' on %s marked failed: worker went offline\n", job.Key, job.NodeID)
			}
		}
	}
}

// ReconcileGhostRecords verifies that completed file records actually exist on the target worker or bucket.
// Requires 3 consecutive 404 strikes before deleting DB rows to avoid purging on transient glitches,
// unless force is true (e.g. manual admin sync).
func (s *Service) ReconcileGhostRecords() (int, error) {
	return s.ReconcileGhostRecordsOpt(false)
}

// ReconcileGhostRecordsOpt reconciles completed file records against live workers and cloud buckets.
func (s *Service) ReconcileGhostRecordsOpt(force bool) (int, error) {
	recs, err := s.store.ListFileRecords()
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	purged := 0
	for _, rec := range recs {
		job, err := s.store.GetFileJob(rec.Key)
		if err != nil || job == nil || job.State != StateCompleted {
			continue
		}

		// 1. Reconcile remote S3 / Cloudflare R2 bucket objects
		if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
			bucketID := strings.TrimPrefix(job.Placement.NodeID, "bucket:")
			if s.bucketResolver != nil {
				cfg, err := s.bucketResolver(bucketID)
				if err == nil && cfg != nil {
					cCfg := storage.S3Config{
						Endpoint:  cfg.Endpoint,
						Region:    cfg.Region,
						Name:      cfg.Name,
						AccessKey: cfg.AccessKey,
						SecretKey: cfg.SecretKey,
					}
					objs, err := storage.ListObjectsS3(context.Background(), cCfg, rec.Key)
					if err == nil && len(objs) == 0 {
						s.strikeMu.Lock()
						if s.missingStrike == nil {
							s.missingStrike = make(map[string]int)
						}
						s.missingStrike[rec.Key]++
						strikes := s.missingStrike[rec.Key]
						if force || strikes >= 3 {
							delete(s.missingStrike, rec.Key)
							s.strikeMu.Unlock()
							fmt.Printf("[FileAPI Reconcile] 🧹 Ghost file '%s' (%s) missing in bucket %s — cleaning DB record\n",
								rec.Key, rec.Filename, cfg.Name)
							if s.deleteHandler != nil {
								s.deleteHandler(rec.Key)
							}
							_ = s.store.DeleteFileJob(rec.Key)
							_ = s.store.DeleteFileRecord(rec.Key)
							purged++
							continue
						}
						s.strikeMu.Unlock()
					}
				}
			}
			continue
		}

		// 2. Reconcile VPS worker node files
		worker := s.nodeByID(job.Placement.NodeID)
		if worker == nil && s.coord != nil {
			for _, n := range s.coord.GetNodes() {
				if n.Metrics.NodeID == job.Placement.NodeID {
					worker = n
					break
				}
			}
		}
		if worker == nil {
			continue
		}

		probeURL := fmt.Sprintf("%s/media/%s/metadata.json", AgentBaseURL(worker), rec.Key)
		req, err := http.NewRequest(http.MethodHead, probeURL, nil)
		if err != nil {
			continue
		}
		if s.secret != "" {
			req.Header.Set("X-Cluster-Secret", s.secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		s.strikeMu.Lock()
		if s.missingStrike == nil {
			s.missingStrike = make(map[string]int)
		}
		if resp.StatusCode == http.StatusNotFound {
			s.missingStrike[rec.Key]++
			strikes := s.missingStrike[rec.Key]
			if force || strikes >= 3 {
				delete(s.missingStrike, rec.Key)
				s.strikeMu.Unlock()
				fmt.Printf("[FileAPI Reconcile] 🧹 Ghost file '%s' (%s) missing on %s disk — cleaning DB record\n",
					rec.Key, rec.Filename, job.Placement.NodeID)
				if s.deleteHandler != nil {
					s.deleteHandler(rec.Key)
				}
				_ = s.store.DeleteFileJob(rec.Key)
				_ = s.store.DeleteFileRecord(rec.Key)
				purged++
				continue
			}
		} else {
			delete(s.missingStrike, rec.Key)
		}
		s.strikeMu.Unlock()
	}
	return purged, nil
}

// MissingStrikeStats reports the ghost-record strike tracker's live usage.
func (s *Service) MissingStrikeStats() cache.Snapshot {
	s.strikeMu.Lock()
	defer s.strikeMu.Unlock()
	var bytes int64
	for key := range s.missingStrike {
		bytes += int64(len(key)) + 16
	}
	return cache.Snapshot{Name: "ghost_strike_tracker", Entries: len(s.missingStrike), Bytes: bytes}
}

// uniqueKey generates a key until it is free in the file table.
func (s *Service) Status(key string) (*StatusResponse, error) {
	rec, err := s.store.GetFileRecord(key)
	if err != nil {
		return nil, fmt.Errorf("file '%s' not found", key)
	}
	job, err := s.store.GetFileJob(key)
	if err != nil || job == nil {
		return nil, fmt.Errorf("file '%s' not found", key)
	}

	createdBy := rec.CreatedBy
	if createdBy == "" {
		createdBy = "Dashboard"
	}

	playPath := "/stream/" + key
	streamURL := s.StreamURL(job)
	if s.secret != "" {
		exp := time.Now().Add(4 * time.Hour).Unix()
		if ticket, err := GeneratePlaybackTicket(key, exp, s.secret); err == nil && ticket != "" {
			playPath = "/stream/" + ticket
			if streamURL != "" && strings.Contains(streamURL, "/stream/"+key) {
				streamURL = strings.Replace(streamURL, "/stream/"+key, "/stream/"+ticket, 1)
			}
		}
	}

	resp := &StatusResponse{
		Key:              key,
		State:            job.State,
		Filename:         rec.Filename,
		SizeBytes:        rec.SizeBytes,
		MimeType:         rec.MimeType,
		CreatedBy:        createdBy,
		Progress:         job.Progress,
		Speed:            job.Speed,
		Error:            job.Error,
		Stage:            job.Stage,
		StageName:        job.StageName,
		StagePercent:     job.StagePercent,
		TransferredBytes: job.TransferredBytes,
		TotalBytes:       job.TotalBytes,
		ETA:              job.ETA,
		Details:          job.Details,
		Source:           rec.SourceType,
		NodeID:           job.NodeID,
		WorkerNodeID:     job.NodeID,
		Placement:        job.Placement,
		StreamURL:        streamURL,
		PlayURL:          playPath,
		CreatedAt:        rec.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
	}
	if resp.State == StateCompleted {
		resp.Progress = 100
	}
	if len(job.MetadataJSON) > 0 {
		var meta map[string]interface{}
		if err := json.Unmarshal(job.MetadataJSON, &meta); err == nil {
			resp.Metadata = meta
		}
	} else if resp.State == StateCompleted && job.Placement.NodeID != "" {
		// Probe worker's /media/{key}/metadata.json to backfill historical jobs
		worker := s.nodeByID(job.Placement.NodeID)
		if worker != nil && worker.Status == cluster.StatusOnline {
			candidates := []string{
				fmt.Sprintf("%s/media/%s/metadata.json", AgentBaseURL(worker), key),
				fmt.Sprintf("http://172.17.0.1:2052/media/%s/metadata.json", key),
				fmt.Sprintf("http://127.0.0.1:2052/media/%s/metadata.json", key),
			}
			client := &http.Client{Timeout: 600 * time.Millisecond}
			for _, metaURL := range candidates {
				if r, err := client.Get(metaURL); err == nil {
					if r.StatusCode == http.StatusOK {
						data, _ := io.ReadAll(r.Body)
						r.Body.Close()
						if len(data) > 0 {
							var meta map[string]interface{}
							if err := json.Unmarshal(data, &meta); err == nil {
								resp.Metadata = meta
								job.MetadataJSON = data
								_ = s.store.SaveFileJob(job)
								break
							}
						}
					} else {
						r.Body.Close()
					}
				}
			}
		}
	}
	return resp, nil
}

// List returns status for every file, newest first.
func (s *Service) List() ([]*StatusResponse, error) {
	recs, err := s.store.ListFileRecords()
	if err != nil {
		return nil, err
	}
	out := make([]*StatusResponse, 0, len(recs))
	for _, rec := range recs {
		if st, err := s.Status(rec.Key); err == nil {
			out = append(out, st)
		}
	}
	return out, nil
}

// StreamURL points clients at the owning worker's byte-range media server,
// or the custom domain/host link configured for the storage block.
func (s *Service) StreamURL(job *FileJob) string {
	if job.State != StateCompleted || job.Placement.NodeID == "" {
		return ""
	}

	// 1. Check if placement cached a custom domain / host link
	publicHost := job.Placement.PublicHost

	// 2. If not cached (e.g. older job, or updated tier block / tier configuration),
	// dynamically look up the live block's PublicHost or fallback to tier's PublicHost
	if publicHost == "" && s.tiers != nil {
		for _, t := range s.tiers.List() {
			for _, b := range t.Blocks {
				if b.NodeID == job.Placement.NodeID && (b.Path == job.Placement.Path || job.Placement.Path == "") {
					if b.PublicHost != "" {
						publicHost = b.PublicHost
						break
					}
				}
			}
			if publicHost != "" {
				break
			}
			if (t.ID == job.Placement.TierID || t.Label() == job.Placement.TierLabel) && t.PublicHost != "" {
				publicHost = t.PublicHost
				break
			}
		}
	}

	// 2b. If target is an S3/R2 remote bucket, resolve the bucket's PublicHost
	if publicHost == "" && strings.HasPrefix(job.Placement.NodeID, "bucket:") && s.bucketResolver != nil {
		bucketID := strings.TrimPrefix(job.Placement.NodeID, "bucket:")
		if cfg, err := s.bucketResolver(bucketID); err == nil && cfg != nil && cfg.PublicHost != "" {
			publicHost = cfg.PublicHost
		}
	}

	if publicHost != "" {
		host := strings.TrimSpace(publicHost)
		host = strings.TrimRight(host, "/")
		if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
			if strings.Contains(host, ":") {
				host = "http://" + host
			} else {
				host = "https://" + host
			}
		}
		if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
			targetFile := ""
			if len(job.MetadataJSON) > 0 {
				var meta struct {
					Filename string `json:"filename"`
				}
				if json.Unmarshal(job.MetadataJSON, &meta) == nil && meta.Filename != "" {
					targetFile = "/" + meta.Filename
				}
			}
			return fmt.Sprintf("%s/%s%s", host, job.Key, targetFile)
		}
		return fmt.Sprintf("%s/stream/%s", host, job.Key)
	}

	// 3. Fallback: worker agent's direct reachable address
	if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
		return ""
	}
	node := s.nodeByID(job.Placement.NodeID)
	if node == nil || node.Status == cluster.StatusOffline {
		return ""
	}
	return fmt.Sprintf("%s/stream/%s", AgentBaseURL(node), job.Key)
}

// AgentBaseURL builds the worker agent's HTTP root from live telemetry.
// Uses PreferAgentAddr so node-to-node transfers dial a reachable public/VPC/
// advertise address (SeaweedFS-style), not Tailscale CGNAT or loopback.
