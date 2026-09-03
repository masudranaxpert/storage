package download

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var downloadBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 64*1024) // 64 KB bounded streaming buffer
		return &buf
	},
}

// ProgressFunc reports real-time download progress (percentage, speed string).
type ProgressFunc func(percent float64, speed string)

// HasAria2 checks if aria2c executable is available in system PATH.
func HasAria2() bool {
	_, err := exec.LookPath("aria2c")
	return err == nil
}

// DownloadFile downloads a remote file to dstPath using aria2c if available,
// automatically falling back to pure-Go streaming HTTP downloader on any error.
func DownloadFile(ctx context.Context, srcURL, dstPath string, onProgress ProgressFunc) error {
	srcURL = strings.TrimSpace(srcURL)
	if !strings.HasPrefix(srcURL, "http://") && !strings.HasPrefix(srcURL, "https://") {
		srcURL = "https://" + srcURL
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	if HasAria2() {
		err := downloadWithAria2(ctx, srcURL, dstPath, onProgress)
		if err == nil {
			return nil
		}
		fmt.Printf("[Downloader] aria2c failed (%v), falling back to native streaming HTTP engine...\n", err)
	}

	return downloadWithNativeGo(ctx, srcURL, dstPath, onProgress)
}

// downloadWithAria2 executes aria2c with 16 parallel connections and parses progress lines.
func downloadWithAria2(ctx context.Context, srcURL, dstPath string, onProgress ProgressFunc) error {
	dir := filepath.Dir(dstPath)
	filename := filepath.Base(dstPath)

	// Standard high-performance aria2 configuration (as benchmarked by yt-dlp & aria2-pro)
	args := []string{
		"-c",                       // Continue downloading partially downloaded file
		"-j", "16",                 // Max concurrent downloads
		"-x", "16",                 // Max connections per server (16 threads)
		"-s", "16",                 // Split into 16 parts
		"-k", "1M",                 // Min split size (1MB chunks)
		"--file-allocation=none",   // Instant start without disk pre-allocation delay
		"--check-certificate=false", // Ignore CDN/self-signed SSL certificate issues
		"--summary-interval=1",     // Emit progress line every 1 second
		"--dir=" + dir,
		"--out=" + filename,
		"--allow-overwrite=true",
		srcURL,
	}

	cmd := exec.CommandContext(ctx, "aria2c", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Parse aria2c stdout lines e.g.: [ #1234 1.2GiB/7.7GiB(15%) CN:16 DL:7.9MiB ETA:8m ]
	pctRegex := regexp.MustCompile(`\((\d+)%\)`)
	dlRegex := regexp.MustCompile(`DL:([0-9\.]+[A-Za-z]+)`)
	etaRegex := regexp.MustCompile(`ETA:([0-9\.]+[A-Za-z]+)`)
	scanner := bufio.NewScanner(stdout)

	fmt.Printf("[Aria2c] 🚀 Starting aria2c (16 parallel connections) for '%s'\n", filename)
	var lastLog time.Time
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			pctMatches := pctRegex.FindStringSubmatch(line)
			if len(pctMatches) >= 2 && onProgress != nil {
				pct, _ := strconv.ParseFloat(pctMatches[1], 64)
				speedStr := ""
				if dlMatches := dlRegex.FindStringSubmatch(line); len(dlMatches) >= 2 {
					speedStr = dlMatches[1]
					if !strings.HasSuffix(speedStr, "/s") && !strings.HasSuffix(speedStr, "/S") {
						speedStr += "/s"
					}
				}
				if etaMatches := etaRegex.FindStringSubmatch(line); len(etaMatches) >= 2 {
					if speedStr != "" {
						speedStr += fmt.Sprintf(" • ETA: %s", etaMatches[1])
					} else {
						speedStr = fmt.Sprintf("ETA: %s", etaMatches[1])
					}
				}
				onProgress(pct, speedStr)
				if time.Since(lastLog) >= 2*time.Second {
					lastLog = time.Now()
					fmt.Printf("[Aria2c] 📥 %s: %.1f%% | Speed: %s\n", filename, pct, speedStr)
				}
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		fmt.Printf("[Aria2c] ❌ aria2c finished with error: %v\n", err)
		return err
	}
	fmt.Printf("[Aria2c] ✅ aria2c completed download: %s\n", filename)
	return nil
}

// downloadWithNativeGo streams the response directly to disk with bounded memory allocations.
func downloadWithNativeGo(ctx context.Context, srcURL, dstPath string, onProgress ProgressFunc) error {
	req, err := http.NewRequestWithContext(ctx, "GET", srcURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   45 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "no such host") || strings.Contains(errStr, "lookup") {
			return fmt.Errorf("DNS error: Host not found / invalid domain in URL")
		}
		if strings.Contains(errStr, "connection refused") {
			return fmt.Errorf("Connection refused by remote server")
		}
		if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") {
			return fmt.Errorf("Connection timed out reaching source server")
		}
		return fmt.Errorf("network request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("HTTP 404 (File not found / broken URL)")
		case http.StatusForbidden:
			return fmt.Errorf("HTTP 403 (Access forbidden or expired download token)")
		case http.StatusUnauthorized:
			return fmt.Errorf("HTTP 401 (Authentication required)")
		case http.StatusTooManyRequests:
			return fmt.Errorf("HTTP 429 (Rate limit exceeded on source host)")
		default:
			return fmt.Errorf("HTTP %d error (%s)", resp.StatusCode, resp.Status)
		}
	}

	totalBytes := resp.ContentLength
	out, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer out.Close()

	bufPtr := downloadBufferPool.Get().(*[]byte)
	defer downloadBufferPool.Put(bufPtr)
	buf := *bufPtr

	var downloaded int64
	lastUpdate := time.Now()
	var lastDownloaded int64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write error: %w", writeErr)
			}
			downloaded += int64(n)

			// Report progress at most once every 500ms
			now := time.Now()
			elapsed := now.Sub(lastUpdate)
			if elapsed >= 500*time.Millisecond && onProgress != nil {
				var pct float64
				if totalBytes > 0 {
					pct = float64(downloaded) / float64(totalBytes) * 100.0
				}
				bytesDiff := downloaded - lastDownloaded
				speedBps := float64(bytesDiff) / elapsed.Seconds()
				speedStr := formatSpeed(speedBps)

				onProgress(pct, speedStr)
				lastUpdate = now
				lastDownloaded = downloaded
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("stream read error: %w", readErr)
		}
	}

	if onProgress != nil {
		onProgress(100.0, "Done")
	}
	return nil
}

func formatSpeed(bytesPerSec float64) string {
	if bytesPerSec < 1024 {
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	} else if bytesPerSec < 1024*1024 {
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/1024)
	}
	return fmt.Sprintf("%.1f MB/s", bytesPerSec/(1024*1024))
}

