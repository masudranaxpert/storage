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
	for _, raw := range strings.Split(allowedOrigins, ",") {
		o := strings.TrimSpace(raw)
		if o == "*" {
			return requestOrigin
		}
		if o != "" && strings.ToLower(strings.TrimRight(o, "/")) == reqNorm {
			return requestOrigin
		}
	}
	return ""
}
