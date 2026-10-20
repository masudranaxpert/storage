package database

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/dgraph-io/badger/v4"
	"stream/internal/model"
)

func fileRecordKey(key string) []byte {
	return []byte("file:" + key)
}

func fileJobKey(key string) []byte {
	return []byte("fjob:" + key)
}

// SaveFileRecord upserts the main-table row for a file.
func (s *Store) SaveFileRecord(rec *model.FileRecord) error {
	if s == nil || rec == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(fileRecordKey(rec.Key), data)
	})
}

// unmarshalJSONWithFallback handles both standard JSON and double-quoted JSON strings.
func unmarshalJSONWithFallback(val []byte, target interface{}) error {
	if err := json.Unmarshal(val, target); err == nil {
		return nil
	}
	var unquoted string
	if err := json.Unmarshal(val, &unquoted); err == nil {
		return json.Unmarshal([]byte(unquoted), target)
	}
	return json.Unmarshal(val, target)
}

// GetFileRecord fetches the main-table row for a key.
func (s *Store) GetFileRecord(key string) (*model.FileRecord, error) {
	if s == nil {
		return nil, badger.ErrDBClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rec model.FileRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(fileRecordKey(key))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return unmarshalJSONWithFallback(val, &rec)
		})
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// FileRecordExists is a cheap membership probe used by key generation.
func (s *Store) FileRecordExists(key string) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	err := s.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(fileRecordKey(key))
		return err
	})
	if err == badger.ErrKeyNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// DeleteFileRecord removes the main-table row.
func (s *Store) DeleteFileRecord(key string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(fileRecordKey(key))
	})
}

// ListFileRecords returns every file record, newest first.
func (s *Store) ListFileRecords() ([]*model.FileRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*model.FileRecord
	prefix := []byte("file:")

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			ok := item.Value(func(val []byte) error {
				var rec model.FileRecord
				if err := unmarshalJSONWithFallback(val, &rec); err == nil && rec.Key != "" {
					list = append(list, &rec)
				}
				return nil
			})
			if ok != nil {
				return ok
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

// SaveFileJob upserts the job-table row for a file (FK: FileJob.Key).
func (s *Store) SaveFileJob(job *model.FileJob) error {
	if s == nil || job == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	job.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(fileJobKey(job.Key), data)
	})
}

// GetFileJob fetches the job-table row for a key.
func (s *Store) GetFileJob(key string) (*model.FileJob, error) {
	if s == nil {
		return nil, badger.ErrDBClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var job model.FileJob
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(fileJobKey(key))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return unmarshalJSONWithFallback(val, &job)
		})
	})
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ListFileJobs returns every job row, newest first.
func (s *Store) ListFileJobs() ([]*model.FileJob, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*model.FileJob
	prefix := []byte("fjob:")

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			ok := item.Value(func(val []byte) error {
				var job model.FileJob
				if err := unmarshalJSONWithFallback(val, &job); err == nil && job.Key != "" {
					list = append(list, &job)
				}
				return nil
			})
			if ok != nil {
				return ok
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

// DeleteFileJob removes the job-table row.
func (s *Store) DeleteFileJob(key string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(fileJobKey(key))
	})
}
