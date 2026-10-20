package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

const bucketTablePrefix = "bucket:"

func bucketKey(id string) []byte {
	return []byte(bucketTablePrefix + strings.TrimSpace(id))
}

// BucketRecord stores configuration and credentials for an S3/R2 remote object store.
type BucketRecord struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`                  // e.g. "stream-media-archive"
	Provider     string    `json:"provider"`              // "r2", "s3", "minio", "b2", "wasabi", "custom"
	Endpoint     string    `json:"endpoint"`              // e.g. "https://<account>.r2.cloudflarestorage.com"
	Region       string    `json:"region"`                // e.g. "auto", "us-east-1"
	AccessKey    string    `json:"access_key"`            // Access Key ID
	SecretKey    string    `json:"secret_key,omitempty"`  // Secret Access Key
	PublicHost   string    `json:"public_host,omitempty"` // Public CDN URL (e.g. "https://cdn.example.com")
	UsedBytes    int64     `json:"used_bytes"`            // Total aggregated bytes in bucket
	ObjectCount  int64     `json:"object_count"`          // Total object count
	LastSyncedAt time.Time `json:"last_synced_at"`        // Last time size was synced
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SaveBucket stores or updates an S3/R2 bucket record.
func (s *Store) SaveBucket(b *BucketRecord) error {
	if s == nil || b == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if b.ID == "" {
		b.ID = fmt.Sprintf("bkt-%d", time.Now().UnixNano())
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	b.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(bucketKey(b.ID), data)
	})
}

// GetBucket retrieves a bucket record by ID.
func (s *Store) GetBucket(id string) (*BucketRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var b BucketRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(bucketKey(id))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &b)
		})
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// ListBuckets returns all registered buckets ordered newest first.
func (s *Store) ListBuckets() ([]*BucketRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*BucketRecord
	prefix := []byte(bucketTablePrefix)

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				var b BucketRecord
				if err := json.Unmarshal(val, &b); err == nil {
					list = append(list, &b)
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return list, nil
}

// DeleteBucket removes a bucket configuration by ID.
func (s *Store) DeleteBucket(id string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(bucketKey(id))
	})
}
