package provision

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func generateTestSSHKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("failed to convert to ssh.PublicKey: %v", err)
	}
	return sshPub
}

func TestTOFUHostKeyCallback(t *testing.T) {
	tempDir := t.TempDir()
	origKnownHosts := KnownHostsFile
	KnownHostsFile = filepath.Join(tempDir, "known_hosts")
	defer func() {
		KnownHostsFile = origKnownHosts
	}()

	keyA := generateTestSSHKey(t)
	keyB := generateTestSSHKey(t)

	hostname := "203.0.113.10:22"
	var dummyAddr net.Addr = &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}

	// 1. First use: host unknown -> should trust and write to known_hosts
	cb1 := TOFUHostKeyCallback(false)
	if err := cb1(hostname, dummyAddr, keyA); err != nil {
		t.Fatalf("expected trust on first use, got: %v", err)
	}

	// 2. Second use: same key -> should pass
	cb2 := TOFUHostKeyCallback(false)
	if err := cb2(hostname, dummyAddr, keyA); err != nil {
		t.Fatalf("expected trusted host with same key to pass, got: %v", err)
	}

	// 3. Mismatch: key changed, retrust=false -> must fail
	cb3 := TOFUHostKeyCallback(false)
	errMismatch := cb3(hostname, dummyAddr, keyB)
	if errMismatch == nil {
		t.Fatal("expected error on host key mismatch with retrust=false, got nil")
	}
	if !strings.Contains(errMismatch.Error(), "host key mismatch") {
		t.Fatalf("expected 'host key mismatch' error, got: %v", errMismatch)
	}

	// 4. Retrust: key changed, retrust=true -> should update and succeed
	cb4 := TOFUHostKeyCallback(true)
	if err := cb4(hostname, dummyAddr, keyB); err != nil {
		t.Fatalf("expected retrust=true to update key and succeed, got: %v", err)
	}

	// 5. Subsequent use with keyB should now pass without retrust
	cb5 := TOFUHostKeyCallback(false)
	if err := cb5(hostname, dummyAddr, keyB); err != nil {
		t.Fatalf("expected new keyB to be trusted after retrust, got: %v", err)
	}
}
