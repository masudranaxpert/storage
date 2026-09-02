package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os/exec"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"stream/pkg/cluster/pb"
	"stream/pkg/media"
	"stream/pkg/telemetry"
)

var (
	errNodeDisabled      = errors.New("node disabled on coordinator")
	errCoordinatorNoGRPC = errors.New("coordinator has no gRPC control plane")
)

// ProgressReporter streams live job status back to the coordinator. It is
// implemented per transport: over the gRPC mesh the report rides the
// persistent stream; in legacy HTTP mode it POSTs to the job endpoint.
type ProgressReporter func(statusStr, speed string, pct float64, errMsg string, cmaf *media.CMAFPackage)

// runControlPlaneLoop owns the agent's coordinator connection for its whole
// lifetime: it keeps a persistent bidirectional gRPC stream open, reconnects
// on drops, and falls back to legacy HTTP heartbeats when the coordinator does
// not expose a gRPC control plane (old coordinator version).
func (a *Agent) runControlPlaneLoop(ctx context.Context) {
	everConnected := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := a.runMeshConnection(ctx)
		if ctx.Err() != nil {
			return
		}

		if errors.Is(err, errCoordinatorNoGRPC) && !everConnected {
			fmt.Printf("[Agent %s] Coordinator at %s has no gRPC control plane - falling back to HTTP heartbeats\n",
				a.NodeID, a.GRPCTarget)
			a.runLegacyHeartbeatLoop(ctx)
			return
		}

		if errors.Is(err, errNodeDisabled) || isPermissionDeniedErr(err) {
			fmt.Printf("[Agent %s] Coordinator rejected this node (removed from cluster). Decommissioning local agent...\n", a.NodeID)
			if a.selfDecommission() {
				return // systemd is stopping the service; nothing left to do
			}
			fmt.Printf("[Agent %s] Self-stop unavailable — retrying in 30s...\n", a.NodeID)
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}

		everConnected = true
		fmt.Printf("[Agent %s] Mesh stream dropped (%v). Reconnecting in 3s...\n", a.NodeID, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// isPermissionDeniedErr reports whether the coordinator refused the stream
// because the node was decommissioned (the hub answers PermissionDenied).
func isPermissionDeniedErr(err error) bool {
	if err == nil {
		return false
	}
	if s, ok := status.FromError(err); ok {
		return s.Code() == codes.PermissionDenied
	}
	return false
}

// selfDecommission stops and disables the local stream-agent systemd unit
// after the coordinator marked this node as removed. Disable runs first so
// the decision survives our own shutdown; the stop then ends the service.
// Returns false when systemd is unavailable (fallback: keep retrying).
func (a *Agent) selfDecommission() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	_ = exec.Command("systemctl", "disable", "stream-agent").Run()
	_ = exec.Command("sh", "-c", "sleep 1 && systemctl stop stream-agent").Start()
	return true
}

// runMeshConnection opens one bidirectional stream lifecycle: sends the
// initial heartbeat, streams telemetry on the interval, and processes every
// command the coordinator pushes down. Returns when the stream breaks.
func (a *Agent) runMeshConnection(ctx context.Context) error {
	conn, err := grpc.NewClient(a.GRPCTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return fmt.Errorf("dial coordinator: %w", err)
	}
	defer conn.Close()

	mctx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+a.Secret)
	stream, err := pb.NewMeshClient(conn).NodeChannel(mctx)
	if err != nil {
		if isUnavailableErr(err) {
			return errCoordinatorNoGRPC
		}
		return err
	}

	// Only one goroutine may call Send at a time; the heartbeat ticker and job
	// progress reports share this mutex.
	var sendMu sync.Mutex
	sendMsg := func(m *pb.NodeMessage) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(m)
	}

	if err := a.sendHeartbeatOnStream(sendMsg); err != nil {
		if isUnavailableErr(err) {
			return errCoordinatorNoGRPC
		}
		return err
	}
	fmt.Printf("[Agent %s] Connected to coordinator control plane at %s (persistent gRPC stream)\n",
		a.NodeID, a.GRPCTarget)

	recvErr := make(chan error, 1)
	go func() {
		recvErr <- a.receiveLoop(stream, sendMsg)
	}()

	// SeaweedFS-style pulse: interval plus up to 10% jitter so a large fleet
	// of nodes never heartbeats in perfect lockstep (thundering herd).
	jittered := a.Interval + time.Duration(rand.Float64()*0.1*float64(a.Interval))
	ticker := time.NewTicker(jittered)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return ctx.Err()
		case err := <-recvErr:
			return err
		case <-ticker.C:
			if err := a.sendHeartbeatOnStream(sendMsg); err != nil {
				return err
			}
		}
	}
}

