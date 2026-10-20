package cluster

import "strings"

// MatchCORSOrigin checks requestOrigin against allowedOrigins (e.g. "*", or comma-separated origins).
// Returns the origin to reflect in Access-Control-Allow-Origin, or "" if blocked.
func MatchCORSOrigin(allowedOrigins, requestOrigin string) string {
	allowedOrigins = strings.TrimSpace(allowedOrigins)
	if allowedOrigins == "" || allowedOrigins == "*" {
		if requestOrigin != "" {
			return requestOrigin
		}
		return "*"
	}
	if requestOrigin == "" {
		return "*"
	}
	reqNorm := strings.ToLower(strings.TrimRight(requestOrigin, "/"))
	reqHost := reqNorm
	if idx := strings.Index(reqHost, "://"); idx != -1 {
		reqHost = reqHost[idx+3:]
	}
	if idx := strings.Index(reqHost, "/"); idx != -1 {
		reqHost = reqHost[:idx]
	}
	if idx := strings.Index(reqHost, ":"); idx != -1 {
		reqHost = reqHost[:idx]
	}

	for _, raw := range strings.Split(allowedOrigins, ",") {
		o := strings.TrimSpace(raw)
		if o == "*" {
			return requestOrigin
		}
		if o == "" {
			continue
		}
		oNorm := strings.ToLower(strings.TrimRight(o, "/"))
		if oNorm == reqNorm {
			return requestOrigin
		}
		oHost := oNorm
		if idx := strings.Index(oHost, "://"); idx != -1 {
			oHost = oHost[idx+3:]
		}
		if idx := strings.Index(oHost, "/"); idx != -1 {
			oHost = oHost[:idx]
		}
		if idx := strings.Index(oHost, ":"); idx != -1 {
			oHost = oHost[:idx]
		}
		if oHost == reqHost || reqHost == "www."+oHost || oHost == "www."+reqHost {
			return requestOrigin
		}
	}
	return ""
}
