package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"stream/pkg/db"
)

// handleAPIKeys handles listing, creating, and deleting API keys.
func (s *Server) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleListAPIKeys(w, r)
	case http.MethodPost:
		s.handleCreateAPIKey(w, r)
	case http.MethodDelete:
		s.handleDeleteAPIKey(w, r)
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": []*db.APIKeyRecord{}})
		return
	}
	keys, err := s.store.ListAPIKeys()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []*db.APIKeyRecord{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONErr(w, http.StatusInternalServerError, "database not initialized")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "API Key " + time.Now().Format("2006-01-02 15:04")
	}

	rec, err := db.GenerateAPIKey(name)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "failed to generate api key: "+err.Error())
		return
	}

	if err := s.store.SaveAPIKey(rec); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "failed to save api key: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(rec)
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONErr(w, http.StatusInternalServerError, "database not initialized")
		return
	}

	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" && r.Body != nil {
		var req struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			key = strings.TrimSpace(req.Key)
		}
	}
	if key == "" {
		writeJSONErr(w, http.StatusBadRequest, "key parameter is required")
		return
	}

	if err := s.store.DeleteAPIKey(key); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "failed to delete api key: "+err.Error())
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "key": key})
}

// handleAPIKeySub handles sub-resource actions such as DELETE /api/keys/{key}.
func (s *Server) handleAPIKeySub(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api/keys/{key}
	if len(parts) < 3 {
		writeJSONErr(w, http.StatusBadRequest, "key identifier missing")
		return
	}
	key := parts[2]

	switch r.Method {
	case http.MethodDelete:
		if s.store == nil {
			writeJSONErr(w, http.StatusInternalServerError, "database not initialized")
			return
		}
		if err := s.store.DeleteAPIKey(key); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "key": key})
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// validateFileAPIAuth authorizes incoming v1 file operations via API key, admin session, or cluster secret.
func (s *Server) validateFileAPIAuth(r *http.Request) bool {
	// 1. Direct local CLI or local processes (not forwarded through reverse proxy / Cloudflare tunnel)
	if isDirectLoopback(r) {
		return true
	}

	// 2. User session cookie
	if sess, err := s.getSessionFromRequest(r); err == nil && sess != nil {
		return true
	}

	// 3. Browser admin session cookie (legacy/fallback)
	sec := adminSecret(s)
	if c, err := r.Cookie(adminCookieName); err == nil && c.Value != "" {
		expected := adminCookieToken(sec)
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(expected)) == 1 {
			return true
		}
	}

	// 3. Cluster secret header/bearer
	if s.hub != nil && s.hub.Secret() != "" {
		provided := r.Header.Get("X-Cluster-Secret")
		if provided == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				provided = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(s.hub.Secret())) == 1 {
			return true
		}
	}

	// 4. API Key verification via X-API-Key or Authorization: Bearer sm_live_...
	apiKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if apiKey == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			candidate := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			if strings.HasPrefix(candidate, "sm_live_") {
				apiKey = candidate
			}
		}
	}

	if apiKey != "" && s.store != nil {
		if rec, err := s.store.GetAPIKey(apiKey); err == nil && rec != nil {
			go func(k string) {
				_ = s.store.UpdateAPIKeyLastUsed(k)
			}(apiKey)
			return true
		}
		// Explicit API key was supplied but is invalid/revoked
		return false
	}

	// 5. If API keys exist in BadgerDB, external callers must provide a valid API key
	if s.store != nil {
		if hasKeys, err := s.store.HasAnyAPIKeys(); err == nil && hasKeys {
			return false
		}
	}

	// 6. Initial bootstrap fallback if no API keys exist and no secret enforced
	return s.hub == nil || s.hub.Secret() == ""
}

// resolveFileAPICreator returns the human-readable originator of the file ingest request:
// either the API key's name, or "Dashboard" if triggered from an authenticated browser session/loopback.
func (s *Server) resolveFileAPICreator(r *http.Request) string {
	apiKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if apiKey == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			candidate := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			if strings.HasPrefix(candidate, "sm_live_") {
				apiKey = candidate
			}
		}
	}
	if apiKey != "" && s.store != nil {
		if rec, err := s.store.GetAPIKey(apiKey); err == nil && rec != nil {
			if rec.Name != "" {
				return rec.Name
			}
			return rec.Prefix
		}
	}
	return "Dashboard"
}
