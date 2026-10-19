package cluster_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stream/pkg/cluster"
)

func TestQuotaGuardBasics(t *testing.T) {
	q := cluster.NewQuotaGuard(1000)

	if q.AllocatedMaxBytes() != 1000 {
		t.Fatalf("expected 1000, got %d", q.AllocatedMaxBytes())
	}
	if q.AvailableBytes() != 1000 {
		t.Fatalf("expected 1000 available, got %d", q.AvailableBytes())
	}

	// Successful reservation
	if err := q.Reserve(400); err != nil {
		t.Fatalf("unexpected reservation error: %v", err)
	}
	if q.AvailableBytes() != 600 {
		t.Fatalf("expected 600 available, got %d", q.AvailableBytes())
	}

	// Commit reservation
	q.Commit(400)
	if q.UsedBytes() != 400 {
		t.Fatalf("expected 400 used, got %d", q.UsedBytes())
	}
	if q.AvailableBytes() != 600 {
		t.Fatalf("expected 600 available, got %d", q.AvailableBytes())
	}

	// Exceeding reservation
	err := q.Reserve(700)
	if !errors.Is(err, cluster.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}

	// Reclaim
	q.Reclaim(100)
	if q.UsedBytes() != 300 {
		t.Fatalf("expected 300 used, got %d", q.UsedBytes())
	}
	if q.AvailableBytes() != 700 {
		t.Fatalf("expected 700 available, got %d", q.AvailableBytes())
	}
}

func TestQuotaGuardUnlimited(t *testing.T) {
	q := cluster.NewQuotaGuard(0)
	if err := q.Reserve(1000000000000); err != nil {
		t.Fatalf("unlimited quota should allow any reservation, got %v", err)
	}
}

func TestScanDirSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "quotatest-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	f1 := filepath.Join(tmpDir, "file1.bin")
	f2 := filepath.Join(tmpDir, "file2.bin")
	_ = os.WriteFile(f1, []byte("hello"), 0644)
	_ = os.WriteFile(f2, []byte("world!"), 0644)

	size, err := cluster.ScanDirSize(tmpDir)
	if err != nil {
		t.Fatalf("ScanDirSize failed: %v", err)
	}
	if size != 11 {
		t.Fatalf("expected size 11, got %d", size)
	}
}
