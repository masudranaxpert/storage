package cluster

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"stream/pkg/tools"
)

var (
	isInstallingTools bool
	isUpgrading       bool
	agentOpMu         sync.Mutex
)

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

		upgradeClient := &http.Client{
			Timeout: 5 * time.Minute,
		}
		resp, err := upgradeClient.Get(targetURL)
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

		// If payload is a tar.gz worker bundle, extract all binaries to /opt/stream/bin
		if len(binBytes) >= 2 && binBytes[0] == 0x1f && binBytes[1] == 0x8b {
			gr, err := gzip.NewReader(bytes.NewReader(binBytes))
			if err == nil {
				tr := tar.NewReader(gr)
				_ = os.MkdirAll("/opt/stream/bin", 0755)
				extractedCount := 0
				for {
					header, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						break
					}
					if header.Typeflag == tar.TypeReg && strings.HasPrefix(header.Name, "stream/bin/") {
						targetName := filepath.Base(header.Name)
						targetPath := filepath.Join("/opt/stream/bin", targetName)
						fileData, err := io.ReadAll(tr)
						if err != nil {
							continue
						}
						tmpPath := targetPath + ".new"
						if err := os.WriteFile(tmpPath, fileData, 0755); err == nil {
							if err := os.Rename(tmpPath, targetPath); err != nil {
								_ = os.Remove(targetPath)
								_ = os.Rename(tmpPath, targetPath)
							}
							extractedCount++
						}
					}
				}
				_ = gr.Close()

				if extractedCount > 0 {
					_ = os.Remove("/usr/local/bin/stream")
					_ = os.Symlink("/opt/stream/bin/stream", "/usr/local/bin/stream")

					fmt.Printf("[Agent %s] Worker bundle successfully upgraded to %s (%d binaries refreshed). Restarting...\n", a.NodeID, newVersion, extractedCount)
					_ = exec.Command("systemctl", "restart", "stream-agent").Start()
					_ = exec.Command("systemctl", "restart", "stream").Start()
					time.Sleep(500 * time.Millisecond)
					os.Exit(0)
					return
				}
			}
		}

		currentExe, err := os.Executable()
		if err != nil {
			currentExe = "/usr/local/bin/stream"
		}
		tmpPath := currentExe + ".new"
		if err := os.WriteFile(tmpPath, binBytes, 0755); err != nil {
			tmpPath = filepath.Join(os.TempDir(), "stream-update.bin")
			if err2 := os.WriteFile(tmpPath, binBytes, 0755); err2 != nil {
				fmt.Printf("[Agent %s] Failed to write binary update: %v\n", a.NodeID, err2)
				return
			}
		}
		_ = os.Chmod(tmpPath, 0755)

		if err := os.Rename(tmpPath, currentExe); err != nil {
			_ = os.Remove(currentExe)
			if err2 := os.WriteFile(currentExe, binBytes, 0755); err2 != nil {
				fmt.Printf("[Agent %s] Failed to replace binary: %v\n", a.NodeID, err2)
				return
			}
		}
		fmt.Printf("[Agent %s] Binary upgraded to %s. Restarting stream-agent service...\n", a.NodeID, newVersion)

		_ = exec.Command("systemctl", "restart", "stream-agent").Start()
		_ = exec.Command("systemctl", "restart", "stream").Start()
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}
