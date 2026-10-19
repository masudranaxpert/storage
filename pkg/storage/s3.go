package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// S3Config wraps connection parameters for an S3/R2 remote bucket.
type S3Config struct {
	Name      string
	Provider  string
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
}

// TestBucketConnection verifies connectivity and credentials by issuing an authenticated S3 request.
func TestBucketConnection(ctx context.Context, cfg S3Config) error {
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

	// Use HEAD request to check bucket existence and permission
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

	// If HEAD is disallowed by policy, fallback to a 1-item list request
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

// SignS3Request signs an HTTP request with AWS Signature Version 4.
func SignS3Request(req *http.Request, cfg S3Config, payload []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadHash := sha256Hex(payload)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

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
