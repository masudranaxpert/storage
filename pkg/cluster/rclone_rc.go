package cluster

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"stream/pkg/tools"
)

// RcloneCoreStats models the response from rclone rc POST /core/stats.
type RcloneCoreStats struct {
	Bytes          int64   `json:"bytes"`
	TotalBytes     int64   `json:"totalBytes"`
	Speed          float64 `json:"speed"`
	ElapsedTime    float64 `json:"elapsedTime"`
	TransferTime   float64 `json:"transferTime"`
	ETA            *int64  `json:"eta"`
	Errors         int64   `json:"errors"`
	FatalError     bool    `json:"fatalError"`
	RetryError     bool    `json:"retryError"`
	Transfers      int64   `json:"transfers"`
	TotalTransfers int64   `json:"totalTransfers"`
}

// RcloneJobStatus models the response from rclone rc POST /job/status.
type RcloneJobStatus struct {
	ID        int64   `json:"id"`
	Finished  bool    `json:"finished"`
	Success   bool    `json:"success"`
	Error     string  `json:"error"`
	Duration  float64 `json:"duration"`
	StartTime string  `json:"startTime"`
	EndTime   string  `json:"endTime"`
}

// RcloneRCClient interacts with a local or remote rclone Remote Control Daemon.
type RcloneRCClient struct {
	baseURL string
	user    string
	pass    string
	client  *http.Client
}

// NewRcloneRCClient returns an HTTP client configured for rclone RC API.
func NewRcloneRCClient(baseURL, user, pass string) *RcloneRCClient {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:5572"
	}
	return &RcloneRCClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		user:    user,
		pass:    pass,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *RcloneRCClient) do(ctx context.Context, endpoint string, reqBody interface{}, respDest interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("failed to marshal rc request: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	urlStr := fmt.Sprintf("%s/%s", c.baseURL, strings.TrimPrefix(endpoint, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bodyReader)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rclone rc %s returned %d: %s", endpoint, resp.StatusCode, string(respBytes))
	}

	if respDest != nil {
		if err := json.NewDecoder(resp.Body).Decode(respDest); err != nil {
			return fmt.Errorf("failed to decode rc response: %w", err)
		}
	}
	return nil
}

// Noop tests if the rclone RC daemon is running and reachable.
func (c *RcloneRCClient) Noop(ctx context.Context) error {
	return c.do(ctx, "rc/noop", map[string]interface{}{}, nil)
}

