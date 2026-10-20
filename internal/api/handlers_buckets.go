package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"stream/internal/database"
	"stream/internal/storage"
)

// handleStorageBuckets manages listing and creation of S3/R2 remote storage buckets.
func (s *Server) handleStorageBuckets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	if s.store == nil {
		writeJSONErr(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	switch r.Method {
	case http.MethodGet:
		buckets, err := s.store.ListBuckets()
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "failed to list buckets: "+err.Error())
			return
		}
		if buckets == nil {
			buckets = []*database.BucketRecord{}
		}

		// Mask secret keys in response
		type safeBucket struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Provider     string `json:"provider"`
			Endpoint     string `json:"endpoint"`
			Region       string `json:"region"`
			AccessKey    string `json:"access_key"`
			PublicHost   string `json:"public_host,omitempty"`
			UsedBytes    int64  `json:"used_bytes"`
			ObjectCount  int64  `json:"object_count"`
			LastSyncedAt string `json:"last_synced_at,omitempty"`
			CreatedAt    string `json:"created_at"`
		}

		safeList := make([]safeBucket, 0, len(buckets))
		for _, b := range buckets {
			lastSynced := ""
			if !b.LastSyncedAt.IsZero() {
				lastSynced = b.LastSyncedAt.Format("2006-01-02 15:04:05")
			}
			safeList = append(safeList, safeBucket{
				ID:           b.ID,
				Name:         b.Name,
				Provider:     b.Provider,
				Endpoint:     b.Endpoint,
				Region:       b.Region,
				AccessKey:    b.AccessKey,
				PublicHost:   b.PublicHost,
				UsedBytes:    b.UsedBytes,
				ObjectCount:  b.ObjectCount,
				LastSyncedAt: lastSynced,
				CreatedAt:    b.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"buckets": safeList,
		})

	case http.MethodPost:
		var req struct {
			Name       string `json:"name"`
			Provider   string `json:"provider"`
			Endpoint   string `json:"endpoint"`
			Region     string `json:"region"`
			AccessKey  string `json:"access_key"`
			SecretKey  string `json:"secret_key"`
			PublicHost string `json:"public_host"`
			SkipTest   bool   `json:"skip_test"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid request body")
			return
		}

		req.Name = strings.TrimSpace(req.Name)
		req.Endpoint = strings.TrimRight(strings.TrimSpace(req.Endpoint), "/")
		req.AccessKey = strings.TrimSpace(req.AccessKey)
		req.SecretKey = strings.TrimSpace(req.SecretKey)
		req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
		if req.Provider == "" {
			req.Provider = "r2"
		}

		if req.Name == "" {
			writeJSONErr(w, http.StatusBadRequest, "bucket name is required")
			return
		}
		if req.Endpoint == "" {
			writeJSONErr(w, http.StatusBadRequest, "endpoint URL is required")
			return
		}
		if req.AccessKey == "" || req.SecretKey == "" {
			writeJSONErr(w, http.StatusBadRequest, "access key and secret key are required")
			return
		}

		rec := &database.BucketRecord{
			Name:       req.Name,
			Provider:   req.Provider,
			Endpoint:   req.Endpoint,
			Region:     req.Region,
			AccessKey:  req.AccessKey,
			SecretKey:  req.SecretKey,
			PublicHost: strings.TrimSpace(req.PublicHost),
		}

		// Perform connection check unless explicitly skipped
		if !req.SkipTest {
			cfg := storage.S3Config{
				Name:      rec.Name,
				Provider:  rec.Provider,
				Endpoint:  rec.Endpoint,
				Region:    rec.Region,
				AccessKey: rec.AccessKey,
				SecretKey: rec.SecretKey,
			}
			if err := storage.TestBucketConnection(r.Context(), cfg); err != nil {
				writeJSONErr(w, http.StatusBadRequest, "connection test failed: "+err.Error())
				return
			}
		}

		if err := s.store.SaveBucket(rec); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "failed to save bucket: "+err.Error())
			return
		}
		go func(c storage.S3Config) {
			_ = storage.EnableBucketCORS(context.Background(), c)
		}(storage.S3Config{
			Name:      rec.Name,
			Provider:  rec.Provider,
			Endpoint:  rec.Endpoint,
			Region:    rec.Region,
			AccessKey: rec.AccessKey,
			SecretKey: rec.SecretKey,
		})

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "created",
			"id":      rec.ID,
			"message": "Bucket registered successfully",
		})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStorageBucketTest transiently verifies S3/R2 credentials without saving.
func (s *Server) handleStorageBucketTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Endpoint  string `json:"endpoint"`
		Region    string `json:"region"`
		AccessKey string `json:"access_key"`
		SecretKey string `json:"secret_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	secretKey := strings.TrimSpace(req.SecretKey)
	if secretKey == "" && req.ID != "" && s.store != nil {
		if existing, err := s.store.GetBucket(req.ID); err == nil && existing != nil {
			secretKey = existing.SecretKey
		}
	}

	cfg := storage.S3Config{
		Name:      req.Name,
		Provider:  req.Provider,
		Endpoint:  req.Endpoint,
		Region:    req.Region,
		AccessKey: req.AccessKey,
		SecretKey: secretKey,
	}
	if err := storage.TestBucketConnection(r.Context(), cfg); err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"message": "Successfully connected to bucket",
	})
}

