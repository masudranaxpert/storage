package ingest

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// parallelChunkBytes is the smallest useful segment: below this a file is
// fetched single-stream because splitting costs more than it gains.
const parallelChunkBytes = 8 << 20 // 8 MB

// DownloadFileParallel fetches srcURL into dstPath using `concurrency`
// concurrent HTTP Range segments. Unlike DownloadFile it never shells out to
// aria2c — it is the built-in engine for storage nodes without worker tools.
// It degrades to a single stream when the server has no known length or the
// file is too small to split.
func DownloadFileParallel(ctx context.Context, srcURL, dstPath string, concurrency int, onProgress ProgressFunc) error {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}

	client := &http.Client{
		Timeout:   2 * time.Hour,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	size, acceptRanges, err := probeSize(ctx, client, srcURL)
	if err != nil {
		return err
	}
	if size <= 0 || !acceptRanges || size < parallelChunkBytes {
		// Cannot split: plain single stream.
		return downloadWithNativeGo(ctx, srcURL, dstPath, onProgress)
	}
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer out.Close()
	if err := out.Truncate(size); err != nil { // preallocate so WriteAt can seek anywhere
		return fmt.Errorf("preallocate output file: %w", err)
	}

	segments := buildSegments(size, concurrency)

	var downloaded int64 // bytes finished (atomic; chunks update on completion)
	var ok int32
	var firstErr error
	var mu sync.Mutex
	wg := sync.WaitGroup{}
	// Small progress ticker instead of per-chunk spam.
	done := make(chan struct{})
	go func() {
		last := int64(0)
		lastT := time.Now()
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				cur := atomic.LoadInt64(&downloaded)
				if onProgress != nil && cur != last {
					elapsed := time.Since(lastT).Seconds()
					speed := float64(cur-last) / elapsed
					pct := float64(cur) / float64(size) * 100
					onProgress(pct, formatSpeed(speed))
					last, lastT = cur, time.Now()
				}
			}
		}
	}()

	for _, seg := range segments {
		wg.Add(1)
		go func(seg segment) {
			defer wg.Done()
			var attempt int
			for {
				attempt++
				n, err := fetchRange(ctx, client, srcURL, out, seg)
				if err == nil {
					atomic.AddInt64(&downloaded, n)
					return
				}
				if ctx.Err() != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = ctx.Err()
					}
					mu.Unlock()
					return
				}
				if attempt >= 3 {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("segment %d-%d: %w", seg.start, seg.end, err)
					}
					mu.Unlock()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(attempt) * time.Second):
				}
			}
		}(seg)
	}

	wg.Wait()
	close(done)
	if firstErr != nil {
		return firstErr
	}
	atomic.StoreInt32(&ok, 1)
	if onProgress != nil {
		onProgress(100.0, "Done")
	}
	return nil
}

type segment struct {
	start, end int64 // inclusive byte range
}

func buildSegments(size int64, concurrency int) []segment {
	step := size / int64(concurrency)
	if step < parallelChunkBytes {
		step = parallelChunkBytes
	}
	segs := make([]segment, 0, concurrency)
	for start := int64(0); start < size; {
		end := start + step - 1
		if end >= size-1 {
			end = size - 1
		}
		segs = append(segs, segment{start: start, end: end})
		if end == size-1 {
			break
		}
		start = end + 1
	}
	return segs
}

// fetchRange streams one inclusive byte range into out at the right offset
// and returns the bytes written.
func fetchRange(ctx context.Context, client *http.Client, srcURL string, out *os.File, seg segment) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", seg.start, seg.end))
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("expected 206 for range request, got %s", resp.Status)
	}

	buf := make([]byte, 128*1024)
	var written int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.WriteAt(buf[:n], seg.start+written); werr != nil {
				return written, werr
			}
			written += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return written, readErr
		}
	}
	expect := seg.end - seg.start + 1
	if written != expect {
		return written, fmt.Errorf("short segment: got %d of %d bytes", written, expect)
	}
	return written, nil
}

// probeSize learns the file length and whether the server honours ranges.
func probeSize(ctx context.Context, client *http.Client, srcURL string) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, srcURL, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, false, fmt.Errorf("probe %s: %s", srcURL, resp.Status)
	}
	return resp.ContentLength, stringsContainsFold(resp.Header.Get("Accept-Ranges"), "bytes"), nil
}

func stringsContainsFold(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if equalFoldByte(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func equalFoldByte(a, b string) bool {
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
