package cluster

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
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

// GeneratePlaybackTicket creates an opaque, tamper-proof ticket that completely hides the real media key.
// The ticket encodes key and exp encrypted with AES-GCM under the cluster secret.
func GeneratePlaybackTicket(key string, exp int64, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("empty cluster secret")
	}
	k := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	plaintext := []byte(fmt.Sprintf("%s:%d", key, exp))
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return "t_" + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// ResolvePlaybackTicket decrypts an opaque ticket into the real media key and verifies expiration.
func ResolvePlaybackTicket(ticket, secret string) (string, int64, error) {
	if secret == "" {
		return "", 0, errors.New("empty cluster secret")
	}
	ticket = strings.TrimSuffix(ticket, ".mp4")
	raw := strings.TrimPrefix(ticket, "t_")
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", 0, errors.New("invalid ticket encoding")
	}
	k := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return "", 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", 0, err
	}
	if len(data) < gcm.NonceSize() {
		return "", 0, errors.New("ticket too short")
	}
	nonce := data[:gcm.NonceSize()]
	ciphertext := data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", 0, errors.New("tampered or invalid ticket")
	}
	parts := strings.Split(string(plaintext), ":")
	if len(parts) != 2 {
		return "", 0, errors.New("corrupt ticket payload")
	}
	key := parts[0]
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, errors.New("invalid ticket expiration")
	}
	if time.Now().Unix() > exp {
		return "", 0, errors.New("ticket expired")
	}
	return key, exp, nil
}
