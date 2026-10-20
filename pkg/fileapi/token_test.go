package fileapi

import (
	"fmt"
	"testing"
	"time"
)

func TestStreamToken(t *testing.T) {
	key := "test-media-123"
	secret := "cluster-secret-key-xyz"
	now := time.Now().Unix()
	expFuture := now + 3600 // 1 hour ahead
	expPast := now - 60     // 1 min ago

	// 1. Generate valid token
	token := GenerateStreamToken(key, expFuture, secret)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// 2. Validate valid token
	if !ValidateStreamToken(key, token, fmt.Sprintf("%d", expFuture), secret) {
		t.Fatal("valid token must pass verification")
	}

	// 3. Reject expired token
	pastToken := GenerateStreamToken(key, expPast, secret)
	if ValidateStreamToken(key, pastToken, fmt.Sprintf("%d", expPast), secret) {
		t.Fatal("expired token must fail verification")
	}

	// 4. Reject tampered token or key mismatch
	if ValidateStreamToken("other-media", token, fmt.Sprintf("%d", expFuture), secret) {
		t.Fatal("tampered key must fail verification")
	}
	if ValidateStreamToken(key, "invalid-token", fmt.Sprintf("%d", expFuture), secret) {
		t.Fatal("tampered token must fail verification")
	}

	// 5. Allow when secret is empty (open access mode)
	if !ValidateStreamToken(key, "", fmt.Sprintf("%d", expFuture), "") {
		t.Fatal("empty secret should allow access")
	}
}
