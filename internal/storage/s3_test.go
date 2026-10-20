package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestS3Operations(t *testing.T) {
	var mu sync.Mutex
	objects := make(map[string][]byte)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
			http.Error(w, "missing or invalid authorization", http.StatusUnauthorized)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/test-bucket")
		path = strings.TrimPrefix(path, "/")

		switch r.Method {
		case http.MethodHead:
			if path == "" {
				w.WriteHeader(http.StatusOK)
				return
			}
			data, ok := objects[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.WriteHeader(http.StatusOK)

		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			objects[path] = data
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			if r.URL.Query().Get("list-type") == "2" {
				prefix := r.URL.Query().Get("prefix")
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
				for k, v := range objects {
					if strings.HasPrefix(k, prefix) {
						fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-19T12:00:00Z</LastModified></Contents>`, k, len(v))
					}
				}
				fmt.Fprint(w, `</ListBucketResult>`)
				return
			}
			data, ok := objects[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)

		case http.MethodDelete:
			delete(objects, path)
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	cfg := S3Config{
		Name:      "test-bucket",
		Provider:  "r2",
		Endpoint:  ts.URL,
		Region:    "auto",
		AccessKey: "test-access",
		SecretKey: "test-secret",
	}

	ctx := context.Background()

	// 1. Test Connection
	if err := TestBucketConnection(ctx, cfg); err != nil {
		t.Fatalf("TestBucketConnection failed: %v", err)
	}

	// 2. Upload Object
	testData := []byte("hello world video manifest")
	if err := UploadObjectS3(ctx, cfg, "media1/video.mp4", testData, ""); err != nil {
		t.Fatalf("UploadObjectS3 failed: %v", err)
	}

	// 3. Head Object
	size, err := HeadObjectS3(ctx, cfg, "media1/video.mp4")
	if err != nil {
		t.Fatalf("HeadObjectS3 failed: %v", err)
	}
	if size != int64(len(testData)) {
		t.Fatalf("HeadObjectS3 expected size %d, got %d", len(testData), size)
	}

	// 4. Download Object
	gotData, err := DownloadObjectS3(ctx, cfg, "media1/video.mp4")
	if err != nil {
		t.Fatalf("DownloadObjectS3 failed: %v", err)
	}
	if string(gotData) != string(testData) {
		t.Fatalf("DownloadObjectS3 expected %s, got %s", string(testData), string(gotData))
	}

	// 5. List Objects
	items, err := ListObjectsS3(ctx, cfg, "media1/")
	if err != nil {
		t.Fatalf("ListObjectsS3 failed: %v", err)
	}
	if len(items) != 1 || items[0].Key != "media1/video.mp4" {
		t.Fatalf("ListObjectsS3 expected 1 item, got %+v", items)
	}

	// 6. Upload Folder
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "video.mp4"), []byte("manifest"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "index.m3u8"), []byte("sub-manifest"), 0644)

	var uploadProgress int64
	if err := UploadFolderS3(ctx, cfg, "stream-key", tmpDir, func(copied int64) {
		uploadProgress = copied
	}); err != nil {
		t.Fatalf("UploadFolderS3 failed: %v", err)
	}
	if uploadProgress <= 0 {
		t.Fatalf("UploadFolderS3 progress was not reported")
	}

	// 7. Download Folder
	destDir := t.TempDir()
	var downloadProgress int64
	if err := DownloadFolderS3(ctx, cfg, "stream-key", destDir, func(copied int64) {
		downloadProgress = copied
	}); err != nil {
		t.Fatalf("DownloadFolderS3 failed: %v", err)
	}
	if downloadProgress <= 0 {
		t.Fatalf("DownloadFolderS3 progress was not reported")
	}

	if _, err := os.Stat(filepath.Join(destDir, "video.mp4")); err != nil {
		t.Fatalf("DownloadFolderS3 video.mp4 missing: %v", err)
	}

	// 8. Delete Folder
	if err := DeleteFolderS3(ctx, cfg, "stream-key"); err != nil {
		t.Fatalf("DeleteFolderS3 failed: %v", err)
	}
	leftover, _ := ListObjectsS3(ctx, cfg, "stream-key/")
	if len(leftover) != 0 {
		t.Fatalf("DeleteFolderS3 left %d objects", len(leftover))
	}
}
