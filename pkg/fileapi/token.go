package fileapi

import "stream/pkg/cluster"

// GenerateStreamToken produces an HMAC-SHA256 signature for a file and expiration timestamp.
func GenerateStreamToken(key string, exp int64, secret string) string {
	return cluster.GenerateStreamToken(key, exp, secret)
}

// ValidateStreamToken verifies that a stream token is unexpired and matches the expected HMAC signature.
func ValidateStreamToken(key, token, expStr, secret string) bool {
	return cluster.ValidateStreamToken(key, token, expStr, secret)
}

// GeneratePlaybackTicket creates an opaque ticket that completely hides the real media key.
func GeneratePlaybackTicket(key string, exp int64, secret string) (string, error) {
	return cluster.GeneratePlaybackTicket(key, exp, secret)
}

// ResolvePlaybackTicket decrypts an opaque ticket into the real media key.
func ResolvePlaybackTicket(ticket, secret string) (string, int64, error) {
	return cluster.ResolvePlaybackTicket(ticket, secret)
}
