package web

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	validPaths := map[string]bool{
		"/":            true,
		"/dashboard":   true,
		"/nodes":       true,
		"/storage":     true,
		"/tiers":       true,
		"/flows":       true,
		"/flows-queue": true,
		"/flows-hot":   true,
		"/flows-audit": true,
		"/media":       true,
		"/database":    true,
		"/settings":    true,
		"/apikeys":     true,
		"/docs":        true,
	}

	if !validPaths[r.URL.Path] {
		http.NotFound(w, r)
		return
	}

	needsAuth := false
	if s.store != nil {
		hasUsers, err := s.store.HasAnyUsers()
		if err == nil && !hasUsers {
			// No users created yet: initial setup screen is required
			needsAuth = true
			if r.Method == http.MethodGet {
				s.setAdminCookie(w)
			}
		} else {
			// Users exist: valid user session is required
			sess, sessErr := s.getSessionFromRequest(r)
			if sessErr != nil || sess == nil {
				needsAuth = true
			}
		}
	}





	currentPage := "dashboard"
	switch r.URL.Path {
	case "/nodes":
		currentPage = "nodes"
	case "/storage":
		currentPage = "storage"
	case "/tiers":
		currentPage = "tiers"
	case "/flows":
		currentPage = "flows"
	case "/flows-queue":
		currentPage = "flows-queue"
	case "/flows-hot":
		currentPage = "flows-hot"
	case "/flows-audit":
		currentPage = "flows-audit"
	case "/media":
		currentPage = "media"
	case "/database":
		currentPage = "database"
	case "/settings":
		currentPage = "settings"
	case "/apikeys":
		currentPage = "apikeys"
	case "/docs":
		currentPage = "docs"
	default:
		currentPage = "dashboard"
	}

	// Reload templates dynamically during development
	pattern := filepath.Join(s.templateDir, "*", "*.html")
	if tmpl, err := template.ParseGlob(pattern); err == nil {
		s.tmpl = tmpl
	}

	if s.tmpl != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := map[string]interface{}{
			"CurrentPage": currentPage,
			"NeedsAuth":   needsAuth,
		}
		if err := s.tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
			http.Error(w, fmt.Sprintf("Template rendering error: %v", err), http.StatusInternalServerError)
		}
		return
	}


	http.Error(w, fmt.Sprintf("Templates not found in %s", s.templateDir), http.StatusInternalServerError)
}
