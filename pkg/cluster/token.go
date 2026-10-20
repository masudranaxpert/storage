package cluster

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// GenerateStreamToken produces an HMAC-SHA256 signature for a file and expiration timestamp.
func GenerateStreamToken(key string, exp int64, secret string) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%s:%d", key, exp)))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateStreamToken verifies that a stream token is unexpired and matches the expected HMAC signature.
func ValidateStreamToken(key, token, expStr, secret string) bool {
	if secret == "" {
		return false
	}
	if token == "" || expStr == "" {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	expected := GenerateStreamToken(key, exp, secret)
	return hmac.Equal([]byte(token), []byte(expected))
}
