package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BundleFileName is the standard worker distribution archive name.
const BundleFileName = "stream-bundle-linux-amd64.tar.gz"

// isStaticELF verifies if an ELF binary is targeted for Linux amd64 and has
// zero external dynamic library dependencies and no dynamic interpreter (PT_INTERP).
func isStaticELF(path string) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	if f.Machine != elf.EM_X86_64 {
		return false
	}

	libs, err := f.ImportedLibraries()
	if err == nil && len(libs) > 0 {
		return false
	}

	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			return false
		}
	}
	return true
}

// findProjectRoot traverses upward from the working directory to locate go.mod.
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

// staticBinaryCandidates lists search locations for standalone Linux amd64 media tools.
func staticBinaryCandidates(toolName string) []string {
	root := findProjectRoot()
	return []string{
		filepath.Join(root, "bin", "static", toolName),
		filepath.Join(root, "bin", toolName),
		filepath.Join(root, "data", "bin", toolName),
		filepath.Join("bin", "static", toolName),
		filepath.Join("bin", toolName),
		filepath.Join("data", "bin", toolName),
		filepath.Join("/opt/stream/bin", toolName),
		filepath.Join("/usr/local/bin", toolName),
	}
}

// FindStaticBinary locates a verified statically linked binary on disk.
func FindStaticBinary(toolName string) (string, bool) {
	for _, p := range staticBinaryCandidates(toolName) {
		fi, err := os.Stat(p)
		if err == nil && !fi.IsDir() && fi.Size() > 0 {
			if isStaticELF(p) {
				return p, true
			}
		}
	}
	return "", false
}

// BuildOrGetWorkerBundle creates or returns a cached self-contained worker tarball
// containing the stream agent binary, media utilities (ffmpeg, ffprobe, aria2c, rclone),
// configuration templates, and folder structures for /opt/stream.
func BuildOrGetWorkerBundle() ([]byte, error) {
	bundlePath := filepath.Join(findProjectRoot(), BundleFileName)
	streamBin, err := GetOrBuildLinuxBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to build stream agent binary: %w", err)
	}

	// Check if cached bundle exists and is up to date relative to source binaries
	if cachedData, err := os.ReadFile(bundlePath); err == nil && len(cachedData) > 0 {
		if fiBundle, err := os.Stat(bundlePath); err == nil {
			bundleModTime := fiBundle.ModTime()
			upToDate := true

			// Check all tool candidates
			for _, tool := range []string{"ffmpeg", "ffprobe", "aria2c", "rclone"} {
				if path, found := FindStaticBinary(tool); found {
					if fi, err := os.Stat(path); err == nil && fi.ModTime().After(bundleModTime) {
						upToDate = false
						break
					}
				}
			}
			if upToDate {
				return cachedData, nil
			}
		}
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	now := time.Now()

	addDir := func(name string) error {
		header := &tar.Header{
			Name:     name,
			Mode:     0755,
			Typeflag: tar.TypeDir,
			ModTime:  now,
		}
		return tw.WriteHeader(header)
	}

	addFile := func(name string, data []byte, mode int64) error {
		header := &tar.Header{
			Name:     name,
			Mode:     mode,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
			ModTime:  now,
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	addStaticFile := func(toolName, targetName string) {
		if path, found := FindStaticBinary(toolName); found {
			data, err := os.ReadFile(path)
			if err == nil && len(data) > 0 {
				_ = addFile("stream/bin/"+targetName, data, 0755)
			}
		}
	}

	// Create root directories for /opt/stream
	_ = addDir("stream/")
	_ = addDir("stream/bin/")
	_ = addDir("stream/etc/")
	_ = addDir("stream/data/")
	_ = addDir("stream/data/media/")
	_ = addDir("stream/data/processing/")

	// Add stream agent binary
	if err := addFile("stream/bin/stream", streamBin, 0755); err != nil {
		return nil, fmt.Errorf("failed to add stream binary to bundle: %w", err)
	}

	// Package verified statically linked media tools
	addStaticFile("ffmpeg", "ffmpeg")
	addStaticFile("ffprobe", "ffprobe")
	addStaticFile("aria2c", "aria2c")
	addStaticFile("rclone", "rclone")

	// Add default README / config template
	readme := []byte("# Stream Mesh Worker Node Bundle\n" +
		"Self-contained runtime deployment for /opt/stream.\n" +
		"Contains stream binary and companion tuned static media tools.\n")
	_ = addFile("stream/README.md", readme, 0644)

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	bundleBytes := buf.Bytes()
	_ = os.WriteFile(bundlePath, bundleBytes, 0644)
	return bundleBytes, nil
}
