package provision

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Request contains connection and setup parameters for remote VPS provisioning
type Request struct {
	Host           string `json:"host"`               // e.g. "203.0.113.10" or "vps.example.com"
	Port           int    `json:"port,omitempty"`     // Defaults to 22
	User           string `json:"user"`               // e.g. "root"
	Password       string `json:"password,omitempty"` // SSH password
	PrivateKey     string `json:"private_key,omitempty"`
	UseSudo        bool   `json:"use_sudo,omitempty"` // Explicit sudo elevation toggle
	NodeName       string `json:"node_name"`          // e.g. "vps-hetzner-1"
	CoordinatorURL string `json:"coordinator_url"`    // must be reachable from VPS (public/VPC IP)
	HeartbeatSec   int    `json:"heartbeat_sec,omitempty"` // Defaults to 5s
	AdvertiseAddr  string `json:"advertise_addr,omitempty"` // override; defaults to Host when Host is an IP
}

// Result holds the provisioning output and status
type Result struct {
	NodeName      string   `json:"node_name"`
	AdvertiseAddr string   `json:"advertise_addr,omitempty"`
	Status        string   `json:"status"`
	Logs          []string `json:"logs"`
}

// ProvisionVPS connects via SSH, deploys the agent binary, and registers a systemd daemon.
// Peering is SeaweedFS-style: agents advertise a reachable public/VPC IP (--advertise-addr).
func ProvisionVPS(ctx context.Context, req Request, linuxBinData []byte) (Result, error) {
	result := Result{
		NodeName: req.NodeName,
		Status:   "pending",
		Logs:     make([]string, 0),
	}

	logMsg := func(msg string) {
		t := time.Now().Format("15:04:05")
		formatted := fmt.Sprintf("[%s] %s", t, msg)
		result.Logs = append(result.Logs, formatted)
		fmt.Printf("[Provision %s] %s\n", req.NodeName, formatted)
	}

	port := req.Port
	if port <= 0 {
		port = 22
	}

	addr := req.Host
	if !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, port)
	}

	logMsg(fmt.Sprintf("Connecting to %s as user '%s'...", addr, req.User))

	var authMethods []ssh.AuthMethod
	if req.Password != "" {
		authMethods = append(authMethods, ssh.Password(req.Password))
	}
	if req.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(req.PrivateKey))
		if err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	config := &ssh.ClientConfig{
		User:            req.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		errFormatted := fmt.Sprintf("SSH connection to %s failed: %v", addr, err)
		logMsg("❌ " + errFormatted)
		return result, fmt.Errorf("%s", errFormatted)
	}
	defer client.Close()
	logMsg("SSH connection established successfully.")

	// Step 1: Ensure aria2 and ffmpeg are installed for distributed media worker tasks
	logMsg("Verifying media worker tools (aria2, ffmpeg)...")
	pkgInstallCmd := "which aria2c > /dev/null 2>&1 && which ffmpeg > /dev/null 2>&1 || (DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq aria2 ffmpeg)"
	if _, err := runRemote(client, pkgInstallCmd, req.User, req.Password, req.UseSudo); err != nil {
		logMsg("Note: media packages installation skipped or non-apt system.")
	} else {
		logMsg("Media engine tools (aria2, ffmpeg) active.")
	}

	// Step 3: Upload stream Linux binary
	if len(linuxBinData) > 0 {
		logMsg("Uploading stream worker daemon to /usr/local/bin/stream...")
		// Stop previous service or processes to release any active file execution locks
		_, _ = runRemote(client, "systemctl stop stream-agent 2>/dev/null || killall -9 stream 2>/dev/null || true", req.User, req.Password, req.UseSudo)
		if err := uploadData(client, linuxBinData, "/usr/local/bin/stream", req.User, req.Password, req.UseSudo); err != nil {
			errFormatted := fmt.Sprintf("failed to upload binary: %v", err)
			logMsg("❌ " + errFormatted)
			return result, fmt.Errorf("%s", errFormatted)
		}
		if _, err := runRemote(client, "chmod +x /usr/local/bin/stream", req.User, req.Password, req.UseSudo); err != nil {
			errFormatted := fmt.Sprintf("failed to chmod binary: %v", err)
			logMsg("❌ " + errFormatted)
			return result, fmt.Errorf("%s", errFormatted)
		}
		logMsg("Binary deployed to /usr/local/bin/stream.")
	}

	// Step 3: Configure Systemd Service for persistent 24/7 background agent
	resolvedCoordinator := ResolveCoordinatorURL(req.CoordinatorURL)
	logMsg(fmt.Sprintf("Coordinator mesh target resolved to: %s", resolvedCoordinator))

	advertise := strings.TrimSpace(req.AdvertiseAddr)
	if advertise == "" {
		hostOnly := strings.Split(req.Host, ":")[0]
		if net.ParseIP(hostOnly) != nil {
			advertise = hostOnly
		}
	}
	result.AdvertiseAddr = advertise
	if advertise != "" {
		logMsg(fmt.Sprintf("Agent will advertise reachable addr %s for node-to-node transfers", advertise))
	} else {
		logMsg("Warning: Host is not an IP — agent will auto-pick an interface IP (set advertise_addr if peering fails)")
	}

	logMsg("Configuring 24/7 systemd background service...")
	interval := req.HeartbeatSec
	if interval <= 0 {
		interval = 5
	}

	advertiseFlag := ""
	if advertise != "" {
		advertiseFlag = fmt.Sprintf(" --advertise-addr=%s", advertise)
	}

	serviceContent := fmt.Sprintf(`[Unit]
Description=Stream Distributed Storage Mesh Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/stream agent --join=%s --name=%s --interval=%ds --port=2052 --media-dir=/var/lib/stream/media%s
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
`, resolvedCoordinator, req.NodeName, interval, advertiseFlag)

	if err := uploadData(client, []byte(serviceContent), "/etc/systemd/system/stream-agent.service", req.User, req.Password, req.UseSudo); err != nil {
		errFormatted := fmt.Sprintf("failed to write systemd unit: %v", err)
		logMsg("❌ " + errFormatted)
		return result, fmt.Errorf("%s", errFormatted)
	}

	// Prepare high-capacity storage paths (/mnt/hdd, /data, /mnt/storage)
	prepStorageCmd := `mkdir -p /var/lib/stream; if [ -d "/mnt/hdd" ]; then mkdir -p /mnt/hdd/stream/media && ln -sfn /mnt/hdd/stream/media /var/lib/stream/media; elif [ -d "/data" ]; then mkdir -p /data/stream/media && ln -sfn /data/stream/media /var/lib/stream/media; elif [ -d "/mnt/storage" ]; then mkdir -p /mnt/storage/stream/media && ln -sfn /mnt/storage/stream/media /var/lib/stream/media; else mkdir -p /var/lib/stream/media; fi`
	_, _ = runRemote(client, prepStorageCmd, req.User, req.Password, req.UseSudo)

	startCmd := "systemctl daemon-reload && systemctl enable stream-agent && systemctl restart stream-agent"
	if out, err := runRemote(client, startCmd, req.User, req.Password, req.UseSudo); err != nil {
		errFormatted := fmt.Sprintf("failed to start systemd service: %v (%s)", err, out)
		logMsg("❌ " + errFormatted)
		return result, fmt.Errorf("%s", errFormatted)
	}

	logMsg("Systemd service 'stream-agent' is active and enabled on boot.")
	result.Status = "online"
	logMsg("🎉 VPS successfully joined the Stream Mesh Cluster!")

	return result, nil
}

