package cluster

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
	"stream/pkg/download"
	"stream/pkg/media"
	"stream/pkg/telemetry"
	"stream/pkg/tools"
)

var (
	isInstallingTools bool
	isUpgrading       bool
	agentOpMu         sync.Mutex
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
	client         *http.Client
	transferClient *http.Client // long-timeout client for node-to-node folder pushes
	httpServer     *http.Server

	rootMu     sync.RWMutex
	extraRoots map[string]bool // additional media roots served by the range server
}

// TransferTarget names the final home of a decoupled file job: the node
// that owns the storage block, the block path, and its agent HTTP address.
type TransferTarget struct {
	NodeID string
	Dir    string
	Addr   string
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
	if dir == "" || dir == "." || strings.Contains(dir, "..") {
		return a.MediaDir
	}
	dir = filepath.Clean(dir)
	slashDir := filepath.ToSlash(dir)
	if strings.HasSuffix(slashDir, "/stream/media") || strings.HasSuffix(slashDir, "/media") {
		return dir
	}
	return filepath.Join(dir, "stream", "media")
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
			// Folder requested: auto-locate primary video (.mp4)
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
	if requestedDir != "" && requestedDir != "/var/lib/stream/media" && requestedDir != filepath.Join("data", "media") {
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

	return &Agent{
		NodeID:         nodeID,
		CoordinatorURL: coordinatorURL,
		Interval:       interval,
		ListenPort:     listenPort,
		MediaDir:       mediaDir,
		ScratchDir:     filepath.Join(filepath.Dir(mediaDir), "processing"),
		GRPCTarget:     grpcTarget,
		Secret:         secret,
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
func newTransferHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Client{
		Timeout: 2 * time.Hour,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     false, // one long HTTP/1.1 upload stream
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ExpectContinueTimeout: 0,
			DisableCompression:    true,
			WriteBufferSize:       1 << 20, // 1 MiB
			ReadBufferSize:        1 << 20,
			ResponseHeaderTimeout: 30 * time.Minute,
		},
	}
}

func (a *Agent) ensureToolsAsync() {
	agentOpMu.Lock()
	if isInstallingTools {
		agentOpMu.Unlock()
		return
	}
	isInstallingTools = true
	agentOpMu.Unlock()

	go func() {
		defer func() {
			agentOpMu.Lock()
			isInstallingTools = false
			agentOpMu.Unlock()
		}()

		fmt.Printf("[Agent %s] Autonomous Healing: Installing missing worker tools (aria2, ffmpeg, rclone)...\n", a.NodeID)
		cmd := exec.Command("bash", "-c", tools.InstallShell("all"))
		if err := cmd.Run(); err == nil {
			fmt.Printf("[Agent %s] Media worker tools (aria2, ffmpeg, rclone) successfully installed!\n", a.NodeID)
			_, _ = a.SendHeartbeat()
		}
	}()
}

func (a *Agent) triggerSelfUpgradeAsync(downloadPath, newVersion string) {
	agentOpMu.Lock()
	if isUpgrading {
		agentOpMu.Unlock()
		return
	}
	isUpgrading = true
	agentOpMu.Unlock()

	go func() {
		defer func() {
			agentOpMu.Lock()
			isUpgrading = false
			agentOpMu.Unlock()
		}()

		targetURL := fmt.Sprintf("%s%s", strings.TrimRight(a.CoordinatorURL, "/"), downloadPath)
		fmt.Printf("[Agent %s] Autonomous Upgrade: Downloading new version %s from %s...\n", a.NodeID, newVersion, targetURL)

		resp, err := a.client.Get(targetURL)
		if err != nil || resp.StatusCode != http.StatusOK {
			fmt.Printf("[Agent %s] Upgrade download failed: %v\n", a.NodeID, err)
			return
		}
		defer resp.Body.Close()

		binBytes, err := io.ReadAll(resp.Body)
		if err != nil || len(binBytes) < 1000 {
			fmt.Printf("[Agent %s] Invalid binary payload received\n", a.NodeID)
			return
		}

		currentExe, err := os.Executable()
		if err != nil {
			currentExe = "/usr/local/bin/stream"
		}
		tmpPath := currentExe + ".new"
		if err := os.WriteFile(tmpPath, binBytes, 0755); err != nil {
			fmt.Printf("[Agent %s] Failed to write binary update: %v\n", a.NodeID, err)
			return
		}
		_ = os.Chmod(tmpPath, 0755)

		_ = os.Rename(tmpPath, currentExe)
		fmt.Printf("[Agent %s] Binary upgraded to %s. Restarting stream-agent service...\n", a.NodeID, newVersion)

		_ = exec.Command("systemctl", "restart", "stream-agent").Start()
	}()
}

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
	}
	if err := json.NewDecoder(resp.Body).Decode(&hbResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
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
	}
	for _, cmd := range commands {
		_ = exec.Command(cmd[0], cmd[1:]...).Run()
	}
}

