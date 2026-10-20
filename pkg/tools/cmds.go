// Package tools holds shared shell snippets for installing/uninstalling
// media worker binaries (aria2c, ffmpeg, rclone) on Linux VPS agents.
package tools

import (
	"fmt"
	"strings"
)

// ValidateToolName checks if the requested tool name is in the strict allow-list.
func ValidateToolName(tool string) error {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "", "all", "core", "media", "aria2", "aria2c", "ffmpeg", "rclone":
		return nil
	default:
		return fmt.Errorf("invalid tool name: %q (allowed: aria2c, ffmpeg, rclone, all, core, media)", tool)
	}
}

// InstallShell returns a bash one-liner that installs the requested tool(s).
// tool: "aria2"|"aria2c"|"ffmpeg"|"rclone"|"all"|"" (all).
func InstallShell(tool string) string {
	t := strings.ToLower(strings.TrimSpace(tool))
	switch t {
	case "aria2", "aria2c":
		return aptInstall("aria2")
	case "ffmpeg":
		return aptInstall("ffmpeg")
	case "rclone":
		return InstallRcloneShell()
	case "", "all", "core", "media":
		// Full media stack: download + package + transfer (S3/FTP/node).
		return aptInstall("aria2 ffmpeg") + " && " + InstallRcloneShell()
	default:
		return ""
	}
}

// UninstallShell returns a bash one-liner that removes the requested tool(s).
func UninstallShell(tool string) string {
	t := strings.ToLower(strings.TrimSpace(tool))
	switch t {
	case "aria2", "aria2c":
		return aptPurge("aria2") + `; rm -f /usr/local/bin/aria2c /usr/bin/aria2c /bin/aria2c; hash -r 2>/dev/null || true`
	case "ffmpeg":
		return aptPurge("ffmpeg") + `; rm -f /usr/local/bin/ffmpeg /usr/bin/ffmpeg /bin/ffmpeg /usr/local/bin/ffprobe /usr/bin/ffprobe /bin/ffprobe; hash -r 2>/dev/null || true`
	case "rclone":
		return aptPurge("rclone") + `; rm -f /usr/bin/rclone /usr/local/bin/rclone; hash -r 2>/dev/null || true`
	case "", "all", "core", "media":
		return aptPurge("aria2 ffmpeg rclone") + `; rm -f /usr/local/bin/aria2c /usr/bin/aria2c /bin/aria2c /usr/local/bin/ffmpeg /usr/bin/ffmpeg /bin/ffmpeg /usr/local/bin/ffprobe /usr/bin/ffprobe /bin/ffprobe /usr/bin/rclone /usr/local/bin/rclone; hash -r 2>/dev/null || true`
	default:
		return ""
	}
}

// EnsureMediaToolsShell installs aria2 + ffmpeg + rclone if any are missing.
func EnsureMediaToolsShell() string {
	return `which aria2c >/dev/null 2>&1 && which ffmpeg >/dev/null 2>&1 && which rclone >/dev/null 2>&1 || (` + InstallShell("all") + `)`
}

// InstallRcloneShell prefers the official rclone installer, then falls back to apt.
func InstallRcloneShell() string {
	return `command -v rclone >/dev/null 2>&1 || { ` +
		`(command -v unzip >/dev/null 2>&1 || (` + aptInstall("unzip curl ca-certificates") + `)); ` +
		`curl -fsSL https://rclone.org/install.sh | bash; } || (` + aptInstall("rclone") + `)`
}

func aptInstall(pkgs string) string {
	return `DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ` + pkgs
}

func aptPurge(pkgs string) string {
	return `DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq ` + pkgs + ` 2>/dev/null; DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq 2>/dev/null`
}
