package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/provision"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
	"stream/pkg/web"
)

func formatBytes(bytes uint64) string {
	if bytes == 0 {
		return "0 B"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "coordinator":
		runCoordinator(os.Args[2:])
	case "agent":
		runAgent(os.Args[2:])
	case "add-vps":
		runAddVPS(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "check":
		runCheck(os.Args[2:])
	default:
		fmt.Printf("Unknown command: %s\n\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Stream - Distributed Cluster Node & Resource Manager

Usage:
  stream <command> [arguments]

Commands:
  coordinator   Start the central cluster coordinator and dashboard
  agent         Run worker agent on a VPS to join a cluster
  add-vps       Directly provision and join a remote VPS over SSH (public/VPC IP)
  status        Check cluster health and aggregated resource pool from CLI
  check         Inspect and print local hardware & storage telemetry

Examples:
  stream coordinator --port=8080
  stream add-vps --host=203.0.113.10 --user=root --pass=SECRET --name=vps-storage-1 --coordinator=http://COORD_PUBLIC_IP:8080
  stream agent --join=http://COORD_IP:8080 --name=vps-hetzner-1 --advertise-addr=203.0.113.10
  stream status --coordinator=http://127.0.0.1:8080
  stream check

Note:
  The coordinator is a system runner only — it does not join the VPS pool.
  Nodes peer SeaweedFS-style over reachable public/VPC IPs (no Tailscale required).
  Add workers with "add-vps" or the dashboard "+ Add VPS Node" button.`)
}

func runCoordinator(args []string) {
	fs := flag.NewFlagSet("coordinator", flag.ExitOnError)
	port := fs.Int("port", 8080, "HTTP port for coordinator")
	grpcPort := fs.Int("grpc-port", 9090, "gRPC control-plane port for agent mesh streams")
	secret := fs.String("secret", "", "Shared cluster secret required by agents on the gRPC control plane")
	withLocal := fs.Bool("with-local", false, "Dev only: also run an embedded worker agent on this machine (not a pool VPS)")
	_ = fs.Bool("no-local", true, "Deprecated (default): coordinator never joins the pool as a storage VPS")
	agentPort := fs.Int("agent-port", 2052, "Local worker agent streaming port (when --with-local is set)")
	mediaDir := fs.String("media-dir", filepath.Join("data", "media"), "Local media storage directory")
	scratchDir := fs.String("scratch-dir", filepath.Join("data", "processing"), "Local scratch directory for processing")
	fs.Parse(args)

	store, err := db.Open(db.GetDataDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to open BadgerDB (%v), running in-memory\n", err)
	} else {
		defer store.Close()
		fmt.Printf("📦 BadgerDB database active at: %s\n", db.GetDataDir())
	}

	var dataStore cluster.DataStore
	var tierStore storage.TierStore
	if store != nil {
		dataStore = store
		tierStore = store
	}

	coord := cluster.NewCoordinator(15*time.Second, 30*time.Second, dataStore)

	tiers := storage.NewManager(tierStore)

	hub := cluster.NewGRPCHub(coord, dataStore, *secret)
	go func() {
		if err := hub.ListenAndServe(*grpcPort); err != nil {
			fmt.Fprintf(os.Stderr, "gRPC control plane error: %v\n", err)
			os.Exit(1)
		}
	}()

	stopCh := make(chan struct{})
	go coord.StartReaper(5*time.Second, stopCh)

	var localAgentCancel context.CancelFunc
	if *withLocal {
		// Dev/debug only. Uses a non-master name so it can appear as a normal pool worker.
		host, _ := os.Hostname()
		if host == "" {
			host = "local-dev"
		}
		localNodeID := fmt.Sprintf("%s-dev-worker", host)
		joinURL := fmt.Sprintf("http://127.0.0.1:%d", *port)
		grpcTarget := fmt.Sprintf("127.0.0.1:%d", *grpcPort)

		localAgent := cluster.NewAgent(localNodeID, joinURL, 5*time.Second, *agentPort, *mediaDir, grpcTarget, *secret)
		if *scratchDir != "" {
			localAgent.ScratchDir = *scratchDir
		}

		var agentCtx context.Context
		agentCtx, localAgentCancel = context.WithCancel(context.Background())

		go func() {
			time.Sleep(300 * time.Millisecond)
			fmt.Printf("👷 Dev embedded worker active: %s on :%d (Media: %s) — prefer Add VPS for real nodes\n", localNodeID, *agentPort, *mediaDir)
			if err := localAgent.Start(agentCtx); err != nil && err != context.Canceled {
				fmt.Fprintf(os.Stderr, "Local worker agent error: %v\n", err)
			}
		}()
	} else {
		fmt.Println("🧭 Coordinator is pool-runner only (no local storage VPS). Add workers via dashboard or: stream add-vps ...")
	}

	addr := fmt.Sprintf(":%d", *port)
	staticDir := filepath.Join("web", "static")
	templateDir := filepath.Join("web", "templates")
	srv := web.NewServer(addr, coord, hub, store, tiers, staticDir, templateDir)

	fmt.Printf("====================================================\n")
	fmt.Printf("🚀 Stream Coordinator running at http://0.0.0.0:%d\n", *port)
	fmt.Printf("📊 Web Dashboard: http://localhost:%d\n", *port)
	fmt.Printf("📡 API Endpoints: /api/heartbeat, /api/nodes, /api/pool\n")
	fmt.Printf("🎯 gRPC Control Plane: :%d (agent mesh streams%s)\n",
		*grpcPort, func() string {
			if *secret == "" {
				return ", WARNING: no secret set"
			}
			return ", secret protected"
		}())
	fmt.Printf("====================================================\n")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	}()

	<-sigCh
	fmt.Println("\nShutting down coordinator...")
	close(stopCh)
	if localAgentCancel != nil {
		localAgentCancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	fmt.Println("Coordinator stopped.")
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	joinURL := fs.String("join", "http://127.0.0.1:8080", "Coordinator HTTP address")
	grpcTarget := fs.String("grpc", "", "Coordinator gRPC control-plane host:port (defaults to join host :9090)")
	secret := fs.String("secret", "", "Shared cluster secret for the gRPC control plane")
	nodeName := fs.String("name", "", "Unique Node ID/Name (defaults to hostname)")
	interval := fs.Duration("interval", 5*time.Second, "Heartbeat interval (e.g. 5s)")
	port := fs.Int("port", 2052, "Direct Byte-Range HTTP streaming port (Cloudflare compatible)")
	mediaDir := fs.String("media-dir", "", "Root directory for local media storage")
	scratchDir := fs.String("scratch-dir", "", "Scratch workspace for process-then-transfer jobs (defaults to <media-dir>/../processing)")
	advertise := fs.String("advertise-addr", "", "Reachable host/IP other nodes dial (SeaweedFS-style; e.g. public VPS IP or docker service name)")
	fs.Parse(args)

	if *nodeName == "" {
		host, err := os.Hostname()
		if err == nil && host != "" {
			*nodeName = host
		} else {
			*nodeName = fmt.Sprintf("node-%d", time.Now().Unix()%10000)
		}
	}

	fmt.Printf("Starting worker agent: Node ID = %s\n", *nodeName)
	fmt.Printf("Target coordinator: %s (Interval: %v)\n", *joinURL, *interval)
	fmt.Printf("Direct Streaming Port: :%d\n", *port)

	agent := cluster.NewAgent(*nodeName, *joinURL, *interval, *port, *mediaDir, *grpcTarget, *secret)
	if *scratchDir != "" {
		agent.ScratchDir = *scratchDir
	}
	if *advertise != "" {
		agent.AdvertiseAddr = *advertise
		fmt.Printf("Advertise address: %s (node-to-node transfers)\n", *advertise)
	}
	fmt.Printf("Control plane target: %s (persistent gRPC stream, HTTP fallback)\n", agent.GRPCTarget)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nDisconnecting agent...")
		cancel()
	}()

	if err := agent.Start(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Agent error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Agent exited.")
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	coordURL := fs.String("coordinator", "http://127.0.0.1:8080", "Coordinator address")
	fs.Parse(args)

	resp, err := http.Get(fmt.Sprintf("%s/api/pool", *coordURL))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to coordinator at %s: %v\n", *coordURL, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var summary cluster.ClusterPoolSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Invalid response: %s\n", string(body))
		os.Exit(1)
	}

	fmt.Printf("\n=== CLUSTER RESOURCE POOL SUMMARY ===\n")
	fmt.Printf("Nodes: %d Total | %d Active | %d Offline\n", summary.TotalNodes, summary.ActiveNodes, summary.OfflineNodes)
	fmt.Printf("Storage Capacity : %s Total | %s Free (%.1f%% used)\n",
		formatBytes(summary.TotalStorageBytes), formatBytes(summary.FreeStorageBytes), summary.StorageUsedPercent)
	fmt.Printf("RAM Pool         : %s Total | %s Available\n",
		formatBytes(summary.TotalRAMBytes), formatBytes(summary.AvailableRAMBytes))
	fmt.Printf("Compute Cores    : %d Cores\n", summary.TotalCPUCores)
	fmt.Printf("=====================================\n\n")

	fmt.Printf("%-20s %-10s %-12s %-15s %-18s\n", "NODE ID", "STATUS", "CPU CORES", "RAM (FREE/TOTAL)", "STORAGE (FREE/TOTAL)")
	fmt.Printf("------------------------------------------------------------------------------------\n")

	for _, n := range summary.Nodes {
		m := n.Metrics
		var totalDisk, freeDisk uint64
		for _, d := range m.Disks {
			totalDisk += d.TotalBytes
			freeDisk += d.FreeBytes
		}

		ramStr := fmt.Sprintf("%s / %s", formatBytes(m.Memory.AvailableBytes), formatBytes(m.Memory.TotalBytes))
		diskStr := fmt.Sprintf("%s / %s", formatBytes(freeDisk), formatBytes(totalDisk))

		fmt.Printf("%-20s %-10s %-12d %-15s %-18s\n",
			m.NodeID, n.Status, m.CPU.Cores, ramStr, diskStr)
	}
	fmt.Println()
}

func runCheck(args []string) {
	fmt.Println("Scanning local hardware & storage telemetry...")
	metrics, err := telemetry.Collect("local-probe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Collection error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nHost      : %s (%s, %s)\n", metrics.Hostname, metrics.OS, metrics.Platform)
	fmt.Printf("IPs       : %v\n", metrics.IPs)
	fmt.Printf("CPU       : %d Cores (%s, %.1f%% used)\n", metrics.CPU.Cores, metrics.CPU.ModelName, metrics.CPU.UsedPercent)
	fmt.Printf("RAM       : %s Available / %s Total (%.1f%% used)\n",
		formatBytes(metrics.Memory.AvailableBytes), formatBytes(metrics.Memory.TotalBytes), metrics.Memory.UsedPercent)
	fmt.Printf("\nDetected Storage Drives (%d):\n", len(metrics.Disks))
	for _, d := range metrics.Disks {
		fmt.Printf("  • %-15s [%s] %s Free of %s (%.1f%% used)\n",
			d.Path, d.FSType, formatBytes(d.FreeBytes), formatBytes(d.TotalBytes), d.UsedPercent)
	}
	fmt.Println()
}

func runAddVPS(args []string) {
	fs := flag.NewFlagSet("add-vps", flag.ExitOnError)
	host := fs.String("host", "", "VPS IP or Hostname (e.g. 203.0.113.10 or vps.example.com)")
	user := fs.String("user", "root", "SSH user (default: root)")
	pass := fs.String("pass", "", "SSH password")
	keyPath := fs.String("key", "", "SSH private key path")
	name := fs.String("name", "", "Node Alias/Name (defaults to vps-<ip>)")
	coordinator := fs.String("coordinator", "http://127.0.0.1:8080", "Coordinator URL (must be reachable from the VPS — use public/VPC IP)")
	fs.Parse(args)

	if *host == "" {
		fmt.Println("Error: --host is required (e.g. --host=203.0.113.10)")
		fs.Usage()
		os.Exit(1)
	}

	var privateKeyContent string
	if *keyPath != "" {
		if data, err := os.ReadFile(*keyPath); err == nil {
			privateKeyContent = string(data)
		}
	}

	req := provision.Request{
		Host:           *host,
		User:           *user,
		Password:       *pass,
		PrivateKey:     privateKeyContent,
		NodeName:       *name,
		CoordinatorURL: *coordinator,
	}

	binData, err := provision.GetOrBuildLinuxBinary()
	if err != nil {
		fmt.Printf("Binary error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🚀 Starting automated provisioning for %s...\n\n", *host)
	res, err := provision.ProvisionVPS(context.Background(), req, binData)
	for _, log := range res.Logs {
		fmt.Println(log)
	}
	if err != nil {
		fmt.Printf("\n❌ Provisioning failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n🎉 VPS '%s' successfully provisioned and joined the cluster!\n", res.NodeName)
	if res.AdvertiseAddr != "" {
		fmt.Printf("📡 Advertise address (node peering): %s\n", res.AdvertiseAddr)
	}
}
