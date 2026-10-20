package cluster

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/disk"

	"stream/pkg/media"
	"stream/pkg/telemetry"
)

// Agent runs on worker VPS nodes. It gathers telemetry, keeps a persistent
// control-plane connection to the coordinator, and serves byte-range HTTP
// video streams directly to viewers.
type Agent struct {
	NodeID         string
	CoordinatorURL string
	Interval       time.Duration
	ListenPort     int
	MediaDir       string
	ScratchDir     string // local workspace for decoupled process-then-transfer jobs
	GRPCTarget     string // host:port of the coordinator gRPC control plane
	Secret         string // shared cluster secret for gRPC stream auth
	AdvertiseAddr  string // SeaweedFS-style: reachable host/IP other nodes must dial
	Quota          *QuotaGuard
	client         *http.Client
	transferClient *http.Client // long-timeout client for node-to-node folder pushes
	httpServer     *http.Server

	corsMu      sync.RWMutex
	corsOrigins string

	rootMu     sync.RWMutex
	extraRoots map[string]bool // additional media roots served by the range server

	relocateMu    sync.Mutex
	relocateTasks map[string]context.CancelFunc
}

func (a *Agent) SetCORSOrigins(origins string) {
	a.corsMu.Lock()
	defer a.corsMu.Unlock()
	a.corsOrigins = strings.TrimSpace(origins)
}

func (a *Agent) GetCORSOrigins() string {
	a.corsMu.RLock()
	defer a.corsMu.RUnlock()
	if a.corsOrigins == "" {
		return "*"
	}
	return a.corsOrigins
}

// TransferTarget names the final home of a decoupled file job: the node
// that owns the storage block, the block path, its agent HTTP address, or remote S3.
type TransferTarget struct {
	NodeID string    `json:"node_id"`
	Dir    string    `json:"dir"`
	Addr   string    `json:"addr"`
	S3     *S3Config `json:"s3,omitempty"`
}

// registerRoot makes an additional directory streamable through the
// byte-range media server (used by tier-placed file jobs and received
// transfers). The set is persisted so placements survive agent restarts.
func (a *Agent) registerRoot(dir string) {
	if dir == "" || dir == a.MediaDir {
		return
	}
	a.rootMu.Lock()
	if a.extraRoots == nil {
		a.extraRoots = make(map[string]bool)
	}
	if a.extraRoots[dir] {
		a.rootMu.Unlock()
		return
	}
	a.extraRoots[dir] = true
	a.rootMu.Unlock()
	a.saveRoots()
}

// rootsFile is the on-disk record of extra streamable roots. It lives next
// to the media dir so it follows the same mount.
func (a *Agent) rootsFile() string {
	return filepath.Join(filepath.Dir(a.MediaDir), "roots.json")
}

// saveRoots writes the current extra-root set to disk (best effort).
func (a *Agent) saveRoots() {
	a.rootMu.RLock()
	dirs := make([]string, 0, len(a.extraRoots))
	for d := range a.extraRoots {
		dirs = append(dirs, d)
	}
	a.rootMu.RUnlock()
	sort.Strings(dirs)
	data, _ := json.MarshalIndent(dirs, "", "  ")
	_ = os.WriteFile(a.rootsFile(), data, 0644)
}

// loadRoots restores extra roots persisted by a previous run, so files
// placed on tier blocks remain streamable after a restart or upgrade.
func (a *Agent) loadRoots() {
	data, err := os.ReadFile(a.rootsFile())
	if err != nil {
		return
	}
	var dirs []string
	if json.Unmarshal(data, &dirs) != nil {
		return
	}
	a.rootMu.Lock()
	if a.extraRoots == nil {
		a.extraRoots = make(map[string]bool)
	}
	for _, d := range dirs {
		if d != "" && d != a.MediaDir {
			a.extraRoots[d] = true
		}
	}
	a.rootMu.Unlock()
}

// resolveTargetDir maps a storage directory or disk mount (e.g. /mnt/hdd or /data)
// into the dedicated media directory (e.g. /mnt/hdd/stream/media) so media files
// never pollute the root of a partition.
func (a *Agent) resolveTargetDir(dir string) string {
	if dir == "" || dir == "." || dir == "/" || strings.Contains(dir, "..") {
		return a.MediaDir
	}
	dir = filepath.Clean(dir)
	slashDir := filepath.ToSlash(dir)
	if slashDir == "/" || slashDir == "." {
		return a.MediaDir
	}
	if strings.HasSuffix(slashDir, "/stream/media") || strings.HasSuffix(slashDir, "/media") {
		return dir
	}
	return filepath.Join(dir, "stream", "media")
}

// ResolveTargetDir exports resolveTargetDir for testing and external callers.
func (a *Agent) ResolveTargetDir(dir string) string {
	return a.resolveTargetDir(dir)
}

