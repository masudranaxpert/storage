package cluster

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"stream/pkg/cluster/pb"
	"stream/pkg/telemetry"
)

// DefaultGRPCPort is the standard coordinator control-plane port.
const DefaultGRPCPort = 9090

// JobProgress is a decoded job progress report from an agent.
type JobProgress struct {
	JobID    string
	Status   string
	Percent  float64
	Speed    string
	ErrorMsg string
	CMAFJSON []byte
}

// GRPCHub is the coordinator-side gRPC control plane. It keeps one persistent
// bidirectional stream per agent (SeaweedFS SendHeartbeat style): telemetry
// flows up on the stream, and commands (job dispatch, tool installs, upgrade
// signals) are pushed down instantly instead of waiting for the next poll.
type GRPCHub struct {
	pb.UnimplementedMeshServer

	coord  *Coordinator
	store  DataStore
	cfg    NodeConfigProvider
	secret string

	mu      sync.RWMutex
	streams map[string]chan *pb.CoordinatorMessage

	// OnJobProgress is set by the caller (web server) to route live job
	// progress reports arriving over gRPC into the ingest queue.
	OnJobProgress func(p JobProgress)
}

// NodeConfigProvider optionally supplies per-node admin state (enabled flag)
// so the hub can reject decommissioned nodes without importing the db package.
type NodeConfigProvider interface {
	IsNodeEnabled(nodeID string) (enabled bool, known bool)
}

// NewGRPCHub creates the gRPC control-plane hub.
func NewGRPCHub(coord *Coordinator, store DataStore, secret string) *GRPCHub {
	h := &GRPCHub{
		coord:   coord,
		store:   store,
		secret:  secret,
		streams: make(map[string]chan *pb.CoordinatorMessage),
	}
	if cp, ok := store.(NodeConfigProvider); ok {
		h.cfg = cp
	}
	return h
}

// ListenAndServe starts the gRPC server on the given port.
func (h *GRPCHub) ListenAndServe(port int) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("grpc listen :%d: %w", port, err)
	}
	if h.secret == "" {
		fmt.Println("[gRPC Hub] ⚠️ WARNING: no cluster secret set — any node can join this coordinator")
	}

	srv := grpc.NewServer(grpc.StreamInterceptor(h.authInterceptor))
	pb.RegisterMeshServer(srv, h)

	fmt.Printf("[gRPC Hub] 🎯 Control plane listening on :%d (agents connect here)\n", port)
	return srv.Serve(lis)
}

// authInterceptor validates the shared cluster secret carried as
// "authorization: Bearer <secret>" metadata on every stream.
func (h *GRPCHub) authInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if h.secret != "" {
		md, ok := metadata.FromIncomingContext(ss.Context())
		if !ok {
			return status.Error(codes.Unauthenticated, "missing metadata")
		}
		vals := md.Get("authorization")
		if len(vals) == 0 || vals[0] != "Bearer "+h.secret {
			return status.Error(codes.Unauthenticated, "invalid cluster secret")
		}
	}
	return handler(srv, ss)
}

