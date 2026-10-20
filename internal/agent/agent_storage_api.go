package agent

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v3/disk"
	"golang.org/x/net/webdav"
	"stream/internal/model"
)

type StreamDriveStat = model.StreamDriveStat

func (a *Agent) getStreamDrives() []StreamDriveStat {
	var drives []StreamDriveStat
	seenRoots := make(map[string]bool)

	addRoot := func(mountPoint, driveName string) {
		mountPoint = filepath.Clean(mountPoint)
		var streamRoot string
		if mountPoint == "/" || mountPoint == "." || mountPoint == "\\" {
			streamRoot = "/stream"
		} else {
			streamRoot = filepath.Join(mountPoint, "stream")
		}

		cleanRoot := filepath.Clean(streamRoot)
		if seenRoots[cleanRoot] {
			return
		}
		seenRoots[cleanRoot] = true

		mediaDir := filepath.Join(cleanRoot, "media")
		procDir := filepath.Join(cleanRoot, "processing")

		mediaSize, mediaCount := measureDir(mediaDir)
		procSize, procCount := measureDir(procDir)

		// Check if data is inside /var/lib/stream/media and this is the root drive
		if (mountPoint == "/" || mountPoint == "/stream") && mediaSize == 0 {
			varLibMedia := "/var/lib/stream/media"
			if stat, err := os.Lstat(varLibMedia); err == nil && stat.Mode()&os.ModeSymlink == 0 {
				s, c := measureDir(varLibMedia)
				if s > 0 {
					mediaSize = s
					mediaCount = c
				}
			}
		}

		drives = append(drives, StreamDriveStat{
			DriveName:           driveName,
			MountPoint:          mountPoint,
			StreamRoot:          cleanRoot,
			MediaDir:            mediaDir,
			MediaSizeBytes:      mediaSize,
			MediaFileCount:      mediaCount,
			ProcessingDir:       procDir,
			ProcessingSizeBytes: procSize,
			ProcessingFileCount: procCount,
			TotalStreamBytes:    mediaSize + procSize,
		})
	}

	// 1. Discover active mounts on Linux (matches telemetry.collectDisks)
	if runtime.GOOS == "linux" {
		partitions, err := disk.Partitions(false)
		if err == nil {
			for _, p := range partitions {
				if strings.HasPrefix(p.Mountpoint, "/proc") ||
					strings.HasPrefix(p.Mountpoint, "/sys") ||
					strings.HasPrefix(p.Mountpoint, "/dev") ||
					strings.HasPrefix(p.Mountpoint, "/run") ||
					strings.HasPrefix(p.Mountpoint, "/boot") ||
					strings.HasPrefix(p.Mountpoint, "/snap") ||
					strings.Contains(p.Fstype, "squashfs") ||
					strings.Contains(p.Fstype, "tmpfs") ||
					strings.Contains(p.Fstype, "overlay") {
					continue
				}

				name := p.Mountpoint
				if p.Mountpoint == "/" {
					name = "System SSD (/)"
				} else {
					name = fmt.Sprintf("%s (%s)", filepath.Base(p.Mountpoint), p.Mountpoint)
				}
				addRoot(p.Mountpoint, name)
			}
		}
	}

	// 2. Always ensure system root "/" is present
	if !seenRoots["/stream"] {
		addRoot("/", "System SSD (/)")
	}

	return drives
}

// handleWebDAV provides high-speed WebDAV file ingest for rclone multi-stream transfers.
func (a *Agent) handleWebDAV(w http.ResponseWriter, r *http.Request) {
	authed := false
	if a.verifySecret(r) {
		authed = true
	} else if user, pass, ok := r.BasicAuth(); ok && a.Secret != "" {
		if subtle.ConstantTimeCompare([]byte(pass), []byte(a.Secret)) == 1 && (user == "admin" || user == "stream") {
			authed = true
		}
	}

	if !authed {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cluster WebDAV"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	subPath := strings.TrimPrefix(r.URL.Path, "/api/v1/webdav/")
	parts := strings.SplitN(subPath, "/", 3)
	if len(parts) < 2 {
		http.Error(w, "invalid webdav path", http.StatusBadRequest)
		return
	}
	hexDir := parts[0]
	key := parts[1]
	if !validKey(key) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}

	rawDirBytes, err := hex.DecodeString(hexDir)
	if err != nil {
		http.Error(w, "invalid hex directory", http.StatusBadRequest)
		return
	}
	rawDir := string(rawDirBytes)
	dir := a.resolveTargetDir(filepath.Clean(rawDir))
	dest := filepath.Join(dir, key)

	if r.Method == http.MethodPut && a.Quota != nil && r.ContentLength > 0 {
		if err := a.Quota.Reserve(uint64(r.ContentLength)); err != nil {
			http.Error(w, fmt.Sprintf("insufficient storage quota: %v", err), http.StatusInsufficientStorage)
			return
		}
		defer a.Quota.Commit(uint64(r.ContentLength))
	}

	_ = os.MkdirAll(dest, 0755)
	a.registerRoot(dir)

	prefix := "/api/v1/webdav/" + hexDir + "/" + key
	handler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: webdav.Dir(dest),
		LockSystem: webdav.NewMemLS(),
	}
	handler.ServeHTTP(w, r)
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
