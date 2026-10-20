package db

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// APIKeyRecord represents a programmatic API access token stored in BadgerDB.
type APIKeyRecord struct {
	Key       string    `json:"key"`    // The raw secret API key, e.g. "sm_live_9f82..."
	Name      string    `json:"name"`   // User label, e.g. "Production Ingestion"
	Prefix    string    `json:"prefix"` // Display preview, e.g. "sm_live_9f82...abcd"
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

func apiKeyRecordKey(key string) []byte {
	return []byte("apikey:" + key)
}

// GenerateAPIKey creates a secure random API key with "sm_live_" prefix.
func GenerateAPIKey(name string) (*APIKeyRecord, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	rawKey := "sm_live_" + hex.EncodeToString(b)
	prefix := rawKey[:12] + "••••••••" + rawKey[len(rawKey)-4:]

	return &APIKeyRecord{
		Key:       rawKey,
		Name:      name,
		Prefix:    prefix,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// SaveAPIKey stores or updates an API key.
func (s *Store) SaveAPIKey(k *APIKeyRecord) error {
	if s == nil || k == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(apiKeyRecordKey(k.Key), data)
	})
}

// GetAPIKey retrieves an API key by its exact string.
func (s *Store) GetAPIKey(key string) (*APIKeyRecord, error) {
	if s == nil {
		return nil, badger.ErrDBClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rec APIKeyRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(apiKeyRecordKey(key))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &rec)
		})
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// UpdateAPIKeyLastUsed sets last_used to now for the key.
func (s *Store) UpdateAPIKeyLastUsed(key string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(apiKeyRecordKey(key))
		if err != nil {
			return err
		}
		var rec APIKeyRecord
		if err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &rec)
		}); err != nil {
			return err
		}
		rec.LastUsed = time.Now().UTC()
		data, err := json.Marshal(&rec)
		if err != nil {
			return err
		}
		return txn.Set(apiKeyRecordKey(key), data)
	})
}

// DeleteAPIKey removes an API key from BadgerDB.
func (s *Store) DeleteAPIKey(key string) error {
	if s == nil {
		return badger.ErrDBClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(apiKeyRecordKey(key))
	})
}

// ListAPIKeys returns all API keys ordered by CreatedAt descending.
func (s *Store) ListAPIKeys() ([]*APIKeyRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := []byte("apikey:")
	var list []*APIKeyRecord
	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			var rec APIKeyRecord
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &rec)
			}); err == nil {
				list = append(list, &rec)
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

// HasAnyAPIKeys checks if at least one API key exists.
func (s *Store) HasAnyAPIKeys() (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := []byte("apikey:")
	has := false
	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		it.Seek(prefix)
		has = it.ValidForPrefix(prefix)
		return nil
	})
	return has, err
}