// handleStorageBucketDelete removes a bucket configuration by ID.
func (s *Server) handleStorageBucketDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.store == nil {
		writeJSONErr(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/storage/buckets/")
	id = strings.Trim(id, "/")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "bucket ID required")
		return
	}

	// Detach bucket from any tiers if currently assigned
	bucketNodeID := "bucket:" + id
	if s.tiers != nil {
		allTiers := s.tiers.List()
		for _, t := range allTiers {
			modified := false
			var remaining []storage.Block
			for _, b := range t.Blocks {
				if b.NodeID == bucketNodeID {
					modified = true
				} else {
					remaining = append(remaining, b)
				}
			}
			if modified {
				t.Blocks = remaining
				_ = s.tiers.Upsert(t)
			}
		}
	}

	if err := s.store.DeleteBucket(id); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "failed to delete bucket: "+err.Error())
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "deleted",
		"message": fmt.Sprintf("Bucket %s removed", id),
	})
}

// handleStorageBucketUpdate modifies an existing bucket configuration.
func (s *Server) handleStorageBucketUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	if s.store == nil {
		writeJSONErr(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/storage/buckets/")
	id = strings.Trim(id, "/")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "bucket ID required")
		return
	}

	existing, err := s.store.GetBucket(id)
	if err != nil || existing == nil {
		writeJSONErr(w, http.StatusNotFound, "bucket not found")
		return
	}

	var req struct {
		Name       string `json:"name"`
		Provider   string `json:"provider"`
		Endpoint   string `json:"endpoint"`
		Region     string `json:"region"`
		AccessKey  string `json:"access_key"`
		SecretKey  string `json:"secret_key"`
		PublicHost string `json:"public_host"`
		SkipTest   bool   `json:"skip_test"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimRight(strings.TrimSpace(req.Endpoint), "/")
	req.AccessKey = strings.TrimSpace(req.AccessKey)
	req.SecretKey = strings.TrimSpace(req.SecretKey)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	if req.Provider == "" {
		req.Provider = existing.Provider
	}

	if req.Name == "" {
		writeJSONErr(w, http.StatusBadRequest, "bucket name is required")
		return
	}
	if req.Endpoint == "" {
		writeJSONErr(w, http.StatusBadRequest, "endpoint URL is required")
		return
	}
	if req.AccessKey == "" {
		writeJSONErr(w, http.StatusBadRequest, "access key is required")
		return
	}

	secretKey := req.SecretKey
	if secretKey == "" {
		secretKey = existing.SecretKey
	}

	if !req.SkipTest {
		cfg := storage.S3Config{
			Name:      req.Name,
			Provider:  req.Provider,
			Endpoint:  req.Endpoint,
			Region:    req.Region,
			AccessKey: req.AccessKey,
			SecretKey: secretKey,
		}
		if err := storage.TestBucketConnection(r.Context(), cfg); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "connection test failed: "+err.Error())
			return
		}
	}

	existing.Name = req.Name
	existing.Provider = req.Provider
	existing.Endpoint = req.Endpoint
	existing.Region = req.Region
	existing.AccessKey = req.AccessKey
	existing.SecretKey = secretKey
	existing.PublicHost = strings.TrimSpace(req.PublicHost)

	if err := s.store.SaveBucket(existing); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "failed to update bucket: "+err.Error())
		return
	}
	go func(c storage.S3Config) {
		_ = storage.EnableBucketCORS(context.Background(), c)
	}(storage.S3Config{
		Name:      existing.Name,
		Provider:  existing.Provider,
		Endpoint:  existing.Endpoint,
		Region:    existing.Region,
		AccessKey: existing.AccessKey,
		SecretKey: existing.SecretKey,
	})

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "updated",
		"id":      existing.ID,
		"message": "Bucket updated successfully",
	})
}

// handleStorageBucketSync calculates total size and object count by querying S3/R2 directly.
func (s *Server) handleStorageBucketSync(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	if s.store == nil {
		writeJSONErr(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/storage/buckets/")
	id = strings.TrimSuffix(id, "/sync")
	id = strings.Trim(id, "/")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "bucket ID required")
		return
	}

	bucket, err := s.store.GetBucket(id)
	if err != nil || bucket == nil {
		writeJSONErr(w, http.StatusNotFound, "bucket not found")
		return
	}

	cfg := storage.S3Config{
		Name:      bucket.Name,
		Provider:  bucket.Provider,
		Endpoint:  bucket.Endpoint,
		Region:    bucket.Region,
		AccessKey: bucket.AccessKey,
		SecretKey: bucket.SecretKey,
	}

	objs, err := storage.ListObjectsS3(r.Context(), cfg, "")
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "failed to scan bucket: "+err.Error())
		return
	}

	var totalBytes int64
	for _, o := range objs {
		totalBytes += o.Size
	}

	bucket.UsedBytes = totalBytes
	bucket.ObjectCount = int64(len(objs))
	bucket.LastSyncedAt = time.Now().UTC()

	if err := s.store.SaveBucket(bucket); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "failed to save bucket size: "+err.Error())
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "ok",
		"id":             bucket.ID,
		"used_bytes":     bucket.UsedBytes,
		"object_count":   bucket.ObjectCount,
		"last_synced_at": bucket.LastSyncedAt.Format("2006-01-02 15:04:05"),
	})
}