// InstallToolsOverSSH directly connects via SSH to install aria2, ffmpeg, and upgrade the agent binary.
func InstallToolsOverSSH(ctx context.Context, req Request, tools ...string) error {
	port := req.Port
	if port <= 0 {
		port = 22
	}
	addr := req.Host
	if !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, port)
	}

	var authMethods []ssh.AuthMethod
	if req.Password != "" {
		authMethods = append(authMethods, ssh.Password(req.Password))
	}
	if req.PrivateKey != "" {
		if signer, err := ssh.ParsePrivateKey([]byte(req.PrivateKey)); err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	config := &ssh.ClientConfig{
		User:            req.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	fmt.Printf("[SSH-AutoFix %s] 📡 Connecting to %s as user '%s'...\n", req.NodeName, addr, req.User)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		fmt.Printf("[SSH-AutoFix %s] ❌ SSH connection to %s failed: %v\n", req.NodeName, addr, err)
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer client.Close()

	pkgs := "aria2 ffmpeg"
	if len(tools) > 0 && tools[0] != "" && tools[0] != "all" {
		if tools[0] == "aria2" || tools[0] == "aria2c" {
			pkgs = "aria2"
		} else if tools[0] == "ffmpeg" {
			pkgs = "ffmpeg"
		}
	}

	fmt.Printf("[SSH-AutoFix %s] 🔒 SSH connected. Installing %s packages...\n", req.NodeName, pkgs)

	// 1. Install specified tools
	installCmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq %s", pkgs)
	if out, err := runRemote(client, installCmd, req.User, req.Password, req.UseSudo); err != nil {
		fmt.Printf("[SSH-AutoFix %s] ⚠️ Tool install note: %v (%s)\n", req.NodeName, err, out)
	} else {
		fmt.Printf("[SSH-AutoFix %s] ✅ %s installed successfully.\n", req.NodeName, pkgs)
	}

	// 2. Deploy latest binary if available
	if binData, err := GetOrBuildLinuxBinary(); err == nil && len(binData) > 0 {
		fmt.Printf("[SSH-AutoFix %s] 📦 Uploading latest agent binary to /usr/local/bin/stream...\n", req.NodeName)
		_ = uploadData(client, binData, "/usr/local/bin/stream", req.User, req.Password, req.UseSudo)
		_, _ = runRemote(client, "chmod +x /usr/local/bin/stream && systemctl restart stream-agent", req.User, req.Password, req.UseSudo)
		fmt.Printf("[SSH-AutoFix %s] 🚀 Service stream-agent restarted with latest binary!\n", req.NodeName)
	} else {
		_, _ = runRemote(client, "systemctl restart stream-agent", req.User, req.Password, req.UseSudo)
	}

	return nil
}

// UninstallToolsOverSSH directly connects via SSH to uninstall aria2, ffmpeg, or all worker tools.
func UninstallToolsOverSSH(ctx context.Context, req Request, tool string) error {
	port := req.Port
	if port <= 0 {
		port = 22
	}
	addr := req.Host
	if !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, port)
	}

	var authMethods []ssh.AuthMethod
	if req.Password != "" {
		authMethods = append(authMethods, ssh.Password(req.Password))
	}
	if req.PrivateKey != "" {
		if signer, err := ssh.ParsePrivateKey([]byte(req.PrivateKey)); err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	config := &ssh.ClientConfig{
		User:            req.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	fmt.Printf("[SSH-Uninstall %s] 📡 Connecting to %s as user '%s'...\n", req.NodeName, addr, req.User)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		fmt.Printf("[SSH-Uninstall %s] ❌ SSH connection to %s failed: %v\n", req.NodeName, addr, err)
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer client.Close()

	var pkgs string
	var fileClean string
	switch tool {
	case "aria2", "aria2c":
		pkgs = "aria2"
		fileClean = "rm -f /usr/local/bin/aria2c /usr/bin/aria2c /bin/aria2c"
	case "ffmpeg":
		pkgs = "ffmpeg"
		fileClean = "rm -f /usr/local/bin/ffmpeg /usr/bin/ffmpeg /bin/ffmpeg /usr/local/bin/ffprobe /usr/bin/ffprobe /bin/ffprobe"
	default:
		pkgs = "aria2 ffmpeg"
		fileClean = "rm -f /usr/local/bin/aria2c /usr/bin/aria2c /bin/aria2c /usr/local/bin/ffmpeg /usr/bin/ffmpeg /bin/ffmpeg /usr/local/bin/ffprobe /usr/bin/ffprobe /bin/ffprobe"
	}

	fmt.Printf("[SSH-Uninstall %s] 🗑 Uninstalling %s packages...\n", req.NodeName, pkgs)
	uninstallCmd := fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq %s 2>/dev/null; DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq 2>/dev/null; %s; hash -r 2>/dev/null || true", pkgs, fileClean)
	if out, err := runRemote(client, uninstallCmd, req.User, req.Password, req.UseSudo); err != nil {
		fmt.Printf("[SSH-Uninstall %s] ⚠️ Tool uninstall note: %v (%s)\n", req.NodeName, err, out)
	} else {
		fmt.Printf("[SSH-Uninstall %s] ✅ %s uninstalled successfully.\n", req.NodeName, pkgs)
	}

	// Deploy latest agent binary if available so outdated auto-healing binaries are replaced
	if binData, err := GetOrBuildLinuxBinary(); err == nil && len(binData) > 0 {
		fmt.Printf("[SSH-Uninstall %s] 📦 Uploading updated agent binary to /usr/local/bin/stream...\n", req.NodeName)
		_ = uploadData(client, binData, "/usr/local/bin/stream", req.User, req.Password, req.UseSudo)
		_, _ = runRemote(client, "chmod +x /usr/local/bin/stream && systemctl restart stream-agent", req.User, req.Password, req.UseSudo)
		fmt.Printf("[SSH-Uninstall %s] 🚀 Service stream-agent restarted with clean binary!\n", req.NodeName)
	} else {
		// Restart stream-agent service so it refreshes capabilities & sends updated heartbeat
		_, _ = runRemote(client, "systemctl restart stream-agent", req.User, req.Password, req.UseSudo)
		fmt.Printf("[SSH-Uninstall %s] 🚀 Service stream-agent restarted to refresh telemetry\n", req.NodeName)
	}

	return nil
}

// StopServiceOverSSH stops and disables stream-agent on the remote node via SSH upon removal.
func StopServiceOverSSH(ctx context.Context, req Request) error {
	port := req.Port
	if port <= 0 {
		port = 22
	}
	addr := req.Host
	if !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, port)
	}

	var authMethods []ssh.AuthMethod
	if req.Password != "" {
		authMethods = append(authMethods, ssh.Password(req.Password))
	}
	if req.PrivateKey != "" {
		if signer, err := ssh.ParsePrivateKey([]byte(req.PrivateKey)); err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	config := &ssh.ClientConfig{
		User:            req.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return err
	}
	defer client.Close()

	// Full decommission: stop + disable the service, remove the unit file, the
	// agent binary, and ALL media data. The media root may be a symlink to the
	// best-mounted disk (e.g. /mnt/hdd/stream/media), so resolve it first and
	// delete the real target, then the symlink wrapper directory itself.
	cleanCmd := `systemctl stop stream-agent 2>/dev/null || true; ` +
		`systemctl disable stream-agent 2>/dev/null || true; ` +
		`rm -f /etc/systemd/system/stream-agent.service; ` +
		`systemctl daemon-reload 2>/dev/null || true; ` +
		`killall -9 stream 2>/dev/null || true; ` +
		`MEDIA_REAL=$(readlink -f /var/lib/stream/media 2>/dev/null); ` +
		`if [ -n "$MEDIA_REAL" ] && [ "$MEDIA_REAL" != "/" ]; then rm -rf "$MEDIA_REAL"; fi; ` +
		`rm -rf /var/lib/stream; ` +
		`rm -f /usr/local/bin/stream`
	_, _ = runRemote(client, cleanCmd, req.User, req.Password, req.UseSudo)
	return nil
}