// NodeChannel implements the persistent bidirectional agent stream.
func (h *GRPCHub) NodeChannel(stream pb.Mesh_NodeChannelServer) error {
	// The first message must be a heartbeat identifying the node.
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hb := first.GetHeartbeat()
	if hb == nil || hb.NodeId == "" {
		return status.Error(codes.InvalidArgument, "first message must be a heartbeat with node_id")
	}

	nodeID := hb.NodeId

	if IsCoordinatorNode(nodeID) {
		fmt.Printf("[gRPC Hub] ⚠️ Rejected coordinator-named agent '%s' (runner is not a pool VPS)\n", nodeID)
		return status.Error(codes.PermissionDenied, "coordinator runner cannot join the VPS pool")
	}

	// Reject decommissioned / disabled nodes, same policy as the HTTP endpoint.
	if h.cfg != nil {
		if enabled, known := h.cfg.IsNodeEnabled(nodeID); known && !enabled {
			_ = stream.Send(&pb.CoordinatorMessage{
				Kind: &pb.CoordinatorMessage_Ack{
					Ack: &pb.HeartbeatAck{
						NodeId:      nodeID,
						Status:      "decommissioned",
						NodeEnabled: false,
					},
				},
			})
			fmt.Printf("[gRPC Hub] ⚠️ Rejected stream from disabled node '%s'\n", nodeID)
			return status.Error(codes.PermissionDenied, "node has been removed from cluster")
		}
	}

	out := make(chan *pb.CoordinatorMessage, 32)
	h.mu.Lock()
	if old, exists := h.streams[nodeID]; exists {
		close(old) // drop the previous stream (node reconnected)
	}
	h.streams[nodeID] = out
	h.mu.Unlock()

	// Heartbeat logging is throttled to once per minute per connection: the
	// telemetry itself still flows on every heartbeat, only the console noise
	// is reduced (connect/disconnect/errors always log immediately).
	var lastBeatLog time.Time
	var lastUpgradeLog time.Time
	firstBeat := true

	defer func() {
		h.mu.Lock()
		if cur, exists := h.streams[nodeID]; exists && cur == out {
			delete(h.streams, nodeID)
		}
		h.mu.Unlock()
		fmt.Printf("[gRPC Hub] 🔌 Node '%s' stream closed\n", nodeID)
	}()

	// Outbound pump: forwards pushed commands to the agent.
	pumpDone := make(chan struct{})
	ctx := stream.Context()
	go func() {
		defer close(pumpDone)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-out:
				if !ok {
					return
				}
				if err := stream.Send(msg); err != nil {
					return
				}
			}
		}
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}

		switch m := msg.Kind.(type) {
		case *pb.NodeMessage_Heartbeat:
			metrics := metricsFromProto(m.Heartbeat)

			// Re-verify the enabled flag on every beat: the node may have
			// been removed while this stream was already open.
			if h.cfg != nil {
				if enabled, known := h.cfg.IsNodeEnabled(nodeID); known && !enabled {
					fmt.Printf("[gRPC Hub] ⚠️ Node '%s' was disabled mid-stream — cutting stream\n", nodeID)
					_ = stream.Send(&pb.CoordinatorMessage{
						Kind: &pb.CoordinatorMessage_Ack{
							Ack: &pb.HeartbeatAck{
								NodeId:      nodeID,
								Status:      "decommissioned",
								NodeEnabled: false,
							},
						},
					})
					h.coord.RemoveNode(nodeID)
					return status.Error(codes.PermissionDenied, "node has been removed from cluster")
				}
			}

			if IsCoordinatorNode(nodeID) || IsCoordinatorNode(metrics.NodeID) || !IsPoolVPS(metrics) {
				fmt.Printf("[gRPC Hub] ⚠️ Rejected non-VPS agent '%s' (os=%s) — desktop storage is not pooled\n",
					metrics.NodeID, metrics.OS)
				return status.Error(codes.PermissionDenied, "only Linux VPS agents can join the storage pool")
			}

			record := h.coord.RegisterHeartbeat(metrics)
			if record == nil {
				return status.Error(codes.PermissionDenied, "node rejected from pool")
			}

			ver := metrics.Capabilities.Version
			if ver == "" {
				ver = "unknown"
			}

			if firstBeat {
				firstBeat = false
				fmt.Printf("[gRPC Hub] 🔗 Node '%s' (%s) connected via persistent stream\n", nodeID, ver)
			} else if time.Since(lastBeatLog) >= time.Minute {
				lastBeatLog = time.Now()
				fmt.Printf("[gRPC Hub] ❤️ Stream alive from '%s' (%s, CPU: %.1f%%, RAM: %.1f%%)\n",
					metrics.NodeID, ver, metrics.CPU.UsedPercent, metrics.Memory.UsedPercent)
			}

			needsUpgrade := metrics.Capabilities.Version != "" && metrics.Capabilities.Version != telemetry.CurrentVersion
			if needsUpgrade && time.Since(lastUpgradeLog) >= time.Minute {
				lastUpgradeLog = time.Now()
				fmt.Printf("[gRPC Hub] ⚡ Node '%s' running %s (latest: %s) — triggering autonomous upgrade\n",
					nodeID, ver, telemetry.CurrentVersion)
			}

			h.push(nodeID, &pb.CoordinatorMessage{
				Kind: &pb.CoordinatorMessage_Ack{
					Ack: &pb.HeartbeatAck{
						NodeId:              nodeID,
						Status:              string(record.Status),
						NodeEnabled:         true,
						LatestVersion:       telemetry.CurrentVersion,
						DownloadUrl:         "/download/stream-linux-amd64",
						UpdateAvailable:     needsUpgrade,
						InstallMissingTools: false,
					},
				},
			})

		case *pb.NodeMessage_Progress:
			p := m.Progress
			if h.OnJobProgress != nil && p != nil {
				h.OnJobProgress(JobProgress{
					JobID:    p.JobId,
					Status:   p.Status,
					Percent:  p.ProgressPercent,
					Speed:    p.DownloadSpeed,
					ErrorMsg: p.ErrorMsg,
					CMAFJSON: p.CmafJson,
				})
			}
		}
	}
}

// push queues a message for a connected node without blocking.
func (h *GRPCHub) push(nodeID string, msg *pb.CoordinatorMessage) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ch, exists := h.streams[nodeID]
	if !exists {
		return false
	}
	select {
	case ch <- msg:
		return true
	default:
		fmt.Printf("[gRPC Hub] ⚠️ outbound queue full for node '%s', dropping message\n", nodeID)
		return false
	}
}

// DispatchJob pushes an ingest job to a node over its live stream.
// Returns false when the node has no gRPC stream (caller falls back to HTTP).
func (h *GRPCHub) DispatchJob(nodeID, jobID, sourceURL string) bool {
	return h.push(nodeID, &pb.CoordinatorMessage{
		Kind: &pb.CoordinatorMessage_RunJob{
			RunJob: &pb.RunIngestJob{
				JobId:     jobID,
				SourceUrl: sourceURL,
			},
		},
	})
}

