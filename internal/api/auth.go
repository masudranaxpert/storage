package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"

	"stream/internal/database"
)

const (
	adminCookieName   = "stream_admin"
	sessionCookieName = "stream_session"
)

func isHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

func adminSecret(s *Server) string {
	if s != nil && s.hub != nil && s.hub.Secret() != "" {
		return s.hub.Secret()
	}
	return ""
}

func (s *Server) setAdminCookie(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return
	}
	token := hex.EncodeToString(b)

	s.bootstrapMu.Lock()
	if s.bootstrapTokens == nil {
		s.bootstrapTokens = make(map[string]time.Time)
	}
	now := time.Now()
	for k, exp := range s.bootstrapTokens {
		if now.After(exp) {
			delete(s.bootstrapTokens, k)
		}
	}
	s.bootstrapTokens[token] = now.Add(24 * time.Hour)
	s.bootstrapMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   24 * 3600,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) getSessionFromRequest(r *http.Request) (*database.SessionRecord, error) {
	if s.store == nil {
		return nil, nil
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return s.store.ValidateSession(c.Value)
}

func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isDirectLoopback verifies the request originates directly on localhost without
// reverse-proxy or tunnel headers (such as Cloudflare or nginx).
// If reverse-proxy headers are present, the request traversed an untrusted forwarder or
// proxy, meaning an external attacker could hit a local proxy listening on loopback to bypass auth.
func isDirectLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Header.Get("CF-Connecting-IP") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	return isLoopbackAddr(r.RemoteAddr)
}

func clientIP(r *http.Request) string {
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.TrimSpace(cf)
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// isCallerAuthenticated returns true if the request carries a valid session or secret.
func (s *Server) isCallerAuthenticated(r *http.Request) bool {
	if isDirectLoopback(r) {
		return true
	}
	// 1. Session token
	if sess, err := s.getSessionFromRequest(r); err == nil && sess != nil {
		return true
	}
	// 2. Secret header/bearer
	sec := adminSecret(s)
	provided := r.Header.Get("X-Cluster-Secret")
	if provided == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			candidate := strings.TrimPrefix(auth, "Bearer ")
			if !strings.HasPrefix(candidate, "sm_live_") {
				provided = candidate
			}
		}
	}
	if sec != "" && provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(sec)) == 1 {
		return true
	}
	// 3. Fallback: if no users are created yet in the database, allow bootstrap with admin cookie
	allowBootstrap := false
	if s.store == nil {
		allowBootstrap = true
	} else {
		hasUsers, err := s.store.HasAnyUsers()
		if err == nil && !hasUsers {
			allowBootstrap = true
		}
	}
	if allowBootstrap {
		if c, err := r.Cookie(adminCookieName); err == nil && c.Value != "" {
			s.bootstrapMu.RLock()
			exp, ok := s.bootstrapTokens[c.Value]
			s.bootstrapMu.RUnlock()
			if ok && time.Now().Before(exp) {
				return true
			}
		}
	}
	return false
}

// requireAdmin authorizes privileged API mutations.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.isCallerAuthenticated(r) {
		return true
	}
	writeJSONErr(w, http.StatusUnauthorized, "unauthorized: valid admin session or cluster secret required")
	return false
}

// requireSecret verifies the cluster secret header or Bearer token for agent & cluster operations.
func (s *Server) requireSecret(w http.ResponseWriter, r *http.Request) bool {
	sec := adminSecret(s)
	if sec == "" {
		writeJSONErr(w, http.StatusUnauthorized, "unauthorized: valid cluster secret required")
		return false
	}
	provided := r.Header.Get("X-Cluster-Secret")
	if provided == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			candidate := strings.TrimPrefix(auth, "Bearer ")
			if !strings.HasPrefix(candidate, "sm_live_") {
				provided = candidate
			}
		}
	}
	if provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(sec)) == 1 {
		return true
	}
	writeJSONErr(w, http.StatusUnauthorized, "unauthorized: valid cluster secret required")
	return false
}
