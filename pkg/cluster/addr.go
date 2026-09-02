package cluster

import (
	"net"
	"strings"
)

// PreferAgentAddrs orders dial targets SeaweedFS-style: explicit/public first,
// then private LAN, never loopback/link-local. Tailscale CGNAT (100.64/10) is
// deprioritized so public/VPC IPs win for node-to-node transfers.
func PreferAgentAddrs(ips []string) []string {
	if len(ips) == 0 {
		return ips
	}
	type scored struct {
		addr  string
		score int
	}
	seen := make(map[string]bool)
	var list []scored
	for _, raw := range ips {
		addr := strings.TrimSpace(raw)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		list = append(list, scored{addr: addr, score: agentAddrScore(addr)})
	}
	// Stable insertion sort by score ascending (lower = better).
	for i := 1; i < len(list); i++ {
		j := i
		for j > 0 && list[j].score < list[j-1].score {
			list[j], list[j-1] = list[j-1], list[j]
			j--
		}
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s.score >= 100 {
			continue // unusable
		}
		out = append(out, s.addr)
	}
	return out
}

// PreferAgentAddr returns the best single dial target, or "".
func PreferAgentAddr(ips []string) string {
	ordered := PreferAgentAddrs(ips)
	if len(ordered) == 0 {
		return ""
	}
	return ordered[0]
}

func agentAddrScore(addr string) int {
	// Hostnames (docker service names, DNS) are first-class reachable targets.
	if ip := net.ParseIP(addr); ip == nil {
		if strings.Contains(addr, ".") || !strings.Contains(addr, ":") {
			return 0
		}
		return 50
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return 100
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return 100
	}
	if v4 := ip.To4(); v4 != nil {
		// Tailscale / CGNAT shared range — last resort for mesh transfers.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return 80
		}
		// Docker / typical bridge
		if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
			return 40
		}
		if v4[0] == 10 {
			return 30
		}
		if v4[0] == 192 && v4[1] == 168 {
			return 30
		}
		// Public unicast — preferred for SeaweedFS-style VPS peering.
		return 10
	}
	return 20
}