// resolveMediaPath maps a request sub-path onto the primary media dir first,
// then any registered block roots. If the path targets a media directory,
// it automatically serves the primary .mp4 video file inside it. Returns "" when nothing matches.
func (a *Agent) resolveMediaPath(cleanSubPath string) string {
	if strings.Contains(cleanSubPath, "..") {
		return ""
	}
	a.rootMu.RLock()
	roots := make([]string, 0, len(a.extraRoots)*2+2)
	roots = append(roots, a.MediaDir)
	for dir := range a.extraRoots {
		roots = append(roots, dir)
		resolved := a.resolveTargetDir(dir)
		if resolved != dir {
			roots = append(roots, resolved)
		}
	}
	a.rootMu.RUnlock()

	for _, root := range roots {
		candidate := filepath.Join(root, cleanSubPath)
		if stat, err := os.Stat(candidate); err == nil {
			if !stat.IsDir() {
				return candidate
			}
			// Folder requested: auto-locate primary video (master.m3u8, .mp4, or .mkv)
			entries, err := os.ReadDir(candidate)
			if err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".mp4") {
						return filepath.Join(candidate, e.Name())
					}
				}
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".mkv") {
						return filepath.Join(candidate, e.Name())
					}
				}
			}
		}
	}
	return ""
}

// autoSelectBestMediaDir returns the requested directory as-is unless it is a
// default path, in which case it picks the mounted partition with the most free
// space and symlinks /var/lib/stream/media to it. Linux-only: on Windows/macOS,
// gopsutil returns drive-relative mountpoints like "C:" which join into a path
// relative to the working directory, silently creating stray media folders.
func autoSelectBestMediaDir(requestedDir string) string {
	if runtime.GOOS != "linux" {
		return requestedDir
	}
	if requestedDir != "/var/lib/stream/media" {
		return requestedDir
	}

	partitions, err := disk.Partitions(false)
	if err != nil || len(partitions) == 0 {
		return requestedDir
	}

	bestMount := ""
	var maxFree uint64

	for _, p := range partitions {
		if strings.HasPrefix(p.Mountpoint, "/proc") ||
			strings.HasPrefix(p.Mountpoint, "/sys") ||
			strings.HasPrefix(p.Mountpoint, "/dev") ||
			strings.HasPrefix(p.Mountpoint, "/run") ||
			strings.HasPrefix(p.Mountpoint, "/boot") {
			continue
		}
		usage, err := disk.Usage(p.Mountpoint)
		if err == nil && usage.Free > maxFree {
			maxFree = usage.Free
			bestMount = p.Mountpoint
		}
	}

	if bestMount != "" && bestMount != "/" && maxFree > 5*1024*1024*1024 {
		targetDir := filepath.Join(bestMount, "stream", "media")
		_ = os.MkdirAll(targetDir, 0755)
		if runtime.GOOS == "linux" {
			_ = os.MkdirAll("/var/lib/stream", 0755)
			if stat, err := os.Lstat("/var/lib/stream/media"); err == nil {
				if stat.Mode()&os.ModeSymlink == 0 {
					_ = os.Remove("/var/lib/stream/media")
				}
			}
			_ = os.Symlink(targetDir, "/var/lib/stream/media")
		}
		return targetDir
	}

	return requestedDir
}

// NewAgent creates a new worker agent with default port 2052 and media storage path.
func NewAgent(nodeID, coordinatorURL string, interval time.Duration, listenPort int, mediaDir, grpcTarget, secret string) *Agent {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if listenPort <= 0 {
		listenPort = 2052
	}
	if mediaDir == "" {
		mediaDir = filepath.Join("data", "media")
	}

	mediaDir = autoSelectBestMediaDir(mediaDir)

	if grpcTarget == "" {
		grpcTarget = DeriveGRPCTarget(coordinatorURL, DefaultGRPCPort)
	}

	q := NewQuotaGuard(0)
	if size, err := ScanDirSize(mediaDir); err == nil {
		q.UpdateUsedBytes(size)
	}

	return &Agent{
		NodeID:         nodeID,
		CoordinatorURL: coordinatorURL,
		Interval:       interval,
		ListenPort:     listenPort,
		MediaDir:       mediaDir,
		ScratchDir:     filepath.Join(filepath.Dir(mediaDir), "processing"),
		GRPCTarget:     grpcTarget,
		Secret:         secret,
		Quota:          q,
		extraRoots:     make(map[string]bool),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		transferClient: newTransferHTTPClient(),
	}
}

// newTransferHTTPClient builds an HTTP client tuned for bulk node→node tar
// pushes: large buffers, no gzip, long timeouts. Parallel streams (later) are
// still needed to fully fill high-RTT WAN links — this only removes local
// client-side throttling.

