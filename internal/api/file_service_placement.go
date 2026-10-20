package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"stream/internal/cluster"
	"stream/internal/storage"
)

func (s *Service) PlanPlacement(requiredBytes uint64, receivers map[string]bool) (Placement, error) {
	nodes := s.coord.GetNodes()
	statuses := s.tiers.Resolve(nodes, s.resolveUsage())

	if s.ingestOrder != nil {
		for _, tierID := range s.ingestOrder() {
			st := s.tierByID(statuses, tierID)
			if bs := PickBlockInTier(st, requiredBytes, receivers); bs != nil {
				return s.placementOf(st, bs), nil
			}
		}
		return Placement{}, fmt.Errorf("insufficient storage: all storage tiers are full for %d bytes", requiredBytes)
	}

	// 1. Direct fit on the preferred default / bulk tier.
	var defaultTier *storage.TierStatus
	for i := range statuses {
		if statuses[i].Default {
			defaultTier = &statuses[i]
			break
		}
	}
	if defaultTier == nil {
		defaultTier = s.tierByID(statuses, DefaultPolicy.HDDTierID)
	}

	if bs := PickBlockInTier(defaultTier, requiredBytes, receivers); bs != nil {
		return s.placementOf(defaultTier, bs), nil
	}

	// 2. Overflow onto the warm tier (SSD).
	ssd := s.tierByID(statuses, DefaultPolicy.SSDTierID)
	if ssd != nil && (defaultTier == nil || ssd.ID != defaultTier.ID) {
		if bs := PickBlockInTier(ssd, requiredBytes, receivers); bs != nil {
			return s.placementOf(ssd, bs), nil
		}
	}

	// 3. Fallback onto any other configured tier with usable capacity (Hot NVMe or custom tiers).
	for i := range statuses {
		st := &statuses[i]
		if (defaultTier != nil && st.ID == defaultTier.ID) || (ssd != nil && st.ID == ssd.ID) {
			continue
		}
		if bs := PickBlockInTier(st, requiredBytes, receivers); bs != nil {
			return s.placementOf(st, bs), nil
		}
	}

	return Placement{}, fmt.Errorf("insufficient storage: all storage tiers are full for %d bytes", requiredBytes)
}

func (s *Service) placementOf(tier *storage.TierStatus, bs *storage.BlockStatus) Placement {
	pubHost := bs.Block.PublicHost
	if pubHost == "" && tier != nil {
		pubHost = tier.PublicHost
	}
	return Placement{
		TierID:     tier.ID,
		TierLabel:  TierLabel(tier.ID),
		NodeID:     bs.Block.NodeID,
		Path:       bs.Block.Path,
		PublicHost: pubHost,
	}
}

// callIngestDelete issues an HTTP delete to a worker agent to remove files and scratch.
func (s *Service) callIngestDelete(node *cluster.NodeRecord, key, dir string) error {
	agentURL := AgentBaseURL(node)
	req, err := http.NewRequest(http.MethodPost, agentURL+"/api/v1/ingest-delete", nil)
	if err != nil {
		return err
	}
	if s.secret != "" {
		req.Header.Set("X-Cluster-Secret", s.secret)
	}
	q := req.URL.Query()
	q.Set("key", key)
	if dir != "" {
		q.Set("dir", dir)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("worker responded %s", resp.Status)
	}
	return nil
}

// deleteFromWorker removes a file's folder from its worker or S3 bucket and drops the DB rows.
func (s *Service) deleteFromWorker(rec *FileRecord) error {
	job, err := s.store.GetFileJob(rec.Key)
	if err != nil || job == nil {
		return fmt.Errorf("job row missing for %s", rec.Key)
	}

	// 1. Remote S3 / Cloudflare R2 bucket placement
	if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
		bucketID := strings.TrimPrefix(job.Placement.NodeID, "bucket:")
		if s.bucketResolver != nil {
			cfg, err := s.bucketResolver(bucketID)
			if err == nil && cfg != nil {
				cCfg := storage.S3Config{
					Name:      cfg.Name,
					Endpoint:  cfg.Endpoint,
					Region:    cfg.Region,
					AccessKey: cfg.AccessKey,
					SecretKey: cfg.SecretKey,
				}
				prefix := rec.Key
				if job.Placement.Path != "" && job.Placement.Path != "." && job.Placement.Path != "/" && job.Placement.Path != cfg.Name {
					prefix = strings.Trim(job.Placement.Path, "/") + "/" + rec.Key
				}
				if delErr := storage.DeleteFolderS3(context.Background(), cCfg, prefix); delErr != nil {
					log.Printf("[Service Delete] ⚠️ S3 DeleteFolder '%s' error: %v", prefix, delErr)
				}
			}
		}
		// Also clean scratch if processing worker node is known
		if job.NodeID != "" {
			if wNode := s.nodeByID(job.NodeID); wNode != nil {
				_ = s.callIngestDelete(wNode, rec.Key, "")
			}
		}
		_ = s.store.DeleteFileJob(rec.Key)
		_ = s.store.DeleteFileRecord(rec.Key)
		return nil
	}

	// 2. Local VPS placement
	node := s.nodeByID(job.Placement.NodeID)
	if node != nil {
		if err := s.callIngestDelete(node, rec.Key, CleanPlacementPath(job.Placement.Path)); err != nil {
			return err
		}
	} else if job.Placement.NodeID != "" {
		log.Printf("[Service Delete] ⚠️ Storage node '%s' is offline; purging record from DB", job.Placement.NodeID)
	}

	// Clean scratch on transcoding worker if decoupled
	if job.NodeID != "" && job.NodeID != job.Placement.NodeID {
		if wNode := s.nodeByID(job.NodeID); wNode != nil {
			_ = s.callIngestDelete(wNode, rec.Key, "")
		}
	}

	_ = s.store.DeleteFileJob(rec.Key)
	_ = s.store.DeleteFileRecord(rec.Key)
	return nil
}

// nodeByID finds a live node record.