// startStreamingServer initializes the local Byte-Range HTTP streaming server on port 2052
func (a *Agent) startStreamingServer(ctx context.Context) {
	tuneLinuxTCPBuffers()
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"status":"ok","node_id":"%s","port":%d}`, a.NodeID, a.ListenPort)))
	})

	mux.HandleFunc("/api/install-tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		tool := r.URL.Query().Get("tool")
		go func() {
			cmd := exec.Command("bash", "-c", tools.InstallShell(tool))
			_ = cmd.Run()
			_, _ = a.SendHeartbeat()
		}()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"installing","message":"Installing tools on host in background"}`))
	})

	mux.HandleFunc("/api/uninstall-tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		tool := r.URL.Query().Get("tool")
		go func() {
			cmd := exec.Command("bash", "-c", tools.UninstallShell(tool))
			_ = cmd.Run()
			_, _ = a.SendHeartbeat()
		}()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"uninstalling","message":"Uninstalling tools on host in background"}`))
	})

	// ---- v1 File API worker endpoints (dispatched by the master) ----

	// POST /api/v1/ingest {key, source_url, filename, target_dir,
	// final_node_id, final_dir, final_addr}: download from the source, then
	// validate + remux, reporting progress back to the master. Direct mode
	// (target_dir, no final_*): everything happens on the tier block this
	// node owns. Decoupled mode (final_*): the job runs in this node's
	// scratch folder and the finished package is transferred to the block
	// owner, then the scratch copy is deleted.
	mux.HandleFunc("/api/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Key         string `json:"key"`
			SourceURL   string `json:"source_url"`
			Filename    string `json:"filename"`
			TargetDir   string `json:"target_dir"`
			FinalNodeID string `json:"final_node_id"`
			FinalDir    string `json:"final_dir"`
			FinalAddr   string `json:"final_addr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		filename := media.SanitizeFilename(req.Filename)
		if filename == "" {
			filename = "video.mp4"
		}
		targetDir := a.resolveTargetDir(req.TargetDir)

		var final *TransferTarget
		if req.FinalNodeID != "" && req.FinalNodeID != a.NodeID && req.FinalAddr != "" {
			if dir := a.resolveTargetDir(req.FinalDir); dir != "" && dir != "." && !strings.Contains(dir, "..") {
				final = &TransferTarget{NodeID: req.FinalNodeID, Dir: dir, Addr: req.FinalAddr}
			}
		}
		if final != nil {
			targetDir = a.ScratchDir // process locally, transfer afterwards
		}

		go a.RunFileJob(req.Key, req.SourceURL, filename, targetDir, final)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"accepted"}`))
	})

	// POST /api/v1/ingest-upload?key=&dir=&filename=[&scratch=1&final_node_id=
	// &final_dir=&final_addr=]: raw streamed bytes from the master (proxied
	// client upload) land either in the block folder (direct) or in scratch
	// (decoupled: remux here, transfer to the block owner afterwards).
	mux.HandleFunc("/api/v1/ingest-upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		filename := media.SanitizeFilename(r.URL.Query().Get("filename"))
		if filename == "" {
			filename = "video.mp4"
		}
		targetDir := a.resolveTargetDir(r.URL.Query().Get("dir"))

		var final *TransferTarget
		if fin := r.URL.Query().Get("final_node_id"); fin != "" && fin != a.NodeID {
			addr := r.URL.Query().Get("final_addr")
			dir := a.resolveTargetDir(r.URL.Query().Get("final_dir"))
			if addr != "" && dir != "" && dir != "." && !strings.Contains(dir, "..") {
				final = &TransferTarget{NodeID: fin, Dir: dir, Addr: addr}
			}
		}
		if final != nil {
			targetDir = a.ScratchDir
		}

		folder, err := media.PrepareMediaFolder(targetDir, key, filename)
		if err != nil {
			http.Error(w, "folder prep failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out, err := os.Create(folder.RawFilePath)
		if err != nil {
			http.Error(w, "create file failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := io.Copy(out, r.Body); err != nil {
			out.Close()
			http.Error(w, "stream interrupted: "+err.Error(), http.StatusBadRequest)
			return
		}
		out.Close()
		if final == nil {
			a.registerRoot(targetDir)
		}

		go a.RunFileJob(key, "", filename, targetDir, final)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"stored"}`))
	})

	// POST /api/v1/ingest-receive?key=&dir= (body: tar.gz folder stream):
	// storage-side half of a decoupled placement. A processing worker
	// streams the finished media folder; it is unpacked into <dir>/<key>/,
	// the block root is registered for streaming, and the manifest is
	// verified. Guarded by the shared cluster secret.
	mux.HandleFunc("/api/v1/ingest-receive", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if a.Secret != "" && r.Header.Get("X-Cluster-Secret") != a.Secret {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		if dir == "" || dir == "." || strings.HasPrefix(dir, "..") || strings.Contains(dir, "..") {
			http.Error(w, "dir required", http.StatusBadRequest)
			return
		}

		dest := filepath.Join(dir, key)
		if err := os.RemoveAll(dest); err != nil { // stale partial from a retry
			http.Error(w, "cleanup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if rawDir != dir {
			_ = os.RemoveAll(filepath.Join(rawDir, key))
		}
		// Slow relay links: give this request its own long read deadline,
		// independent of the server-wide streaming timeout.
		if rc, ok := w.(interface {
			SetReadDeadline(time.Time) error
		}); ok {
			_ = rc.SetReadDeadline(time.Now().Add(2 * time.Hour))
		}
		files, err := media.UnpackFolder(r.Body, dest)
		if err != nil {
			_ = os.RemoveAll(dest)
			http.Error(w, "unpack failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(filepath.Join(dest, "metadata.json")); err != nil {
			_ = os.RemoveAll(dest)
			http.Error(w, "archive incomplete: metadata.json missing", http.StatusUnprocessableEntity)
			return
		}
		a.registerRoot(dir)

		fmt.Printf("[Agent %s] 📥 received file job '%s' (%d files) into block %s\n", a.NodeID, key, files, dir)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"status":"stored","files":%d}`, files)))
	})

	// Parallel Chunk Ingest Engine (SeaweedFS/MinIO multi-stream high throughput)
	mux.HandleFunc("/api/v1/ingest-init", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if a.Secret != "" && r.Header.Get("X-Cluster-Secret") != a.Secret {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		if dir == "" || dir == "." || strings.HasPrefix(dir, "..") || strings.Contains(dir, "..") {
			http.Error(w, "dir required", http.StatusBadRequest)
			return
		}
		dest := filepath.Join(dir, key)
		_ = os.RemoveAll(dest)
		if err := os.MkdirAll(dest, 0755); err != nil {
			http.Error(w, "mkdir failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"initialized"}`))
	})

