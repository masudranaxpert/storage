package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/fileapi"
	"stream/pkg/lifecycle"
	"stream/pkg/storage"
)

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		policy := s.engine.GetPolicy()
		var nodes []*cluster.NodeRecord
		if s.coord != nil {
			nodes = s.coord.GetNodes()
		}
		var tiers []storage.TierStatus
		if s.tiers != nil {
			tiers = s.tiers.Resolve(nodes, s.mediaUsagePerNode())
		}
		stats := s.computeTierFlowStats(policy, tiers)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"policy": policy,
			"tiers":  tiers,
			"stats":  stats,
		})
		return
	}

	if r.Method == http.MethodPut {
		if !s.requireAdmin(w, r) {
			return
		}

		var req struct {
			lifecycle.FlowPolicy
			ExpireConfirm string `json:"expire_confirm"`
			Confirm       string `json:"confirm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid flow policy payload"}`, http.StatusBadRequest)
			return
		}

		confirm := strings.TrimSpace(req.ExpireConfirm)
		if confirm == "" {
			confirm = strings.TrimSpace(req.Confirm)
		}
		if confirm == "" {
			confirm = strings.TrimSpace(r.URL.Query().Get("expire_confirm"))
		}

		saved, err := s.engine.SavePolicy(req.FlowPolicy, req.Version, confirm)
		if err != nil {
			if errors.Is(err, lifecycle.ErrVersionConflict) {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			var valErr *lifecycle.ValidationError
			if errors.As(err, &valErr) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": valErr.Errors,
				})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(saved)
		return
	}

	http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
}

func (s *Server) handleFlowsSimulate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var sizeBytes int64
	if raw := strings.TrimSpace(r.URL.Query().Get("size_bytes")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid size_bytes parameter"})
			return
		}
		sizeBytes = parsed
	}

	res, err := s.engine.Simulate(sizeBytes)
	if err != nil {
		if errors.Is(err, lifecycle.ErrNoTierFits) {
			w.WriteHeader(http.StatusInsufficientStorage)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(res)
}

func (s *Server) computeTierFlowStats(policy lifecycle.FlowPolicy, tiers []storage.TierStatus) map[int]lifecycle.TierFlowStats {
	stats := make(map[int]lifecycle.TierFlowStats, len(tiers))
	for _, t := range tiers {
		stats[t.ID] = lifecycle.TierFlowStats{}
	}
	if s.store == nil {
		return stats
	}

	hotThresholds := make(map[int]float64)
	for _, e := range policy.Edges {
		if e.Kind == lifecycle.EdgePromote && e.Enabled && e.Trigger.HeatAtLeast > 0 {
			hotThresholds[e.From] = e.Trigger.HeatAtLeast
		}
	}

	jobs, err := s.store.ListFileJobs()
	if err != nil {
		return stats
	}
	states, _ := s.store.ListLifecycleStates()
	stateByKey := make(map[string]*lifecycle.FileState, len(states))
	for _, st := range states {
		if st != nil {
			stateByKey[st.Key] = st
		}
	}

	now := time.Now().UTC()
	for _, job := range jobs {
		if job == nil || job.State != fileapi.StateCompleted {
			continue
		}
		tid := job.Placement.TierID
		entry := stats[tid]
		entry.FileCount++
		if fs := stateByKey[job.Key]; fs != nil {
			thresh := hotThresholds[tid]
			if thresh <= 0 {
				thresh = 1.0
			}
			if fs.Decayed(now, policy.HalfLifeHours) >= thresh {
				entry.HotCount++
			}
		}
		stats[tid] = entry
	}
	return stats
}

func (s *Server) handleLifecycleTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	tasks, err := s.engine.Executor().ListTasks(state, limit)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": tasks,
	})
}

func (s *Server) handleLifecycleTaskAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	// Path: /api/lifecycle/tasks/{id}/{action}
	rest := strings.TrimPrefix(r.URL.Path, "/api/lifecycle/tasks/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeJSONErr(w, http.StatusNotFound, "not found")
		return
	}
	taskID, action := parts[0], parts[1]

	switch action {
	case "cancel":
		if !s.requireAdmin(w, r) {
			return
		}
		task, err := s.engine.Executor().Cancel(taskID)
		if err != nil {
			if errors.Is(err, lifecycle.ErrTaskNotFound) {
				writeJSONErr(w, http.StatusNotFound, err.Error())
				return
			}
			if errors.Is(err, lifecycle.ErrCannotCancel) {
				writeJSONErr(w, http.StatusConflict, err.Error())
				return
			}
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		json.NewEncoder(w).Encode(task)

	case "progress":
		if !s.requireSecret(w, r) {
			return
		}
		var payload lifecycle.ProgressPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid progress payload")
			return
		}
		if err := s.engine.Executor().ApplyProgress(taskID, payload); err != nil {
			if errors.Is(err, lifecycle.ErrTaskNotFound) {
				writeJSONErr(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		writeJSONErr(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) handleFileMove(w http.ResponseWriter, r *http.Request, key string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}

	var req struct {
		TierID *int `json:"tier_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TierID == nil {
		writeJSONErr(w, http.StatusBadRequest, "tier_id is required")
		return
	}

	task, err := s.engine.MoveFile(key, *req.TierID)
	if err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrFileNotFound), errors.Is(err, lifecycle.ErrTierNotFound):
			writeJSONErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, lifecycle.ErrNoTierFits):
			writeJSONErr(w, http.StatusInsufficientStorage, err.Error())
		case errors.Is(err, lifecycle.ErrFileNotCompleted), errors.Is(err, lifecycle.ErrTaskAlreadyActive), errors.Is(err, lifecycle.ErrSameTier):
			writeJSONErr(w, http.StatusConflict, err.Error())
		default:
			writeJSONErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(task)
}

func (s *Server) handleFlowsPreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		res := s.engine.Preview()
		json.NewEncoder(w).Encode(res)
		return
	}
	if r.Method == http.MethodPost {
		var draft lifecycle.FlowPolicy
		if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid draft policy payload")
			return
		}
		res := s.engine.PreviewPolicy(draft)
		json.NewEncoder(w).Encode(res)
		return
	}
	writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (s *Server) handleLifecycleHot(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	hotFiles, err := s.engine.ListHotFiles(limit)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hot_files": hotFiles,
	})
}

func (s *Server) handleFilePlacement(w http.ResponseWriter, r *http.Request, key string) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	view, err := s.engine.FilePlacement(key)
	if err != nil {
		if errors.Is(err, lifecycle.ErrFileNotFound) {
			writeJSONErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	json.NewEncoder(w).Encode(view)
}

func (s *Server) handleFileDemand(w http.ResponseWriter, r *http.Request, key string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}

	var req lifecycle.DemandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp, err := s.engine.SetDemand(key, req)
	if err != nil {
		var valErr *lifecycle.ValidationError
		switch {
		case errors.As(err, &valErr):
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]interface{}{"errors": valErr.Errors})
		case errors.Is(err, lifecycle.ErrFileNotFound):
			writeJSONErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, lifecycle.ErrFileNotCompleted):
			writeJSONErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, lifecycle.ErrTierNotFound):
			writeJSONErr(w, http.StatusUnprocessableEntity, err.Error())
		default:
			writeJSONErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleLifecycleAudit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	audits, err := s.engine.ListAudits(limit)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if audits == nil {
		audits = []*lifecycle.AuditEntry{}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"audits": audits,
	})
}
