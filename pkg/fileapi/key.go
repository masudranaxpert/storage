package fileapi

import (
	"crypto/rand"
	"math/big"
)

// keyAlphabet is the URL-safe key space: upper, lower and digits.
const keyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// KeyLength is the exact length of every public file key.
const KeyLength = 16

// NewKey generates a cryptographically random 16-character URL-safe key
// drawn from A-Z, a-z and 0-9 (62^16 ≈ 4.7e28 combinations).
func NewKey() (string, error) {
	out := make([]byte, KeyLength)
	max := big.NewInt(int64(len(keyAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = keyAlphabet[n.Int64()]
	}
	return string(out), nil
}

// IsValidKey reports whether s is a well-formed file key.
func IsValidKey(s string) bool {
	if len(s) != KeyLength {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}
