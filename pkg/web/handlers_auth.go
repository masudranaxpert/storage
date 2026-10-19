package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter prevents brute-force login attacks.
type loginAttempt struct {
	count     int
	blockedTo time.Time
}

var (
	loginLimiterMu sync.Mutex
	loginAttempts  = make(map[string]*loginAttempt)
)

func checkLoginRateLimit(ip string) bool {
	loginLimiterMu.Lock()
	defer loginLimiterMu.Unlock()

	now := time.Now()
	att, exists := loginAttempts[ip]
	if !exists {
		return true
	}
	if now.Before(att.blockedTo) {
		return false
	}
	// Expire old blocks
	if att.count >= 5 && now.After(att.blockedTo) {
		delete(loginAttempts, ip)
		return true
	}
	return true
}

func recordLoginFailure(ip string) {
	loginLimiterMu.Lock()
	defer loginLimiterMu.Unlock()

	now := time.Now()
	att, exists := loginAttempts[ip]
	if !exists {
		loginAttempts[ip] = &loginAttempt{count: 1}
		return
	}
	att.count++
	if att.count >= 5 {
		// 5 failures -> block for 5 minutes
		att.blockedTo = now.Add(5 * time.Minute)
	}
}

func recordLoginSuccess(ip string) {
	loginLimiterMu.Lock()
	defer loginLimiterMu.Unlock()
	delete(loginAttempts, ip)
}

// handleAuthStatus reports whether setup is required and whether the current caller is authenticated.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	needsSetup := false
	if s.store != nil {
		hasUsers, err := s.store.HasAnyUsers()
		if err == nil && !hasUsers {
			needsSetup = true
		}
	}

	username := ""
	authenticated := false

	if !needsSetup {
		if sess, err := s.getSessionFromRequest(r); err == nil && sess != nil {
			authenticated = true
			username = sess.Username
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"needs_setup":   needsSetup,
		"authenticated": authenticated,
		"username":      username,
	})
}

// handleAuthSetup initializes the first administrator account if no users exist.
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}

	hasUsers, err := s.store.HasAnyUsers()
	if err == nil && hasUsers {
		writeJSONErr(w, http.StatusConflict, "setup already completed; admin user exists")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 {
		writeJSONErr(w, http.StatusBadRequest, "username must be at least 3 characters")
		return
	}
	if len(req.Password) < 6 {
		writeJSONErr(w, http.StatusBadRequest, "password must be at least 6 characters")
		return
	}

	user, err := s.store.CreateUser(req.Username, req.Password)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	sess, err := s.store.CreateSession(user.Username, 7*24*time.Hour)
	if err == nil && sess != nil {
		s.setSessionCookie(w, sess.Token)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "setup_complete",
		"username": user.Username,
	})
}

// handleAuthLogin authenticates with username and password, creating a session.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := clientIP(r)
	if !checkLoginRateLimit(ip) {
		writeJSONErr(w, http.StatusTooManyRequests, "too many failed login attempts; please try again in 5 minutes")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request payload")
		return
	}

	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "database store unavailable")
		return
	}

	user, err := s.store.AuthenticateUser(req.Username, req.Password)
	if err != nil {
		recordLoginFailure(ip)
		writeJSONErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	recordLoginSuccess(ip)

	sess, err := s.store.CreateSession(user.Username, 7*24*time.Hour)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "failed to create session: "+err.Error())
		return
	}

	s.setSessionCookie(w, sess.Token)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "authenticated",
		"username": user.Username,
	})
}

// handleAuthLogout terminates the current user session.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if sess, err := s.getSessionFromRequest(r); err == nil && sess != nil && s.store != nil {
		_ = s.store.DeleteSession(sess.Token)
	}

	s.clearSessionCookie(w)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "logged_out",
	})
}

// handleAuthPasswordChange lets the logged-in administrator update their password.
func (s *Server) handleAuthPasswordChange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sess, err := s.getSessionFromRequest(r)
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.NewPassword) < 6 {
		writeJSONErr(w, http.StatusBadRequest, "new password must be at least 6 characters")
		return
	}

	if err := s.store.UpdateUserPassword(sess.Username, req.OldPassword, req.NewPassword); err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "password_updated",
	})
}

// handleAuthUsers handles GET /api/auth/users (list) and POST /api/auth/users (create user).
func (s *Server) handleAuthUsers(w http.ResponseWriter, r *http.Request) {
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
		users, err := s.store.ListUsers()
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "failed to list users: "+err.Error())
			return
		}
		type safeUser struct {
			Username  string    `json:"username"`
			CreatedAt time.Time `json:"created_at"`
		}
		safeList := make([]safeUser, 0, len(users))
		for _, u := range users {
			safeList = append(safeList, safeUser{
				Username:  u.Username,
				CreatedAt: u.CreatedAt,
			})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"users":  safeList,
		})

	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		req.Username = strings.ToLower(strings.TrimSpace(req.Username))
		if len(req.Username) < 3 {
			writeJSONErr(w, http.StatusBadRequest, "username must be at least 3 characters")
			return
		}
		if len(req.Password) < 6 {
			writeJSONErr(w, http.StatusBadRequest, "password must be at least 6 characters")
			return
		}

		user, err := s.store.CreateUser(req.Username, req.Password)
		if err != nil {
			writeJSONErr(w, http.StatusConflict, err.Error())
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "created",
			"username": user.Username,
		})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAuthUserDelete handles DELETE /api/auth/users/{username}.
func (s *Server) handleAuthUserDelete(w http.ResponseWriter, r *http.Request) {
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

	username := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/auth/users/"))
	username = strings.Trim(username, "/")
	if username == "" {
		writeJSONErr(w, http.StatusBadRequest, "username required")
		return
	}

	// Safety check: ensure at least one user remains so the cluster is never locked out
	users, err := s.store.ListUsers()
	if err == nil && len(users) <= 1 {
		writeJSONErr(w, http.StatusBadRequest, "cannot delete the only remaining administrator account")
		return
	}

	if err := s.store.DeleteUser(username); err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "deleted",
		"message": "User deleted successfully",
	})
}