// streamReporter builds a ProgressReporter that rides the live mesh stream.
func streamReporter(sendMsg func(*pb.NodeMessage) error, jobID string) ProgressReporter {
	return func(statusStr, speed string, pct float64, errMsg string, cmaf *media.CMAFPackage) {
		var cmafJSON []byte
		if cmaf != nil {
			cmafJSON, _ = json.Marshal(cmaf)
		}
		_ = sendMsg(&pb.NodeMessage{
			Kind: &pb.NodeMessage_Progress{
				Progress: &pb.JobProgressUpdate{
					JobId:           jobID,
					Status:          statusStr,
					ProgressPercent: pct,
					DownloadSpeed:   speed,
					ErrorMsg:        errMsg,
					CmafJson:        cmafJSON,
				},
			},
		})
	}
}

// receiveLoop processes coordinator-pushed commands until the stream breaks.
func (a *Agent) receiveLoop(stream pb.Mesh_NodeChannelClient, sendMsg func(*pb.NodeMessage) error) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}

		switch m := msg.Kind.(type) {
		case *pb.CoordinatorMessage_Ack:
			ack := m.Ack
			if ack != nil && !ack.NodeEnabled {
				return errNodeDisabled
			}
			if ack != nil && ack.UpdateAvailable && ack.DownloadUrl != "" {
				a.triggerSelfUpgradeAsync(ack.DownloadUrl, ack.LatestVersion)
			}

		case *pb.CoordinatorMessage_RunJob:
			job := m.RunJob
			if job == nil || job.SourceUrl == "" {
				continue
			}
			fmt.Printf("[Agent %s] Received ingest job '%s' over mesh stream\n", a.NodeID, job.JobId)
			go a.RunIngestJob(job.JobId, job.SourceUrl, streamReporter(sendMsg, job.JobId))

		case *pb.CoordinatorMessage_InstallTools:
			a.ensureToolsAsync()
		}
	}
}

// sendHeartbeatOnStream collects telemetry and sends it as one stream message.
func (a *Agent) sendHeartbeatOnStream(send func(*pb.NodeMessage) error) error {
	m, err := telemetry.Collect(a.NodeID)
	if err != nil {
		return fmt.Errorf("telemetry collection failed: %w", err)
	}
	m.MediaPath = a.MediaDir
	m.Capabilities.AgentPort = a.ListenPort // real bind port, not the telemetry default
	if a.AdvertiseAddr != "" {
		m.IPs = PreferAgentAddrs(append([]string{a.AdvertiseAddr}, m.IPs...))
	} else {
		m.IPs = PreferAgentAddrs(m.IPs)
	}
	return send(&pb.NodeMessage{Kind: &pb.NodeMessage_Heartbeat{Heartbeat: metricsToProto(*m)}})
}

// runLegacyHeartbeatLoop is the pre-gRPC transport: periodic HTTP heartbeats.
// It re-probes the gRPC control plane periodically and switches back to the
// persistent stream as soon as it becomes available, so a coordinator upgrade
// is picked up without an agent restart.
func (a *Agent) runLegacyHeartbeatLoop(ctx context.Context) {
	if _, err := a.SendHeartbeat(); err != nil {
		fmt.Printf("[Agent %s] Warning: initial HTTP heartbeat failed: %v\n", a.NodeID, err)
	} else {
		fmt.Printf("[Agent %s] Connected to coordinator at %s (legacy HTTP heartbeats)\n",
			a.NodeID, a.CoordinatorURL)
	}

	ticker := time.NewTicker(a.Interval)
	probe := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	defer probe.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := a.SendHeartbeat(); err != nil {
				if strings.Contains(err.Error(), "(status 410)") {
					fmt.Printf("[Agent %s] Coordinator removed this node. Decommissioning local agent...\n", a.NodeID)
					if a.selfDecommission() {
						return
					}
				}
				fmt.Printf("[Agent %s] Heartbeat failed: %v\n", a.NodeID, err)
			}
		case <-probe.C:
			if a.probeMeshOnce(ctx) {
				fmt.Printf("[Agent %s] gRPC control plane detected - upgrading to persistent stream\n", a.NodeID)
				a.runControlPlaneLoop(ctx)
				return
			}
		}
	}
}

// probeMeshOnce checks whether the coordinator exposes the gRPC control plane.
func (a *Agent) probeMeshOnce(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(a.GRPCTarget, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false
	}
	defer conn.Close()

	mctx := metadata.AppendToOutgoingContext(probeCtx, "authorization", "Bearer "+a.Secret)
	stream, err := pb.NewMeshClient(conn).NodeChannel(mctx)
	if err != nil {
		return false
	}
	if err := a.sendHeartbeatOnStream(func(m *pb.NodeMessage) error { return stream.Send(m) }); err != nil {
		return false
	}
	// Wait briefly: any response frame (ack or a non-Unavailable error) proves
	// a live gRPC service.
	_, err = stream.Recv()
	return err == nil || !isUnavailableErr(err)
}

func isUnavailableErr(err error) bool {
	s, ok := status.FromError(err)
	if !ok {
		return false
	}
	return s.Code() == codes.Unavailable || s.Code() == codes.Unimplemented
}
