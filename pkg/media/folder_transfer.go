package media

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CalculateFolderSize returns the total size in bytes of all regular files in baseDir.
func CalculateFolderSize(baseDir string) (int64, error) {
	var total int64
	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// PackFolder streams the contents of baseDir as an uncompressed tar archive to w.
// Uncompressed tar eliminates CPU compression bottlenecks, allowing multi-gigabit
// transfer speeds for video media that is already encoded and compressed.
func PackFolder(w io.Writer, baseDir string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	return filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil // files carry their parent dirs implicitly on extract
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = "./" + rel
		hdr.Format = tar.FormatPAX
		hdr.ModTime = info.ModTime()
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not transferable: %s", rel)
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	})
}

// UnpackFolder extracts a PackFolder archive from r into destDir (created if
// missing) and returns the number of files written. It automatically detects
// whether the stream is plain tar or gzip-compressed (backward compatibility).
// Paths are sanitized: absolute paths, ".." traversal, symlinks and special files
// are rejected so a malicious sender can never escape destDir.
func UnpackFolder(r io.Reader, destDir string) (int, error) {
	br := bufio.NewReaderSize(r, 64*1024)

	var tr *tar.Reader
	magic, err := br.Peek(2)
	if err == nil && len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		// Legacy gzip compressed stream
		gz, err := gzip.NewReader(br)
		if err != nil {
			return 0, fmt.Errorf("gzip header: %w", err)
		}
		defer gz.Close()
		tr = tar.NewReader(gz)
	} else {
		// High-speed raw tar stream
		tr = tar.NewReader(br)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return 0, err
	}

	files := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return files, fmt.Errorf("tar stream: %w", err)
		}

		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := safeMkdirAll(destDir, name); err != nil {
				return files, err
			}
		case tar.TypeReg:
			if err := safeMkdirAll(destDir, filepath.Dir(name)); err != nil {
				return files, err
			}
			target := filepath.Join(destDir, name)
			if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
				return files, fmt.Errorf("archive entry escapes destination: %s", hdr.Name)
			}
			if err := writeFileAtomic(target, tr, hdr.FileInfo().Mode()); err != nil {
				return files, err
			}
			files++
		default:
			return files, fmt.Errorf("unsupported archive entry type %q: %s", string(hdr.Typeflag), hdr.Name)
		}
	}
}

// safeMkdirAll creates sub under root after validating it stays inside root.
func safeMkdirAll(root, sub string) error {
	if filepath.IsAbs(sub) || strings.Contains(sub, "..") {
		return fmt.Errorf("unsafe archive path: %s", sub)
	}
	return os.MkdirAll(filepath.Join(root, sub), 0755)
}

// writeFileAtomic streams one archive member to a temp file next to target
// and renames it into place, so a partial transfer never leaves half files.
func writeFileAtomic(target string, r io.Reader, mode os.FileMode) error {
	tmp := target + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
}

// SweepStaleScratch removes scratch subfolders whose latest modification is
// older than maxAge. It is the crash-safe garbage collector for interrupted
// processing jobs (a healthy job always deletes its own scratch folder).
func SweepStaleScratch(scratchDir string, maxAge time.Duration) (removed []string) {
	entries, err := os.ReadDir(scratchDir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(scratchDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if maxAge > 0 && info.ModTime().After(cutoff) {
			continue
		}
		if os.RemoveAll(dir) == nil {
			removed = append(removed, dir)
		}
	}
	return removed
}
