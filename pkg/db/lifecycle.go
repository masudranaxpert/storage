package db

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"stream/pkg/lifecycle"
)

const (
	flowPolicyKey  = "flow:policy"
	lcStatePrefix  = "lcstate:"
	lcTaskPrefix   = "lctask:"
	lcActivePrefix = "lcactive:"
	lcAuditPrefix  = "lcaudit:"
)

func lcStateKey(key string) []byte  { return []byte(lcStatePrefix + key) }
func lcTaskKey(id string) []byte    { return []byte(lcTaskPrefix + id) }
func lcActiveKey(key string) []byte { return []byte(lcActivePrefix + key) }
func lcAuditKey(at time.Time, key string) []byte {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return []byte(fmt.Sprintf("%s%020d-%s", lcAuditPrefix, at.UTC().UnixNano(), key))
}

// GetFlowPolicy loads the single flow:policy document, returning (nil, nil) if not yet seeded.
func (s *Store) GetFlowPolicy() (*lifecycle.FlowPolicy, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var p lifecycle.FlowPolicy
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(flowPolicyKey))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &p)
		})
	})
	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// SaveFlowPolicy persists the flow:policy document.
func (s *Store) SaveFlowPolicy(p *lifecycle.FlowPolicy) error {
	if s == nil || p == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(flowPolicyKey), data)
	})
}

// GetLifecycleState fetches the lcstate:<key> row, returning (nil, nil) when absent.
func (s *Store) GetLifecycleState(key string) (*lifecycle.FileState, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var st lifecycle.FileState
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(lcStateKey(key))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &st)
		})
	})
	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// SaveLifecycleState upserts the lcstate:<key> row.
func (s *Store) SaveLifecycleState(st *lifecycle.FileState) error {
	if s == nil || st == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(lcStateKey(st.Key), data)
	})
}

// DeleteLifecycleState removes the lcstate:<key> row.
func (s *Store) DeleteLifecycleState(key string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(lcStateKey(key))
	})
}

// ListLifecycleStates returns all per-file lifecycle state rows.
func (s *Store) ListLifecycleStates() ([]*lifecycle.FileState, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*lifecycle.FileState
	prefix := []byte(lcStatePrefix)

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				var st lifecycle.FileState
				if err := json.Unmarshal(val, &st); err == nil {
					list = append(list, &st)
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
	return list, nil
}

// GetLifecycleTask fetches the lctask:<id> row, returning (nil, nil) when absent.
func (s *Store) GetLifecycleTask(id string) (*lifecycle.Task, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var t lifecycle.Task
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(lcTaskKey(id))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &t)
		})
	})
	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// SaveLifecycleTask upserts the lctask:<id> row.
func (s *Store) SaveLifecycleTask(t *lifecycle.Task) error {
	if s == nil || t == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	t.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(lcTaskKey(t.ID), data)
	})
}

// ListLifecycleTasks returns all lifecycle tasks ordered newest first.
func (s *Store) ListLifecycleTasks() ([]*lifecycle.Task, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*lifecycle.Task
	prefix := []byte(lcTaskPrefix)

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				var t lifecycle.Task
				if err := json.Unmarshal(val, &t); err == nil {
					list = append(list, &t)
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

// GetActiveLifecycleTask returns the active task ID for a file key, or "" if none.
func (s *Store) GetActiveLifecycleTask(key string) (string, error) {
	if s == nil {
		return "", nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var taskID string
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(lcActiveKey(key))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if err := json.Unmarshal(val, &taskID); err != nil {
				taskID = strings.TrimSpace(string(val))
			}
			return nil
		})
	})
	if err == badger.ErrKeyNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return taskID, nil
}

// SetActiveLifecycleTask sets lcactive:<key> to taskID.
func (s *Store) SetActiveLifecycleTask(key, taskID string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(taskID)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(lcActiveKey(key), data)
	})
}

// DeleteActiveLifecycleTask removes lcactive:<key>.
func (s *Store) DeleteActiveLifecycleTask(key string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(lcActiveKey(key))
	})
}

// ListActiveLifecycleTasks returns the map of file key -> active task ID.
func (s *Store) ListActiveLifecycleTasks() (map[string]string, error) {
	out := make(map[string]string)
	if s == nil {
		return out, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := []byte(lcActivePrefix)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			k := strings.TrimPrefix(string(item.Key()), lcActivePrefix)
			if err := item.Value(func(val []byte) error {
				var tid string
				if err := json.Unmarshal(val, &tid); err != nil {
					tid = strings.TrimSpace(string(val))
				}
				if k != "" && tid != "" {
					out[k] = tid
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
	return out, nil
}

// SaveLifecycleAudit writes an immutable expire audit row under lcaudit:<ts>-<key>.
func (s *Store) SaveLifecycleAudit(entry *lifecycle.AuditEntry) error {
	if s == nil || entry == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.At.IsZero() {
		entry.At = time.Now().UTC()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(lcAuditKey(entry.At, entry.Key), data)
	})
}

// ListLifecycleAudits returns expire audit entries ordered newest first.
func (s *Store) ListLifecycleAudits(limit int) ([]*lifecycle.AuditEntry, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*lifecycle.AuditEntry
	prefix := []byte(lcAuditPrefix)

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				var a lifecycle.AuditEntry
				if err := json.Unmarshal(val, &a); err == nil {
					list = append(list, &a)
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
		return list[i].At.After(list[j].At)
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

// DeleteLifecycleTask removes an individual lifecycle task record by ID.
func (s *Store) DeleteLifecycleTask(id string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(lcTaskKey(id))
	})
}

// PruneLifecycleTasks removes terminal (done, failed, cancelled) tasks older than maxAge.
func (s *Store) PruneLifecycleTasks(maxAge time.Duration) (int, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var toDelete [][]byte
	cutoff := time.Now().UTC().Add(-maxAge)
	prefix := []byte(lcTaskPrefix)

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			k := item.KeyCopy(nil)
			if err := item.Value(func(val []byte) error {
				var t lifecycle.Task
				if err := json.Unmarshal(val, &t); err == nil {
					if t.State.IsTerminal() && (t.UpdatedAt.Before(cutoff) || t.CreatedAt.Before(cutoff)) {
						toDelete = append(toDelete, k)
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	if len(toDelete) == 0 {
		return 0, nil
	}

	err = s.db.Update(func(txn *badger.Txn) error {
		for _, k := range toDelete {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return len(toDelete), err
}

// PruneLifecycleAudits removes audit entries older than maxAge or exceeding maxEntries.
func (s *Store) PruneLifecycleAudits(maxEntries int, maxAge time.Duration) (int, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	type auditKeyEntry struct {
		key []byte
		at  time.Time
	}
	var entries []auditKeyEntry
	cutoff := time.Now().UTC().Add(-maxAge)
	prefix := []byte(lcAuditPrefix)

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			k := item.KeyCopy(nil)
			if err := item.Value(func(val []byte) error {
				var a lifecycle.AuditEntry
				if err := json.Unmarshal(val, &a); err == nil {
					entries = append(entries, auditKeyEntry{key: k, at: a.At})
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].at.After(entries[j].at)
	})

	var toDelete [][]byte
	for i, e := range entries {
		if (maxEntries > 0 && i >= maxEntries) || (maxAge > 0 && e.at.Before(cutoff)) {
			toDelete = append(toDelete, e.key)
		}
	}

	if len(toDelete) == 0 {
		return 0, nil
	}

	err = s.db.Update(func(txn *badger.Txn) error {
		for _, k := range toDelete {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return len(toDelete), err
}
