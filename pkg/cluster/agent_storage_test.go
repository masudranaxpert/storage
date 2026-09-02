package cluster

import (
	"path/filepath"
	"testing"
	"time"
)

func TestResolveTargetDir(t *testing.T) {
	agent := NewAgent("test-node", "http://127.0.0.1:8080", 5*time.Second, 2052, filepath.Join("data", "media"), "", "")

	tests := []struct {
		input    string
		expected string
	}{
		{"", filepath.Join("data", "media")},
		{".", filepath.Join("data", "media")},
		{"/mnt/hdd", filepath.Join("/mnt/hdd", "stream", "media")},
		{"/data", filepath.Join("/data", "stream", "media")},
		{"/mnt/hdd/stream/media", filepath.Join("/mnt/hdd", "stream", "media")},
		{"/var/lib/stream/media", filepath.Join("/var/lib/stream", "media")},
		{filepath.Join("data", "media"), filepath.Join("data", "media")},
	}

	for _, tc := range tests {
		got := agent.resolveTargetDir(tc.input)
		if filepath.Clean(got) != filepath.Clean(tc.expected) {
			t.Errorf("resolveTargetDir(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}