// Obscure produces an obscured password string accepted by on-the-fly backend configurations.
func (c *RcloneRCClient) Obscure(ctx context.Context, clearText string) (string, error) {
	if clearText == "" {
		clearText = "stream"
	}
	var res struct {
		Obscured string `json:"obscured"`
	}
	if err := c.do(ctx, "core/obscure", map[string]string{"clear": clearText}, &res); err == nil && res.Obscured != "" {
		return res.Obscured, nil
	}

	// Fallback to CLI command if RC command fails
	out, err := exec.CommandContext(ctx, "rclone", "obscure", clearText).Output()
	if err != nil {
		return "", fmt.Errorf("rclone obscure failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SyncCopy initiates an asynchronous copy job using multi-stream transfers.
func (c *RcloneRCClient) SyncCopy(ctx context.Context, srcFs, dstFs string, transfers int) (int64, error) {
	if transfers <= 0 {
		transfers = 8
	}
	payload := map[string]interface{}{
		"srcFs":              srcFs,
		"dstFs":              dstFs,
		"createEmptySrcDirs": true,
		"_async":             true,
		"_config": map[string]interface{}{
			"Transfers": transfers,
			"Checkers":  transfers,
		},
	}

	var res struct {
		JobID int64 `json:"jobid"`
	}
	if err := c.do(ctx, "sync/copy", payload, &res); err != nil {
		return 0, err
	}
	return res.JobID, nil
}

// GetJobStatus checks whether an async job has completed.
func (c *RcloneRCClient) GetJobStatus(ctx context.Context, jobID int64) (*RcloneJobStatus, error) {
	var status RcloneJobStatus
	if err := c.do(ctx, "job/status", map[string]int64{"jobid": jobID}, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// GetStats returns the current real-time transfer telemetry.
func (c *RcloneRCClient) GetStats(ctx context.Context) (*RcloneCoreStats, error) {
	var stats RcloneCoreStats
	if err := c.do(ctx, "core/stats", map[string]interface{}{}, &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// StopJob terminates a running background job.
func (c *RcloneRCClient) StopJob(ctx context.Context, jobID int64) error {
	return c.do(ctx, "job/stop", map[string]int64{"jobid": jobID}, nil)
}

var (
	rcloneRCDMu sync.Mutex
)

// ensureRcloneRCD guarantees that a local rclone rcd daemon is active on 127.0.0.1:5572.
func (a *Agent) ensureRcloneRCD(ctx context.Context) (*RcloneRCClient, error) {
	rcloneRCDMu.Lock()
	defer rcloneRCDMu.Unlock()

	rcPass := a.Secret
	if rcPass == "" {
		rcPass = "stream-cluster-rc"
	}
	rcUser := "admin"
	rcAddr := "127.0.0.1:5572"

	client := NewRcloneRCClient("http://"+rcAddr, rcUser, rcPass)

	// Quick probe: daemon might already be running
	probeCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	err := client.Noop(probeCtx)
	cancel()
	if err == nil {
		return client, nil
	}

	// Verify rclone binary exists; attempt installation if running on Linux
	if _, err := exec.LookPath("rclone"); err != nil {
		if runtime.GOOS == "linux" {
			fmt.Printf("[Agent %s] 📦 Installing missing rclone binary for node-to-node transfer...\n", a.NodeID)
			installCmd := exec.CommandContext(ctx, "bash", "-c", tools.InstallRcloneShell())
			_ = installCmd.Run()
		}
		if _, err := exec.LookPath("rclone"); err != nil {
			return nil, fmt.Errorf("rclone binary not available: %w", err)
		}
	}

	// Spawn daemon: rclone rcd --rc-addr=127.0.0.1:5572 --rc-user=admin --rc-pass=xxx
	cmd := exec.Command("rclone", "rcd",
		"--rc-addr="+rcAddr,
		"--rc-user="+rcUser,
		"--rc-pass="+rcPass,
		"--rc-web-gui=false",
		"--rc-allow-remote-access=false",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to spawn rclone rcd: %w", err)
	}

	// Wait up to 5 seconds for the daemon to become ready
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		checkCtx, checkCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		if err := client.Noop(checkCtx); err == nil {
			checkCancel()
			fmt.Printf("[Agent %s] 🚀 Rclone Remote Control Daemon ready on %s\n", a.NodeID, rcAddr)
			return client, nil
		}
		checkCancel()
	}

	return nil, errors.New("rclone rcd failed to respond within 5s")
}

// transferFolderRclone copies a finished media folder to the target node using rclone RCD and WebDAV.
func (a *Agent) transferFolderRclone(ctx context.Context, key, baseDir string, final *TransferTarget,
	totalBytes int64, report ProgressReporterFunc) error {

	// Step 1: Probe and initialize destination directory on target agent
	initURL := fmt.Sprintf("%s/api/v1/ingest-init?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create ingest-init request: %w", err)
	}
	if a.Secret != "" {
		initReq.Header.Set("X-Cluster-Secret", a.Secret)
	}
	initResp, err := a.transferClient.Do(initReq)
	if err != nil {
		return fmt.Errorf("target node ingest-init unreachable: %w", err)
	}
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusOK {
		return fmt.Errorf("target node ingest-init returned HTTP %d", initResp.StatusCode)
	}

	// Step 2: Ensure local rclone rcd daemon is active
	rcClient, err := a.ensureRcloneRCD(ctx)
	if err != nil {
		return fmt.Errorf("rclone rcd daemon setup failed: %w", err)
	}

	// Step 3: Obscure password for on-the-fly rclone backend
	passClear := a.Secret
	if passClear == "" {
		passClear = "stream"
	}
	obscuredPass, err := rcClient.Obscure(ctx, passClear)
	if err != nil {
		return fmt.Errorf("rclone obscure failed: %w", err)
	}

	// Step 4: Construct WebDAV destination URL with hex-encoded target directory
	hexDir := hex.EncodeToString([]byte(final.Dir))
	targetWebdavURL := fmt.Sprintf("%s/api/v1/webdav/%s/%s",
		strings.TrimRight(final.Addr, "/"), hexDir, url.PathEscape(key))
	dstFs := fmt.Sprintf(":webdav,url='%s',user='admin',pass='%s':", targetWebdavURL, obscuredPass)
	srcFs := filepath.Clean(baseDir)

	fmt.Printf("[Agent %s] 🚀 Dispatching rclone RCD transfer for '%s' -> %s (%s, Total: %s)\n",
		a.NodeID, key, final.NodeID, final.Addr, formatTransferSpeed(float64(totalBytes)))

	// Step 5: Start copy job with 8 parallel streams
	jobID, err := rcClient.SyncCopy(ctx, srcFs, dstFs, 8)
	if err != nil {
		return fmt.Errorf("rclone sync/copy dispatch failed: %w", err)
	}

	// Step 6: Poll progress and report telemetry until completion
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			_ = rcClient.StopJob(context.Background(), jobID)
			return ctx.Err()

		case <-ticker.C:
			status, statusErr := rcClient.GetJobStatus(ctx, jobID)
			if statusErr == nil && status.Finished {
				if !status.Success {
					return fmt.Errorf("rclone transfer job %d failed: %s", jobID, status.Error)
				}
				goto transferDone
			}

			// Gather live stats for cluster dashboard
			stats, statsErr := rcClient.GetStats(ctx)
			if statsErr == nil && stats != nil {
				curBytes := stats.Bytes
				if totalBytes > 0 && curBytes > totalBytes {
					curBytes = totalBytes
				}
				var transferPct float64
				if totalBytes > 0 {
					transferPct = (float64(curBytes) / float64(totalBytes)) * 100.0
				}
				overallPct := 90.0 + (transferPct * 0.09) // Scale 90% -> 99%
				detailedSpeed := fmt.Sprintf("%s • %.0f/%.0f MB (%.0f%%) [rclone 8-stream]",
					formatTransferSpeed(stats.Speed),
					float64(curBytes)/(1024*1024),
					float64(totalBytes)/(1024*1024),
					transferPct,
				)
				report("transferring", overallPct, detailedSpeed, "", nil, map[string]interface{}{
					"stage":             "transfer",
					"stage_name":        fmt.Sprintf("Syncing to %s", final.NodeID),
					"stage_percent":     transferPct,
					"transferred_bytes": curBytes,
					"total_bytes":       totalBytes,
					"speed_bytes_sec":   int64(stats.Speed),
				})
			}
		}
	}

transferDone:
	// Step 7: Finalize placement on target node
	compURL := fmt.Sprintf("%s/api/v1/ingest-complete?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	compReq, err := http.NewRequestWithContext(ctx, http.MethodPost, compURL, nil)
	if err == nil {
		if a.Secret != "" {
			compReq.Header.Set("X-Cluster-Secret", a.Secret)
		}
		compResp, compErr := a.transferClient.Do(compReq)
		if compErr == nil {
			compResp.Body.Close()
		}
	}

	elapsed := time.Since(startTime)
	var avgSpeed float64
	if elapsed.Seconds() > 0 {
		avgSpeed = float64(totalBytes) / elapsed.Seconds()
	}
	fmt.Printf("[Agent %s] ✅ Rclone RCD transfer complete: '%s' -> %s in %s (Avg: %s) [8 parallel streams]\n",
		a.NodeID, key, final.NodeID, elapsed.Round(time.Millisecond), formatTransferSpeed(avgSpeed))

	return nil
}
