package cluster

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// S3Config wraps connection parameters for an S3/R2 remote bucket.
type S3Config struct {
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Endpoint   string `json:"endpoint"`
	Region     string `json:"region"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	PublicHost string `json:"public_host,omitempty"`
}

// S3Object represents a stored item in an S3/R2 bucket.
type S3Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// TestBucketConnection verifies connectivity and credentials by issuing an authenticated S3 request.
func TestBucketConnection(ctx context.Context, cfg S3Config) error {
	normalizeS3Config(&cfg)
	if cfg.Name == "" {
		return errors.New("bucket name is required")
	}
	if cfg.Endpoint == "" {
		return errors.New("endpoint URL is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return errors.New("access key ID and secret access key are required")
	}

	rawURL := fmt.Sprintf("%s/%s", cfg.Endpoint, url.PathEscape(cfg.Name))
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return err
	}

	SignS3Request(req, cfg, nil)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if resp.StatusCode == http.StatusMethodNotAllowed {
		listReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"?max-keys=1", nil)
		if err == nil {
			SignS3Request(listReq, cfg, nil)
			listResp, err := client.Do(listReq)
			if err == nil {
				defer listResp.Body.Close()
				if listResp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusForbidden:
		return errors.New("access denied: verify Access Key ID, Secret Key, and bucket permissions")
	case http.StatusNotFound:
		return fmt.Errorf("bucket %q not found on endpoint %s", cfg.Name, cfg.Endpoint)
	default:
		msg := strings.TrimSpace(string(body))
		if msg != "" {
			return fmt.Errorf("S3 error (%d): %s", resp.StatusCode, msg)
		}
		return fmt.Errorf("S3 error (%d %s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
}

// EnableBucketCORS configures permissive CORS on the S3/R2 bucket so browser players can fetch byte ranges and subtitles.
func EnableBucketCORS(ctx context.Context, cfg S3Config) error {
	normalizeS3Config(&cfg)
	if cfg.Name == "" || cfg.Endpoint == "" {
		return errors.New("bucket name and endpoint required")
	}

	corsXML := `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <CORSRule>
    <AllowedOrigin>*</AllowedOrigin>
    <AllowedMethod>GET</AllowedMethod>
    <AllowedMethod>HEAD</AllowedMethod>
    <AllowedHeader>*</AllowedHeader>
    <ExposeHeader>Content-Range</ExposeHeader>
    <ExposeHeader>Content-Length</ExposeHeader>
    <ExposeHeader>Accept-Ranges</ExposeHeader>
    <ExposeHeader>ETag</ExposeHeader>
    <MaxAgeSeconds>3600</MaxAgeSeconds>
  </CORSRule>
</CORSConfiguration>`

	urlStr := fmt.Sprintf("%s/%s/?cors=", strings.TrimRight(cfg.Endpoint, "/"), url.PathEscape(cfg.Name))
	u, err := url.Parse(urlStr)
	if err != nil {
		return err
	}

	payload := []byte(corsXML)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(payload))
	req.Header.Set("Content-Type", "application/xml")

	SignS3Request(req, cfg, payload)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("enable CORS failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// SignS3Request signs an HTTP request with AWS Signature Version 4.
func SignS3Request(req *http.Request, cfg S3Config, payload []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadHash := req.Header.Get("x-amz-content-sha256")
	if payloadHash == "" {
		payloadHash = sha256Hex(payload)
		req.Header.Set("x-amz-content-sha256", payloadHash)
	}
	req.Header.Set("x-amz-date", amzDate)

	host := req.URL.Host
	if host == "" {
		host = req.Host
	}
	req.Header.Set("Host", host)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalQuery := req.URL.RawQuery

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		host, payloadHash, amzDate)

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash)

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, cfg.Region)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, credentialScope, sha256Hex([]byte(canonicalRequest)))

	signingKey := getSignatureKey(cfg.SecretKey, dateStamp, cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cfg.AccessKey, credentialScope, signedHeaders, signature)

	req.Header.Set("Authorization", authHeader)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func getSignatureKey(key, dateStamp, regionName, serviceName string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+key), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(regionName))
	kService := hmacSHA256(kRegion, []byte(serviceName))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

func normalizeS3Config(cfg *S3Config) {
	if cfg.Region == "" {
		if strings.ToLower(cfg.Provider) == "r2" {
			cfg.Region = "auto"
		} else {
			cfg.Region = "us-east-1"
		}
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	cfg.AccessKey = strings.TrimSpace(cfg.AccessKey)
	cfg.SecretKey = strings.TrimSpace(cfg.SecretKey)
}

func s3ObjectURL(cfg S3Config, objectKey string) (*url.URL, error) {
	key := strings.TrimLeft(filepath.ToSlash(objectKey), "/")
	parts := strings.Split(key, "/")
	escapedParts := make([]string, len(parts))
	for i, p := range parts {
		escapedParts[i] = url.PathEscape(p)
	}
	raw := fmt.Sprintf("%s/%s/%s", strings.TrimRight(cfg.Endpoint, "/"), url.PathEscape(cfg.Name), strings.Join(escapedParts, "/"))
	return url.Parse(raw)
}

// UploadStreamS3 streams an io.Reader of known size to S3/R2 with SigV4 signing.
func UploadStreamS3(ctx context.Context, cfg S3Config, objectKey string, r io.Reader, size int64, contentType string) error {
	normalizeS3Config(&cfg)
	u, err := s3ObjectURL(cfg, objectKey)
	if err != nil {
		return err
	}

	if contentType == "" {
		switch strings.ToLower(filepath.Ext(objectKey)) {
		case ".m3u8":
			contentType = "application/vnd.apple.mpegurl"
		case ".mp4":
			contentType = "video/mp4"
		case ".m4s":
			contentType = "video/iso.segment"
		case ".json":
			contentType = "application/json"
		case ".vtt":
			contentType = "text/vtt"
		case ".ts":
			contentType = "video/mp2t"
		default:
			contentType = mime.TypeByExtension(filepath.Ext(objectKey))
			if contentType == "" {
				contentType = "application/octet-stream"
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")

	SignS3Request(req, cfg, nil)

	client := &http.Client{} // streaming upload respects ctx deadline
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("S3 upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("S3 upload %s failed (%d): %s", objectKey, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// UploadObjectS3 uploads an in-memory byte slice to S3/R2 with SigV4 signing.
func UploadObjectS3(ctx context.Context, cfg S3Config, objectKey string, data []byte, contentType string) error {
	return UploadStreamS3(ctx, cfg, objectKey, bytes.NewReader(data), int64(len(data)), contentType)
}

// DownloadObjectS3 retrieves an object from S3/R2 with SigV4 signing.
func DownloadObjectS3(ctx context.Context, cfg S3Config, objectKey string) ([]byte, error) {
	normalizeS3Config(&cfg)
	u, err := s3ObjectURL(cfg, objectKey)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	SignS3Request(req, cfg, nil)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("S3 download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("S3 download %s failed (%d): %s", objectKey, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

// HeadObjectS3 queries object existence and returns its size in bytes.
func HeadObjectS3(ctx context.Context, cfg S3Config, objectKey string) (int64, error) {
	normalizeS3Config(&cfg)
	u, err := s3ObjectURL(cfg, objectKey)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return 0, err
	}
	SignS3Request(req, cfg, nil)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("S3 head failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return 0, os.ErrNotExist
		}
		return 0, fmt.Errorf("S3 head %s failed (%d)", objectKey, resp.StatusCode)
	}
	return resp.ContentLength, nil
}

// DeleteObjectS3 removes a single object from S3/R2.
func DeleteObjectS3(ctx context.Context, cfg S3Config, objectKey string) error {
	normalizeS3Config(&cfg)
	u, err := s3ObjectURL(cfg, objectKey)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	SignS3Request(req, cfg, nil)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("S3 delete failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("S3 delete %s failed (%d): %s", objectKey, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type listBucketV2Output struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// ListObjectsS3 lists all objects under a given key prefix using S3 ListObjectsV2.
func ListObjectsS3(ctx context.Context, cfg S3Config, prefix string) ([]S3Object, error) {
	normalizeS3Config(&cfg)
	var results []S3Object
	token := ""

	client := &http.Client{Timeout: 30 * time.Second}
	for {
		raw := fmt.Sprintf("%s/%s?list-type=2&prefix=%s",
			strings.TrimRight(cfg.Endpoint, "/"),
			url.PathEscape(cfg.Name),
			url.QueryEscape(prefix))
		if token != "" {
			raw += "&continuation-token=" + url.QueryEscape(token)
		}

		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		SignS3Request(req, cfg, nil)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("S3 list failed: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("S3 list failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var out listBucketV2Output
		if err := xml.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("parse S3 list response: %w", err)
		}

		for _, c := range out.Contents {
			var mod time.Time
			if c.LastModified != "" {
				mod, _ = time.Parse(time.RFC3339, c.LastModified)
			}
			results = append(results, S3Object{
				Key:          c.Key,
				Size:         c.Size,
				LastModified: mod,
			})
		}

		if !out.IsTruncated || out.NextContinuationToken == "" {
			break
		}
		token = out.NextContinuationToken
	}

	return results, nil
}

// DeleteFolderS3 removes all objects sharing the given prefix (e.g. "key/").
func DeleteFolderS3(ctx context.Context, cfg S3Config, prefix string) error {
	objects, err := ListObjectsS3(ctx, cfg, prefix)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if err := DeleteObjectS3(ctx, cfg, obj.Key); err != nil {
			return err
		}
	}
	return nil
}

// UploadFolderS3 uploads all files in localDir to S3 under keyPrefix.
func UploadFolderS3(ctx context.Context, cfg S3Config, keyPrefix, localDir string, onProgress func(copiedBytes int64)) error {
	var copied int64
	return filepath.WalkDir(localDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d == nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}

		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		objectKey := strings.Trim(keyPrefix, "/") + "/" + filepath.ToSlash(rel)
		if keyPrefix == "" || keyPrefix == "." {
			objectKey = filepath.ToSlash(rel)
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil {
			return err
		}

		if err := UploadStreamS3(ctx, cfg, objectKey, f, fi.Size(), ""); err != nil {
			return err
		}
		copied += fi.Size()
		if onProgress != nil {
			onProgress(copied)
		}
		return nil
	})
}

// DownloadFolderS3 downloads all objects matching keyPrefix from S3 to localDir.
func DownloadFolderS3(ctx context.Context, cfg S3Config, keyPrefix, localDir string, onProgress func(copiedBytes int64)) error {
	prefix := strings.Trim(keyPrefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	objects, err := ListObjectsS3(ctx, cfg, prefix)
	if err != nil {
		return err
	}
	if len(objects) == 0 {
		return fmt.Errorf("no objects found with prefix %q in S3", keyPrefix)
	}

	if err := os.MkdirAll(localDir, 0755); err != nil {
		return err
	}

	var copied int64
	for _, obj := range objects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel := strings.TrimPrefix(obj.Key, prefix)
		rel = strings.TrimLeft(rel, "/")
		if rel == "" {
			continue
		}

		targetFile := filepath.Join(localDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(targetFile), 0755); err != nil {
			return err
		}

		data, err := DownloadObjectS3(ctx, cfg, obj.Key)
		if err != nil {
			return err
		}

		tmpFile := targetFile + ".tmp"
		if err := os.WriteFile(tmpFile, data, 0644); err != nil {
			return err
		}
		if err := os.Rename(tmpFile, targetFile); err != nil {
			_ = os.Remove(tmpFile)
			return err
		}

		copied += int64(len(data))
		if onProgress != nil {
			onProgress(copied)
		}
	}
	return nil
}
