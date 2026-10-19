package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"

	"stream/pkg/db"
)

const (
	adminCookieName    = "stream_admin"
	adminCookieMessage = "admin-ui"
	defaultAdminSecret = "stream-control-plane-session-2026"

	// sessionCookieName stores the stateful DB session token for logged-in accounts.
	sessionCookieName = "stream_session"
)

func adminSecret(s *Server) string {
	if s != nil && s.hub != nil && s.hub.Secret() != "" {
		return s.hub.Secret()
	}
	return defaultAdminSecret
}

func adminCookieToken(secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(adminCookieMessage))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) setAdminCookie(w http.ResponseWriter) {
	sec := adminSecret(s)
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    adminCookieToken(sec),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
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

func (s *Server) getSessionFromRequest(r *http.Request) (*db.SessionRecord, error) {
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
	if provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(sec)) == 1 {
		return true
	}
	// 3. Fallback: if no users are created yet in the database, allow bootstrap
	if s.store != nil {
		hasUsers, err := s.store.HasAnyUsers()
		if err == nil && !hasUsers {
			if c, err := r.Cookie(adminCookieName); err == nil && c.Value != "" {
				expected := adminCookieToken(sec)
				if subtle.ConstantTimeCompare([]byte(c.Value), []byte(expected)) == 1 {
					return true
				}
			}
			if (s.hub == nil || s.hub.Secret() == "") {
				if hasKeys, _ := s.store.HasAnyAPIKeys(); !hasKeys {
					return true
				}
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

// requireSecret verifies the cluster secret header or Bearer token when a secret is configured.
func (s *Server) requireSecret(w http.ResponseWriter, r *http.Request) bool {
	if s.hub == nil || s.hub.Secret() == "" {
		return true
	}
	sec := s.hub.Secret()
	provided := r.Header.Get("X-Cluster-Secret")
	if provided == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			provided = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(sec)) == 1 {
		return true
	}
	writeJSONErr(w, http.StatusUnauthorized, "unauthorized: valid cluster secret required")
	return false
}