func quoteBash(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func runRemote(client *ssh.Client, cmd, user, password string, useSudo bool) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	finalCmd := cmd
	if useSudo && user != "root" && password != "" {
		finalCmd = fmt.Sprintf("echo %s | sudo -S -p '' bash -c %s", quoteBash(password), quoteBash(cmd))
	}

	err = session.Run(finalCmd)
	if err != nil {
		return stdout.String() + stderr.String(), err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func uploadData(client *ssh.Client, data []byte, remotePath, user, password string, useSudo bool) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stderr = &stderr

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}

	// Use temporary path and atomic mv to bypass Linux ETXTBSY file execution locks
	tmpPath := fmt.Sprintf("/tmp/stream_upload_%d", time.Now().UnixNano())
	remoteDir := path.Dir(remotePath)

	var cmd string
	if !useSudo || user == "root" || password == "" {
		cmd = fmt.Sprintf("cat > %s && chmod +x %s && mkdir -p %s && mv -f %s %s",
			tmpPath, tmpPath, remoteDir, tmpPath, remotePath)
	} else {
		cmd = fmt.Sprintf("cat > %s && chmod +x %s && echo %s | sudo -S -p '' bash -c 'mkdir -p %s && mv -f %s %s && chmod 755 %s'",
			tmpPath, tmpPath, quoteBash(password), remoteDir, tmpPath, remotePath, remotePath)
	}

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("failed to start upload command: %w", err)
	}

	total := len(data)
	chunkSize := 64 * 1024
	for i := 0; i < total; i += chunkSize {
		end := i + chunkSize
		if end > total {
			end = total
		}
		if _, err := stdin.Write(data[i:end]); err != nil {
			_ = stdin.Close()
			_ = session.Wait()
			return fmt.Errorf("write chunk failed: %w (stderr: %s)", err, stderr.String())
		}
	}
	_ = stdin.Close()

	if err := session.Wait(); err != nil {
		return fmt.Errorf("remote upload failed: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// GetOrBuildLinuxBinary returns the cached or compiled stream-linux-amd64 binary bytes
func GetOrBuildLinuxBinary() ([]byte, error) {
	binPath := "stream-linux-amd64"

	// Auto cross-compile on demand
	cmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", binPath, "./cmd/stream")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		if data, readErr := os.ReadFile(binPath); readErr == nil && len(data) > 0 {
			return data, nil
		}
		return nil, fmt.Errorf("failed to build linux binary: %v (%s)", err, string(out))
	}

	return os.ReadFile(binPath)
}

// ResolveCoordinatorURL resolves localhost/loopback to a LAN/public IP the VPS can dial.
// Tailscale is intentionally not preferred — peering is public/VPC IP based.
func ResolveCoordinatorURL(rawURL string) string {
	if !strings.Contains(rawURL, "localhost") && !strings.Contains(rawURL, "127.0.0.1") && rawURL != "" {
		return rawURL
	}

	port := "8080"
	if strings.Contains(rawURL, ":") {
		parts := strings.Split(rawURL, ":")
		port = parts[len(parts)-1]
		port = strings.Trim(port, "/")
	}

	if lanIP := GetLocalLANIPv4(); lanIP != "" {
		return fmt.Sprintf("http://%s:%s", lanIP, port)
	}

	return rawURL
}

// GetLocalLANIPv4 discovers the primary local IP address of this machine.
func GetLocalLANIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		name := strings.ToLower(iface.Name)
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if strings.Contains(name, "tailscale") || strings.Contains(name, "docker") || strings.Contains(name, "veth") {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}