var (
	activeTransferFilesMu sync.Mutex
	activeTransferFiles   = make(map[string]*os.File)
)

	mux.HandleFunc("/api/v1/ingest-file-init", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if a.Secret != "" && r.Header.Get("X-Cluster-Secret") != a.Secret {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		filename := filepath.Base(r.URL.Query().Get("file"))
		if !validKey(key) || filename == "" || filename == "." {
			http.Error(w, "invalid key or filename", http.StatusBadRequest)
			return
		}
		dir := a.resolveTargetDir(filepath.Clean(r.URL.Query().Get("dir")))
		dest := filepath.Join(dir, key, filename)
		size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)

		activeTransferFilesMu.Lock()
		if old, exists := activeTransferFiles[dest]; exists {
			_ = old.Close()
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
		if err != nil {
			activeTransferFilesMu.Unlock()
			http.Error(w, "create file failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if size > 0 {
			_ = f.Truncate(size)
		}
		activeTransferFiles[dest] = f
		activeTransferFilesMu.Unlock()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"file_ready"}`))
	})

var chunkBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 1024*1024)
		return &b
	},
}

	mux.HandleFunc("/api/v1/ingest-chunk", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if a.Secret != "" && r.Header.Get("X-Cluster-Secret") != a.Secret {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		filename := filepath.Base(r.URL.Query().Get("file"))
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if !validKey(key) || filename == "" || filename == "." {
			http.Error(w, "invalid key or filename", http.StatusBadRequest)
			return
		}
		dir := a.resolveTargetDir(filepath.Clean(r.URL.Query().Get("dir")))
		dest := filepath.Join(dir, key, filename)

		activeTransferFilesMu.Lock()
		f := activeTransferFiles[dest]
		activeTransferFilesMu.Unlock()

		if f == nil {
			var err error
			f, err = os.OpenFile(dest, os.O_WRONLY, 0644)
			if err != nil {
				http.Error(w, "open file failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer f.Close()
		}

		bufPtr := chunkBufPool.Get().(*[]byte)
		defer chunkBufPool.Put(bufPtr)
		buf := *bufPtr

		var written int64
		curOffset := offset
		for {
			nr, readErr := r.Body.Read(buf)
			if nr > 0 {
				nw, writeErr := f.WriteAt(buf[:nr], curOffset)
				if writeErr != nil {
					http.Error(w, "write error: "+writeErr.Error(), http.StatusInternalServerError)
					return
				}
				written += int64(nw)
				curOffset += int64(nw)
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				http.Error(w, "read stream error: "+readErr.Error(), http.StatusInternalServerError)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"status":"chunk_written","written":%d}`, written)))
	})

	mux.HandleFunc("/api/v1/ingest-complete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if a.Secret != "" && r.Header.Get("X-Cluster-Secret") != a.Secret {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		dest := filepath.Join(dir, key)

		activeTransferFilesMu.Lock()
		prefix := dest + string(os.PathSeparator)
		for p, f := range activeTransferFiles {
			if strings.HasPrefix(p, prefix) || p == dest {
				_ = f.Sync()
				_ = f.Close()
				delete(activeTransferFiles, p)
			}
		}
		activeTransferFilesMu.Unlock()

		if _, err := os.Stat(filepath.Join(dest, "metadata.json")); err != nil {
			_ = os.RemoveAll(dest)
			http.Error(w, "metadata.json missing", http.StatusUnprocessableEntity)
			return
		}
		a.registerRoot(dir)
		fmt.Printf("[Agent %s] 📥 Parallel ingest complete for key '%s' into block %s\n", a.NodeID, key, dir)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"stored"}`))
	})

	// POST /api/v1/ingest-delete?key=&dir=: removes a placed file folder.
	mux.HandleFunc("/api/v1/ingest-delete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := r.URL.Query().Get("key")
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		if key == "" || dir == "" || dir == "." || strings.Contains(dir, "..") {
			http.Error(w, "key and dir required", http.StatusBadRequest)
			return
		}
		target := filepath.Join(dir, key)
		_ = os.RemoveAll(target)
		if rawDir != dir {
			_ = os.RemoveAll(filepath.Join(rawDir, key))
		}
		if a.ScratchDir != "" {
			_ = os.RemoveAll(filepath.Join(a.ScratchDir, key))
		}
		w.Write([]byte(`{"status":"deleted"}`))
	})

	mediaHandler := func(w http.ResponseWriter, r *http.Request) {
		subPath := strings.TrimPrefix(r.URL.Path, "/media/")
		subPath = strings.TrimPrefix(subPath, "/stream/")
		cleanSubPath := filepath.Clean(subPath)

		if strings.Contains(cleanSubPath, "..") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		targetFilePath := a.resolveMediaPath(cleanSubPath)
		if targetFilePath == "" {
			http.NotFound(w, r)
			return
		}

			w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

		if strings.HasSuffix(targetFilePath, ".mp4") {
			w.Header().Set("Content-Type", "video/mp4")
		} else if strings.HasSuffix(targetFilePath, ".json") {
			w.Header().Set("Content-Type", "application/json")
		} else if strings.HasSuffix(targetFilePath, ".vtt") {
			w.Header().Set("Content-Type", "text/vtt")
		} else if strings.HasSuffix(targetFilePath, ".m4a") {
			w.Header().Set("Content-Type", "audio/mp4")
		} else if strings.HasSuffix(targetFilePath, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		} else if strings.HasSuffix(targetFilePath, ".m4s") {
			w.Header().Set("Content-Type", "video/iso.segment")
		}

			if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

			http.ServeFile(w, r, targetFilePath)
	}

	mux.HandleFunc("/media/", mediaHandler)
	mux.HandleFunc("/stream/", mediaHandler)

	addr := fmt.Sprintf(":%d", a.ListenPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
			fmt.Printf("[Agent %s] Port %d busy (%v), trying fallback port :8880...\n", a.NodeID, a.ListenPort, err)
		addr = ":8880"
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			fmt.Printf("[Agent %s] Warning: failed to bind streaming server: %v\n", a.NodeID, err)
			return
		}
		a.ListenPort = 8880
	}

	a.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Minute, // Allow long range stream sessions
		WriteTimeout: 30 * time.Minute,
	}

	fmt.Printf("[Agent %s] Direct Byte-Range Media Server active on %s (Media Root: %s)\n",
		a.NodeID, addr, a.MediaDir)

	if err := a.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Printf("[Agent %s] Streaming server error: %v\n", a.NodeID, err)
	}
}

// RunIngestJob downloads a source media file and packages it into CMAF on this
// node, streaming live progress back to the coordinator through the given
// reporter (persistent gRPC mesh stream, or legacy HTTP POST fallback).
func (a *Agent) RunIngestJob(jobID, srcURL string, report ProgressReporter) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	report("downloading", "Starting", 0.0, "", nil)

	filename, _, _, _ := media.ProbeRemoteHeader(srcURL)
	if filename == "" {
		filename = "video.mp4"
	}

	folder, err := media.PrepareMediaFolder(a.MediaDir, jobID, filename)
	if err != nil {
		report("failed", "0 B/s", 0.0, fmt.Sprintf("folder prep failed: %v", err), nil)
		return
	}

	dlProgress := func(pct float64, speed string) {
		report("downloading", speed, pct, "", nil)
	}

	if err := download.DownloadFile(ctx, srcURL, folder.RawFilePath, dlProgress); err != nil {
		report("failed", "0 B/s", 0.0, fmt.Sprintf("download failed: %v", err), nil)
		return
	}

	report("packaging", "Processing streamable media", 90.0, "", nil)
	meta, err := media.RemuxToStreamableMP4(folder.RawFilePath, folder.VideoFilePath, jobID, filename)
	if err != nil {
		report("failed", "0 B/s", 0.0, fmt.Sprintf("remux failed: %v", err), nil)
		return
	}
	_ = folder.SaveMetadata(meta)

	if _, err := os.Stat(folder.VideoFilePath); err == nil {
		_ = folder.CleanRaw()
	}

	cmafPkg := meta.ToCMAFPackage()
	report("completed", "Ready", 100.0, "", cmafPkg)
}

// fileProgressReporter posts file-job state transitions to the master's
// v1 progress callback over the same coordinator URL used for heartbeats.
// State strings mirror fileapi.FileState values without importing it (the
// fileapi package imports cluster for node records).
func (a *Agent) fileProgressReporter(key string) func(state string, pct float64, speed, errMsg string, cmaf *media.CMAFPackage) {
	report := func(state string, pct float64, speed, errMsg string, cmaf *media.CMAFPackage) {
		payload := map[string]interface{}{
			"state":             state,
			"progress_percent":  pct,
			"speed":             speed,
			"error":             errMsg,
		}
		if cmaf != nil {
			if data, err := json.Marshal(cmaf); err == nil {
				payload["cmaf_json"] = data
			}
		}
		body, _ := json.Marshal(payload)
		url := fmt.Sprintf("%s/api/v1/files/%s/progress", strings.TrimRight(a.CoordinatorURL, "/"), key)
		resp, err := a.client.Post(url, "application/json", bytes.NewBuffer(body))
		if err != nil {
			fmt.Printf("[Agent %s] ⚠️ file progress post failed for %s: %v\n", a.NodeID, key, err)
			return
		}
		resp.Body.Close()
	}
	return report
}

// RunFileJob is the worker half of the v1 file API: optionally download the
// source (URL mode), validate magic bytes, package to CMAF, and report every
// transition to the master. Direct mode (final == nil): everything happens
// in targetDir, a tier block this node owns. Decoupled mode (final != nil):
// the job runs in this node's scratch, the finished folder is streamed to
// the block owner (final), and the scratch copy is deleted.
func (a *Agent) RunFileJob(key, srcURL, filename, targetDir string, final *TransferTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	report := a.fileProgressReporter(key)
	if final == nil {
		a.registerRoot(targetDir)
	}

	folder, err := media.PrepareMediaFolder(targetDir, key, filename)
	if err != nil {
		report("failed", 0, "0 B/s", fmt.Sprintf("folder prep failed: %v", err), nil)
		return
	}

	if final != nil {
		defer func() {
			_ = os.RemoveAll(folder.BaseDir)
		}()
	}

	if srcURL != "" {
		report("downloading", 0, "Starting", "", nil)
		dlProgress := func(pct float64, speed string) {
			report("downloading", pct, speed, "", nil)
		}
		if err := download.DownloadFile(ctx, srcURL, folder.RawFilePath, dlProgress); err != nil {
			report("failed", 0, "0 B/s", fmt.Sprintf("download failed: %v", err), nil)
			if final != nil {
				_ = os.RemoveAll(folder.BaseDir) // reclaim scratch
			}
			return
		}
	} else if _, err := os.Stat(folder.RawFilePath); err != nil {
		report("failed", 0, "0 B/s", "neither source url nor uploaded bytes present", nil)
		if final != nil {
			_ = os.RemoveAll(folder.BaseDir)
		}
		return
	}

	// Authoritative magic-byte check: a mislabeled source never enters the library.
	if err := media.ValidateVideoFile(folder.RawFilePath); err != nil {
		_ = os.RemoveAll(folder.BaseDir)
		report("failed", 0, "0 B/s", err.Error(), nil)
		return
	}

	report("processing", 90, "Processing streamable media", "", nil)
	meta, err := media.RemuxToStreamableMP4(folder.RawFilePath, folder.VideoFilePath, key, filename)
	if err != nil {
		report("failed", 0, "0 B/s", fmt.Sprintf("remux failed: %v", err), nil)
		if final != nil {
			_ = os.RemoveAll(folder.BaseDir)
		}
		return
	}
	_ = folder.SaveMetadata(meta)

	if _, err := os.Stat(folder.VideoFilePath); err == nil {
		_ = folder.CleanRaw()
	}
	cmafPkg := meta.ToCMAFPackage()

	if final != nil {
		if err := a.transferFolder(ctx, key, folder.BaseDir, final, report); err != nil {
			_ = os.RemoveAll(folder.BaseDir)
			report("failed", 95, "0 B/s", fmt.Sprintf("transfer to %s failed: %v", final.NodeID, err), nil)
			return
		}
		_ = os.RemoveAll(folder.BaseDir) // scratch is reclaimed once stored
		report("completed", 100, "Ready", "", cmafPkg)
		fmt.Printf("[Agent %s] ✅ File job '%s' processed in scratch, stored on %s:%s (local copy cleaned)\n",
			a.NodeID, key, final.NodeID, final.Dir)
		return
	}

	report("completed", 100, "Ready", "", cmafPkg)
	fmt.Printf("[Agent %s] ✅ File job '%s' completed on tier block %s\n", a.NodeID, key, targetDir)
}

// countingReader wraps an io.Reader to track bytes read and invoke a progress callback periodically.
type countingReader struct {
	r         io.Reader
	total     int64
	read      int64
	startTime time.Time
	lastT     time.Time
	lastBytes int64
	onUpdate  func(transferred, total int64, speed string, pct float64)
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.read += int64(n)
		now := time.Now()
		if now.Sub(cr.lastT) >= 1200*time.Millisecond {
			elapsedSec := now.Sub(cr.lastT).Seconds()
			if elapsedSec > 0 {
				bytesInInterval := cr.read - cr.lastBytes
				speedBps := float64(bytesInInterval) / elapsedSec
				var pct float64
				if cr.total > 0 {
					pct = 90.0 + (float64(cr.read)/float64(cr.total))*9.0 // 90.0% to 99.0%
				} else {
					pct = 95.0
				}
				speedStr := formatTransferSpeed(speedBps)
				if cr.onUpdate != nil {
					cr.onUpdate(cr.read, cr.total, speedStr, pct)
				}
			}
			cr.lastT = now
			cr.lastBytes = cr.read
		}
	}
	return n, err
}

func formatTransferSpeed(bps float64) string {
	if bps <= 0 {
		return "0 B/s"
	}
	const unit = 1024
	if bps < unit {
		return fmt.Sprintf("%.0f B/s", bps)
	}
	div, exp := int64(unit), 0
	for n := bps / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB/s", bps/float64(div), "KMGTPE"[exp])
}

// transferFolder streams the finished media folder to the block owner. It attempts
// high-speed 8-stream parallel chunk transfer first (saturating 1Gbps WAN links),
// falling back to uncompressed tar single-stream if the remote node is an older version.
func (a *Agent) transferFolder(ctx context.Context, key, baseDir string, final *TransferTarget,
	report func(state string, pct float64, speed, errMsg string, cmaf *media.CMAFPackage)) error {
	totalBytes, _ := media.CalculateFolderSize(baseDir)
	report("transferring", 90.0, "Starting parallel transfer to "+final.NodeID, "", nil)

	// Step 1: Probe if the target node supports parallel chunk ingest
	initURL := fmt.Sprintf("%s/api/v1/ingest-init?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, nil)
	if err == nil {
		if a.Secret != "" {
			initReq.Header.Set("X-Cluster-Secret", a.Secret)
		}
		initResp, initErr := a.transferClient.Do(initReq)
		if initErr == nil {
			initResp.Body.Close()
			if initResp.StatusCode == http.StatusOK {
				// Target node supports parallel multi-stream chunk ingest!
				return a.transferFolderParallel(ctx, key, baseDir, final, totalBytes, report)
			}
		}
	}

	// Fallback to legacy single-stream tar transfer for older nodes
	return a.transferFolderClassic(ctx, key, baseDir, final, totalBytes, report)
}

func (a *Agent) transferFolderParallel(ctx context.Context, key, baseDir string, final *TransferTarget,
	totalBytes int64, report func(state string, pct float64, speed, errMsg string, cmaf *media.CMAFPackage)) error {

	startTime := time.Now()
	fmt.Printf("[Agent %s] 🚀 Starting 8-stream parallel transfer for '%s' -> %s (%s, Total: %s)\n",
		a.NodeID, key, final.NodeID, final.Addr, formatTransferSpeed(float64(totalBytes)))

	type fileEntry struct {
		relPath string
		absPath string
		size    int64
	}
	var files []fileEntry
	_ = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return nil
		}
		files = append(files, fileEntry{
			relPath: filepath.ToSlash(rel),
			absPath: path,
			size:    info.Size(),
		})
		return nil
	})

	// Initialize all target files on the remote node with preallocated disk space
	for _, f := range files {
		initFileURL := fmt.Sprintf("%s/api/v1/ingest-file-init?key=%s&dir=%s&file=%s&size=%d",
			strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir),
			url.QueryEscape(f.relPath), f.size)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, initFileURL, nil)
		if a.Secret != "" {
			req.Header.Set("X-Cluster-Secret", a.Secret)
		}
		resp, err := a.transferClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to init remote file %s: %w", f.relPath, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("receiver refused file %s: %s", f.relPath, resp.Status)
		}
	}

	type chunkTask struct {
		file   fileEntry
		offset int64
		length int64
	}

	chunkSize := int64(16 * 1024 * 1024) // 16 MB chunks for optimal TCP window ramp-up
	if totalBytes > 1024*1024*1024 {
		chunkSize = int64(32 * 1024 * 1024) // 32 MB for files > 1GB
	}
	var tasks []chunkTask
	for _, f := range files {
		if f.size == 0 {
			continue
		}
		for off := int64(0); off < f.size; off += chunkSize {
			l := chunkSize
			if off+l > f.size {
				l = f.size - off
			}
			tasks = append(tasks, chunkTask{
				file:   f,
				offset: off,
				length: l,
			})
		}
	}

	var transferred atomic.Int64
	taskChan := make(chan chunkTask, len(tasks))
	for _, t := range tasks {
		taskChan <- t
	}
	close(taskChan)

	tickerDone := make(chan struct{})
	var lastLoggedTime time.Time
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		lastBytes := int64(0)
		lastTick := time.Now()
		var smoothSpeedBps float64

		for {
			select {
			case <-tickerDone:
				return
			case now := <-ticker.C:
				cur := transferred.Load()
				elapsed := now.Sub(lastTick).Seconds()
				if elapsed > 0 {
					instantSpeed := float64(cur-lastBytes) / elapsed
					if smoothSpeedBps == 0 {
						smoothSpeedBps = instantSpeed
					} else {
						smoothSpeedBps = 0.35*instantSpeed + 0.65*smoothSpeedBps
					}
					speedStr := formatTransferSpeed(smoothSpeedBps)
					var transferPct float64
					if totalBytes > 0 {
						transferPct = (float64(cur) / float64(totalBytes)) * 100.0
					}
					overallPct := 90.0 + (transferPct * 0.09) // 90.0% to 99.0%

					detailedSpeed := fmt.Sprintf("%s • %.0f/%.0f MB (%.0f%%) [12 streams]",
						speedStr,
						float64(cur)/(1024*1024),
						float64(totalBytes)/(1024*1024),
						transferPct)

					report("transferring", overallPct, detailedSpeed, "", nil)

					if time.Since(lastLoggedTime) >= 3*time.Second {
						lastLoggedTime = time.Now()
						fmt.Printf("[Agent %s] 🔀 Transferring '%s' -> %s: %.1f%% | %s\n",
							a.NodeID, key, final.NodeID, overallPct, detailedSpeed)
					}
					lastBytes = cur
					lastTick = now
				}
			}
		}
	}()

	const numWorkers = 12
	var wg sync.WaitGroup
	var workerErr error
	var errMu sync.Mutex

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for task := range taskChan {
				if ctx.Err() != nil {
					return
				}
				errMu.Lock()
				if workerErr != nil {
					errMu.Unlock()
					return
				}
				errMu.Unlock()

				f, err := os.Open(task.file.absPath)
				if err != nil {
					errMu.Lock()
					workerErr = err
					errMu.Unlock()
					return
				}
				sr := io.NewSectionReader(f, task.offset, task.length)

				var uploadSuccess bool
				for attempt := 1; attempt <= 3; attempt++ {
					_, _ = sr.Seek(0, io.SeekStart)
					chunkURL := fmt.Sprintf("%s/api/v1/ingest-chunk?key=%s&dir=%s&file=%s&offset=%d",
						strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir),
						url.QueryEscape(task.file.relPath), task.offset)
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, chunkURL, sr)
					if err != nil {
						break
					}
					req.ContentLength = task.length
					req.Header.Set("Content-Type", "application/octet-stream")
					req.Header.Set("Content-Length", strconv.FormatInt(task.length, 10))
					if a.Secret != "" {
						req.Header.Set("X-Cluster-Secret", a.Secret)
					}
					resp, err := a.transferClient.Do(req)
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode == http.StatusOK {
							uploadSuccess = true
							break
						}
					}
					time.Sleep(time.Duration(attempt*200) * time.Millisecond)
				}
				f.Close()

				if !uploadSuccess {
					errMu.Lock()
					workerErr = fmt.Errorf("failed chunk %s at offset %d after 3 retries", task.file.relPath, task.offset)
					errMu.Unlock()
					return
				}

				transferred.Add(task.length)
			}
		}(i)
	}

	wg.Wait()
	close(tickerDone)

	if workerErr != nil {
		return fmt.Errorf("parallel transfer error: %w", workerErr)
	}

	completeURL := fmt.Sprintf("%s/api/v1/ingest-complete?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	compReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, completeURL, nil)
	if a.Secret != "" {
		compReq.Header.Set("X-Cluster-Secret", a.Secret)
	}
	compResp, err := a.transferClient.Do(compReq)
	if err != nil {
		return fmt.Errorf("complete request failed: %w", err)
	}
	compResp.Body.Close()
	if compResp.StatusCode >= 300 {
		return fmt.Errorf("remote node rejected complete with %s", compResp.Status)
	}

	elapsed := time.Since(startTime)
	avgSpeed := float64(totalBytes) / elapsed.Seconds()
	fmt.Printf("[Agent %s] ✅ Parallel transfer complete: '%s' -> %s in %s (Avg: %s) [12 parallel streams]\n",
		a.NodeID, key, final.NodeID, elapsed.Round(time.Millisecond), formatTransferSpeed(avgSpeed))
	return nil
}

func (a *Agent) transferFolderClassic(ctx context.Context, key, baseDir string, final *TransferTarget,
	totalBytes int64, report func(state string, pct float64, speed, errMsg string, cmaf *media.CMAFPackage)) error {

	fmt.Printf("[Agent %s] 🚀 Starting classic single-stream transfer for '%s' -> %s (%s, Total: %s)\n",
		a.NodeID, key, final.NodeID, final.Addr, formatTransferSpeed(float64(totalBytes)))

	pipeR, pipeW := io.Pipe()
	defer pipeR.Close()
	go func() {
		err := media.PackFolder(pipeW, baseDir)
		pipeW.CloseWithError(err)
	}()

	bufferedPipeR := bufio.NewReaderSize(pipeR, 1024*1024)
	var lastLog time.Time
	cr := &countingReader{
		r:         bufferedPipeR,
		total:     totalBytes,
		startTime: time.Now(),
		lastT:     time.Now(),
		onUpdate: func(transferred, total int64, speed string, pct float64) {
			var transferPct float64
			if total > 0 {
				transferPct = (float64(transferred) / float64(total)) * 100.0
			}
			speedWithProgress := fmt.Sprintf("%s • %.0f/%.0f MB (%.0f%%)",
				speed,
				float64(transferred)/(1024*1024),
				float64(total)/(1024*1024),
				transferPct,
			)
			report("transferring", pct, speedWithProgress, "", nil)
			if time.Since(lastLog) >= 2*time.Second {
				lastLog = time.Now()
				fmt.Printf("[Agent %s] 🔀 Transferring '%s' -> %s: %.1f%% | %s\n",
					a.NodeID, key, final.NodeID, pct, speedWithProgress)
			}
		},
	}

	endpoint := fmt.Sprintf("%s/api/v1/ingest-receive?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, cr)
	if err != nil {
		pipeR.Close()
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	if a.Secret != "" {
		req.Header.Set("X-Cluster-Secret", a.Secret)
	}

	resp, err := a.transferClient.Do(req)
	if err != nil {
		fmt.Printf("[Agent %s] ❌ Node transfer failed for '%s' -> %s: %v\n", a.NodeID, key, final.NodeID, err)
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		err := fmt.Errorf("receiver answered %s", resp.Status)
		fmt.Printf("[Agent %s] ❌ Node transfer rejected for '%s' -> %s: %v\n", a.NodeID, key, final.NodeID, err)
		return err
	}
	elapsed := time.Since(cr.startTime)
	avgSpeed := float64(totalBytes) / elapsed.Seconds()
	fmt.Printf("[Agent %s] ✅ Node transfer complete: '%s' -> %s in %s (Avg: %s)\n",
		a.NodeID, key, final.NodeID, elapsed.Round(time.Millisecond), formatTransferSpeed(avgSpeed))
	return nil
}

// validKey enforces the file-key charset so a hostile transfer request can
// never craft a path outside its block directory.
func validKey(key string) bool {
	if len(key) < 6 || len(key) > 64 {
		return false
	}
	for _, c := range key {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
