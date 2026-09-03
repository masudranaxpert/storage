package web

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	validPaths := map[string]bool{
		"/":          true,
		"/dashboard": true,
		"/nodes":     true,
		"/storage":   true,
		"/tiers":     true,
		"/media":     true,
		"/database":  true,
		"/settings":  true,
		"/docs":      true,
	}

	if !validPaths[r.URL.Path] {
		http.NotFound(w, r)
		return
	}

	currentPage := "dashboard"
	switch r.URL.Path {
	case "/nodes":
		currentPage = "nodes"
	case "/storage":
		currentPage = "storage"
	case "/tiers":
		currentPage = "tiers"
	case "/media":
		currentPage = "media"
	case "/database":
		currentPage = "database"
	case "/settings":
		currentPage = "settings"
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
		}
		if err := s.tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
			http.Error(w, fmt.Sprintf("Template rendering error: %v", err), http.StatusInternalServerError)
		}
		return
	}

	http.Error(w, fmt.Sprintf("Templates not found in %s", s.templateDir), http.StatusInternalServerError)
}
