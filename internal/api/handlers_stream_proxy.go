package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"stream/internal/cluster"
)

// proxyStream acts as an inline reverse proxy fallback when a client cannot
// reach a storage node directly or when node address is private/behind NAT.
func (s *Server) proxyStream(w http.ResponseWriter, r *http.Request, targetURL string, nodeCORS string) {
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		http.Error(w, "invalid target stream url", http.StatusBadGateway)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{
		Scheme: parsedURL.Scheme,
		Host:   parsedURL.Host,
	})

	sec := adminSecret(s)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = parsedURL.Scheme
		req.URL.Host = parsedURL.Host
		req.URL.Path = parsedURL.Path
		req.URL.RawQuery = parsedURL.RawQuery
		req.Host = parsedURL.Host
		if sec != "" {
			req.Header.Set("X-Cluster-Secret", sec)
		}
		if r.Header.Get("Range") != "" {
			req.Header.Set("Range", r.Header.Get("Range"))
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		origin := r.Header.Get("Origin")
		allowedOrigin := cluster.MatchCORSOrigin(nodeCORS, origin)
		if allowedOrigin != "" {
			resp.Header.Set("Access-Control-Allow-Origin", allowedOrigin)
			resp.Header.Set("Access-Control-Allow-Credentials", "true")
			resp.Header.Set("Vary", "Origin")
		}
		resp.Header.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		resp.Header.Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type, Authorization")
		resp.Header.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		return nil
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		http.Error(rw, "storage node stream unavailable: "+err.Error(), http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
