package agent

import (
	"context"
	"strings"
	"testing"

	"stream/internal/model"
)

func TestTransferFolderRcloneS3_StrictRcloneRequirement(t *testing.T) {
	a := &Agent{NodeID: "test-node"}
	ctx := context.Background()

	// Clear PATH or test environment where rclone binary isn't found
	t.Setenv("PATH", "/empty/nonexistent/bin")

	cfg := model.S3Config{
		Name:      "test-bkt",
		Endpoint:  "https://r2.cloudflarestorage.com",
		AccessKey: "test-access",
		SecretKey: "test-secret",
	}

	err := a.transferFolderRcloneS3(ctx, "test-key", t.TempDir(), cfg, "test-prefix", 1024, nil)
	if err == nil {
		t.Fatal("expected error when rclone is not installed, got nil")
	}

	if !strings.Contains(err.Error(), "rclone is required") {
		t.Fatalf("expected error mentioning 'rclone is required', got: %v", err)
	}
}

func TestTransferFolder_StrictS3NoFallback(t *testing.T) {
	a := &Agent{NodeID: "test-node"}
	ctx := context.Background()

	t.Setenv("PATH", "/empty/nonexistent/bin")

	target := TransferTarget{
		NodeID: "bucket:test-bkt",
		S3: &model.S3Config{
			Name:      "test-bkt",
			Endpoint:  "https://r2.cloudflarestorage.com",
			AccessKey: "test-access",
			SecretKey: "test-secret",
		},
	}

	err := a.transferFolder(ctx, "test-key", t.TempDir(), &target, nil)
	if err == nil {
		t.Fatal("expected error when rclone is missing for S3 transfer, got nil")
	}

	if !strings.Contains(err.Error(), "rclone is required") {
		t.Fatalf("expected strict rclone error without fallback, got: %v", err)
	}
}
