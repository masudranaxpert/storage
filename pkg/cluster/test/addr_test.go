package cluster_test

import (
	"testing"

	"stream/pkg/cluster"
)

func TestPreferAgentAddrsPublicBeforeTailscale(t *testing.T) {
	got := cluster.PreferAgentAddrs([]string{
		"127.0.0.1",
		"100.68.160.16",
		"10.0.0.5",
		"203.0.113.10",
		"agent-1",
	})
	if len(got) < 3 {
		t.Fatalf("expected usable addrs, got %v", got)
	}
	if got[0] != "agent-1" {
		t.Fatalf("hostname/advertise should win first, got %v", got)
	}
	if got[1] != "203.0.113.10" {
		t.Fatalf("public IP should rank above private/tailscale, got %v", got)
	}
	for _, a := range got {
		if a == "127.0.0.1" {
			t.Fatal("loopback must be excluded")
		}
	}
}