// TriggerInstallTools instantly asks a node to install missing worker tools.
func (h *GRPCHub) TriggerInstallTools(nodeID string) bool {
	return h.push(nodeID, &pb.CoordinatorMessage{
		Kind: &pb.CoordinatorMessage_InstallTools{
			InstallTools: &pb.InstallTools{},
		},
	})
}

// IsNodeStreamed reports whether a node currently holds a live gRPC stream.
func (h *GRPCHub) IsNodeStreamed(nodeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, exists := h.streams[nodeID]
	return exists
}

// ---------------------------------------------------------------------------
// conversions between protobuf wire types and telemetry structs
// ---------------------------------------------------------------------------

func metricsToProto(m telemetry.NodeMetrics) *pb.Heartbeat {
	hb := &pb.Heartbeat{
		NodeId:    m.NodeID,
		Hostname:  m.Hostname,
		Os:        m.OS,
		Platform:  m.Platform,
		Ips:       m.IPs,
		UptimeSec: m.UptimeSec,
		Memory: &pb.MemoryStat{
			TotalBytes:     m.Memory.TotalBytes,
			AvailableBytes: m.Memory.AvailableBytes,
			UsedBytes:      m.Memory.UsedBytes,
			UsedPercent:    m.Memory.UsedPercent,
		},
		Cpu: &pb.CPUStat{
			Cores:       int32(m.CPU.Cores),
			ModelName:   m.CPU.ModelName,
			UsedPercent: m.CPU.UsedPercent,
		},
		Capabilities: &pb.NodeCapabilities{
			HasFfmpeg: m.Capabilities.HasFFmpeg,
			HasAria2C: m.Capabilities.HasAria2c,
			HasRclone: m.Capabilities.HasRclone,
			AgentPort: int32(m.Capabilities.AgentPort),
			Version:   m.Capabilities.Version,
		},
		MediaPath:      m.MediaPath,
		ReportedAtUnix: m.ReportedAt.Unix(),
	}
	for _, d := range m.Disks {
		hb.Disks = append(hb.Disks, &pb.DiskStat{
			Path:        d.Path,
			FsType:      d.FSType,
			DiskType:    d.DiskType,
			TotalBytes:  d.TotalBytes,
			FreeBytes:   d.FreeBytes,
			UsedBytes:   d.UsedBytes,
			UsedPercent: d.UsedPercent,
		})
	}
	return hb
}

func metricsFromProto(hb *pb.Heartbeat) telemetry.NodeMetrics {
	m := telemetry.NodeMetrics{
		NodeID:     hb.NodeId,
		Hostname:   hb.Hostname,
		OS:         hb.Os,
		Platform:   hb.Platform,
		IPs:        hb.Ips,
		UptimeSec:  hb.UptimeSec,
		MediaPath:  hb.MediaPath,
		ReportedAt: time.Now().UTC(),
	}
	if hb.Memory != nil {
		m.Memory = telemetry.MemoryStat{
			TotalBytes:     hb.Memory.TotalBytes,
			AvailableBytes: hb.Memory.AvailableBytes,
			UsedBytes:      hb.Memory.UsedBytes,
			UsedPercent:    hb.Memory.UsedPercent,
		}
	}
	if hb.Cpu != nil {
		m.CPU = telemetry.CPUStat{
			Cores:       int(hb.Cpu.Cores),
			ModelName:   hb.Cpu.ModelName,
			UsedPercent: hb.Cpu.UsedPercent,
		}
	}
	if hb.Capabilities != nil {
		m.Capabilities = telemetry.NodeCapabilities{
			HasFFmpeg: hb.Capabilities.HasFfmpeg,
			HasAria2c: hb.Capabilities.HasAria2C,
			HasRclone: hb.Capabilities.HasRclone,
			AgentPort: int(hb.Capabilities.AgentPort),
			Version:   hb.Capabilities.Version,
		}
	}
	for _, d := range hb.Disks {
		m.Disks = append(m.Disks, telemetry.DiskStat{
			Path:        d.Path,
			FSType:      d.FsType,
			DiskType:    d.DiskType,
			TotalBytes:  d.TotalBytes,
			FreeBytes:   d.FreeBytes,
			UsedBytes:   d.UsedBytes,
			UsedPercent: d.UsedPercent,
		})
	}
	return m
}

// DeriveGRPCTarget converts a coordinator HTTP URL (e.g. http://host:1212)
// into a host:port gRPC target, so agents provisioned with only --join
// automatically find the control plane on its standard port.
func DeriveGRPCTarget(coordinatorURL string, grpcPort int) string {
	host := strings.TrimPrefix(coordinatorURL, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	if grpcPort <= 0 {
		grpcPort = 9090
	}
	return fmt.Sprintf("%s:%d", host, grpcPort)
}
