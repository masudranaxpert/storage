package telemetry

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
)

// FormatBytes renders a byte count in the largest fitting binary unit.
func FormatBytes(b uint64) string {
	if b == 0 {
		return "0 B"
	}
	const unit = 1024
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// DiskStat holds capacity, medium type (SSD/HDD), and mount details for a storage volume.
type DiskStat struct {
	Path        string  `json:"path"`
	FSType      string  `json:"fs_type"`
	DiskType    string  `json:"disk_type"` // "SSD", "HDD", "NVMe"
	TotalBytes  uint64  `json:"total_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

// MemoryStat holds RAM utilization metrics.
type MemoryStat struct {
	TotalBytes     uint64  `json:"total_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

// CPUStat holds processor capacity and instantaneous load.
type CPUStat struct {
	Cores       int     `json:"cores"`
	ModelName   string  `json:"model_name"`
	UsedPercent float64 `json:"used_percent"`
}

// NodeCapabilities holds information on installed streaming tools (ffmpeg, aria2c, rclone, etc.)
type NodeCapabilities struct {
	HasFFmpeg bool   `json:"has_ffmpeg"`
	HasAria2c bool   `json:"has_aria2c"`
	HasRclone bool   `json:"has_rclone"`
	AgentPort int    `json:"agent_port"`
	Version   string `json:"version"`
}

// NodeMetrics encapsulates all hardware and OS telemetry gathered from a host.
type NodeMetrics struct {
	NodeID       string           `json:"node_id"`
	Hostname     string           `json:"hostname"`
	OS           string           `json:"os"`
	Platform     string           `json:"platform"`
	IPs          []string         `json:"ips"`
	UptimeSec    uint64           `json:"uptime_sec"`
	Disks        []DiskStat       `json:"disks"`
	Memory       MemoryStat       `json:"memory"`
	CPU          CPUStat          `json:"cpu"`
	Capabilities NodeCapabilities `json:"capabilities"`
	// MediaPath is the on-disk root where this node stores media assets.
	MediaPath  string    `json:"media_path"`
	ReportedAt time.Time `json:"reported_at"`
}

// Collect gathers live system metrics from the local machine.
// CurrentVersion defines the latest build version of the Stream Mesh platform.
// Bump this whenever the wire/agent behavior changes: agents running older
// versions self-upgrade when their reported version differs from this value.
const CurrentVersion = "v1.7.0"

func Collect(nodeID string) (*NodeMetrics, error) {
	hostInfo, err := host.Info()
	if err != nil {
		return nil, err
	}

	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}

	cpuPercentages, _ := cpu.Percent(0, false)
	var cpuUsage float64
	if len(cpuPercentages) > 0 {
		cpuUsage = cpuPercentages[0]
	}

	cpuInfos, _ := cpu.Info()
	modelName := "Generic CPU"
	if len(cpuInfos) > 0 {
		modelName = cpuInfos[0].ModelName
	}

	disks := collectDisks()
	ips := collectNonLoopbackIPs()

	hasFFmpeg := false
	if p, err := exec.LookPath("ffmpeg"); err == nil && p != "" {
		hasFFmpeg = true
	}
	hasAria2c := false
	if p, err := exec.LookPath("aria2c"); err == nil && p != "" {
		hasAria2c = true
	}
	hasRclone := false
	if p, err := exec.LookPath("rclone"); err == nil && p != "" {
		hasRclone = true
	}

	return &NodeMetrics{
		NodeID:    nodeID,
		Hostname:  hostInfo.Hostname,
		OS:        hostInfo.OS,
		Platform:  hostInfo.Platform,
		IPs:       ips,
		UptimeSec: hostInfo.Uptime,
		Disks:     disks,
		Memory: MemoryStat{
			TotalBytes:     vmStat.Total,
			AvailableBytes: vmStat.Available,
			UsedBytes:      vmStat.Used,
			UsedPercent:    vmStat.UsedPercent,
		},
		CPU: CPUStat{
			Cores:       runtime.NumCPU(),
			ModelName:   modelName,
			UsedPercent: cpuUsage,
		},
		Capabilities: NodeCapabilities{
			HasFFmpeg: hasFFmpeg,
			HasAria2c: hasAria2c,
			HasRclone: hasRclone,
			AgentPort: 2052,
			Version:   CurrentVersion,
		},
		ReportedAt: time.Now().UTC(),
	}, nil
}

// collectDisks filters out virtual filesystems and returns usable partitions.
func collectDisks() []DiskStat {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil
	}

	var stats []DiskStat
	seen := make(map[string]bool)

	for _, p := range partitions {
		// Skip virtual, boot, and container filesystems.
		if strings.HasPrefix(p.Mountpoint, "/sys") ||
			strings.HasPrefix(p.Mountpoint, "/proc") ||
			strings.HasPrefix(p.Mountpoint, "/dev") ||
			strings.HasPrefix(p.Mountpoint, "/run") ||
			strings.HasPrefix(p.Mountpoint, "/boot") ||
			strings.Contains(p.Fstype, "squashfs") ||
			strings.Contains(p.Fstype, "tmpfs") ||
			strings.Contains(p.Fstype, "overlay") {
			continue
		}

		if seen[p.Mountpoint] {
			continue
		}
		seen[p.Mountpoint] = true

		usage, err := disk.Usage(p.Mountpoint)
		if err != nil || usage.Total == 0 {
			continue
		}

		dtype := DetectDiskType(p.Device, p.Mountpoint)
		stats = append(stats, DiskStat{
			Path:        p.Mountpoint,
			FSType:      p.Fstype,
			DiskType:    dtype,
			TotalBytes:  usage.Total,
			FreeBytes:   usage.Free,
			UsedBytes:   usage.Used,
			UsedPercent: usage.UsedPercent,
		})
	}
	return stats
}

// collectNonLoopbackIPs returns all active non-loopback IPv4/IPv6 addresses.
func collectNonLoopbackIPs() []string {
	var ips []string
	interfaces, err := net.Interfaces()
	if err != nil {
		return ips
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil || ip.IsLoopback() {
				continue
			}

					if ip.To4() != nil {
				ips = append(ips, ip.String())
			}
		}
	}
	return ips
}