// SendHeartbeat collects local system telemetry and transmits it to the coordinator.
func (a *Agent) SendHeartbeat() (*NodeRecord, error) {
	metrics, err := telemetry.Collect(a.NodeID)
	if err != nil {
		return nil, fmt.Errorf("telemetry collection failed: %w", err)
	}
	metrics.MediaPath = a.MediaDir
	metrics.Capabilities.AgentPort = a.ListenPort // telemetry default is 2052; report the real bind (2053, 8880 fallback…)
	if a.AdvertiseAddr != "" {
		metrics.IPs = PreferAgentAddrs(append([]string{a.AdvertiseAddr}, metrics.IPs...))
	} else {
		metrics.IPs = PreferAgentAddrs(metrics.IPs)
	}

	data, err := json.Marshal(metrics)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metrics: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/heartbeat", a.CoordinatorURL)
	resp, err := a.client.Post(endpoint, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("coordinator connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("coordinator rejected heartbeat (status %d): %s", resp.StatusCode, string(body))
	}

	var hbResp struct {
		Status              string      `json:"status"`
		Record              *NodeRecord `json:"record"`
		LatestVersion       string      `json:"latest_version"`
		DownloadURL         string      `json:"download_url"`
		UpdateAvailable     bool        `json:"update_available"`
		InstallMissingTools bool        `json:"install_missing_tools"`
		CORSAllowedOrigins  string      `json:"cors_allowed_origins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hbResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if hbResp.CORSAllowedOrigins != "" {
		a.SetCORSOrigins(hbResp.CORSAllowedOrigins)
	}

	if hbResp.UpdateAvailable && hbResp.DownloadURL != "" {
		a.triggerSelfUpgradeAsync(hbResp.DownloadURL, hbResp.LatestVersion)
	}

	if hbResp.Record != nil {
		return hbResp.Record, nil
	}
	return &NodeRecord{Metrics: *metrics, Status: StatusOnline, LastSeen: time.Now()}, nil
}

// Start begins the control-plane connection (persistent gRPC stream with
// legacy HTTP heartbeat fallback) and starts the local Byte-Range streaming
// HTTP server.
func (a *Agent) Start(ctx context.Context) error {
	_ = os.MkdirAll(a.MediaDir, 0755)
	_ = os.MkdirAll(a.ScratchDir, 0755)
	a.loadRoots() // tier-block roots placed before this run stay streamable

	// Garbage-collect scratch folders immediately on boot (orphaned from previous runs),
	// then keep sweeping every 15 minutes.
	go func() {
		sweep := func(maxAge time.Duration) {
			if removed := media.SweepStaleScratch(a.ScratchDir, maxAge); len(removed) > 0 {
				fmt.Printf("[Agent %s] 🧹 swept %d stale scratch folder(s)\n", a.NodeID, len(removed))
			}
		}
		sweep(0) // On boot: any existing folder in scratch is an orphan from a crashed/interrupted run
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep(1 * time.Hour)
			}
		}
	}()

	fmt.Printf("[Agent %s] 🚀 Stream Agent %s active (Media: %s, Scratch: %s)\n",
		a.NodeID, telemetry.CurrentVersion, a.MediaDir, a.ScratchDir)

	go a.startStreamingServer(ctx)

	// Persistent gRPC mesh stream with HTTP heartbeat fallback.
	go a.runControlPlaneLoop(ctx)

	<-ctx.Done()
	if a.httpServer != nil {
		_ = a.httpServer.Shutdown(context.Background())
	}
	return ctx.Err()
}

func tuneLinuxTCPBuffers() {
	if runtime.GOOS != "linux" {
		return
	}
	commands := [][]string{
		{"sysctl", "-w", "net.core.default_qdisc=fq"},
		{"sysctl", "-w", "net.ipv4.tcp_congestion_control=bbr"},
		{"sysctl", "-w", "net.core.rmem_max=33554432"},
		{"sysctl", "-w", "net.core.wmem_max=33554432"},
		{"sysctl", "-w", "net.ipv4.tcp_rmem=4096 87380 33554432"},
		{"sysctl", "-w", "net.ipv4.tcp_wmem=4096 65536 33554432"},
		{"sysctl", "-w", "fs.file-max=2097152"},
	}
	for _, cmd := range commands {
		_ = exec.Command(cmd[0], cmd[1:]...).Run()
	}
}

// verifySecret validates whether an HTTP request supplies the matching cluster secret.
func (a *Agent) verifySecret(r *http.Request) bool {
	if a.Secret == "" {
		return false
	}
	provided := r.Header.Get("X-Cluster-Secret")
	if provided == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			candidate := strings.TrimPrefix(auth, "Bearer ")
			if !strings.HasPrefix(candidate, "sm_live_") {
				provided = candidate
			}
		}
	}
	return provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(a.Secret)) == 1
}
