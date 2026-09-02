package db

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dgraph-io/badger/v4"
	"stream/pkg/cluster"
	"stream/pkg/storage"
)

// Store handles persistent cluster state and telemetry in BadgerDB.
type Store struct {
	mu sync.RWMutex
	db *badger.DB
}

// NodeConfig represents persistent configurations and admin allocations for a node.
type NodeConfig struct {
	NodeID            string `json:"node_id"`
	AllocatedMaxBytes uint64 `json:"allocated_max_bytes"` // 0 = unlimited / full capacity
	CustomLabel       string `json:"custom_label"`
	Enabled           bool   `json:"enabled"`

	// Processing worker reservation (Kubernetes-requests style): the node only
	// receives processing jobs while its live free resources cover the request.
	ProcessingEnabled    bool   `json:"processing_enabled"`
	ReservedCPUCores     int    `json:"reserved_cpu_cores"`
	ReservedRAMBytes     uint64 `json:"reserved_ram_bytes"`
	PreferredDiskType    string `json:"preferred_disk_type"`     // NVME/SSD/HDD, "" = any
	ReservedStorageBytes uint64 `json:"reserved_storage_bytes"`  // scratch-space floor, 0 = no check

	SSHHost     string `json:"ssh_host,omitempty"`
	SSHUser     string `json:"ssh_user,omitempty"`
	SSHPassword string `json:"ssh_password,omitempty"`
	SSHPort     int    `json:"ssh_port,omitempty"`
	SSHUseSudo  bool   `json:"ssh_use_sudo,omitempty"`
}

// Open initializes BadgerDB at the specified directory.
func Open(dirPath string) (*Store, error) {
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	opts := badger.DefaultOptions(dirPath)
	opts.Logger = nil

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db at %s: %w", dirPath, err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying Badger database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SaveNode stores or updates a node telemetry record.
func (s *Store) SaveNode(record *cluster.NodeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(record)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("node:%s", record.Metrics.NodeID))
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// GetNode retrieves a specific node record by its ID.
func (s *Store) GetNode(nodeID string) (*cluster.NodeRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var record cluster.NodeRecord
	key := []byte(fmt.Sprintf("node:%s", nodeID))

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &record)
		})
	})

	if err != nil {
		return nil, err
	}
	return &record, nil
}

// GetAllNodes retrieves all persisted node records.
func (s *Store) GetAllNodes() ([]*cluster.NodeRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*cluster.NodeRecord
	prefix := []byte("node:")

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			err := item.Value(func(val []byte) error {
				var rec cluster.NodeRecord
				if err := json.Unmarshal(val, &rec); err == nil {
					list = append(list, &rec)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	return list, err
}

// SaveNodeConfig persists admin quota and allocation limits for a node.
func (s *Store) SaveNodeConfig(cfg *NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("config:%s", cfg.NodeID))
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, data)
	})
}

// GetNodeConfig retrieves admin quota and allocation for a node.
func (s *Store) GetNodeConfig(nodeID string) (*NodeConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var cfg NodeConfig
	key := []byte(fmt.Sprintf("config:%s", nodeID))

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &cfg)
		})
	})

	if err == badger.ErrKeyNotFound {
		return &NodeConfig{NodeID: nodeID, Enabled: true}, nil
	}
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// IsNodeEnabled reports whether a node is permitted to rejoin the cluster.
// The second return value is false when no admin decision exists yet.
func (s *Store) IsNodeEnabled(nodeID string) (bool, bool) {
	cfg, err := s.GetNodeConfig(nodeID)
	if err != nil || cfg == nil {
		return false, false
	}
	return cfg.Enabled, true
}

// DeleteNode removes a node and its configuration from BadgerDB.
func (s *Store) DeleteNode(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	nodeKey := []byte(fmt.Sprintf("node:%s", nodeID))
	configKey := []byte(fmt.Sprintf("config:%s", nodeID))

	return s.db.Update(func(txn *badger.Txn) error {
		_ = txn.Delete(nodeKey)
		_ = txn.Delete(configKey)
		return nil
	})
}

// DeleteNodeRecord removes only the persisted telemetry record, keeping the
// admin configuration (enabled flag, quotas, SSH creds) intact. Decommission
// decisions must survive so a ghost agent's heartbeats stay rejected forever.
func (s *Store) DeleteNodeRecord(nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(fmt.Sprintf("node:%s", nodeID)))
	})
}

// GetDataDir returns the root data folder path.
func GetDataDir() string {
	if v := strings.TrimSpace(os.Getenv("STREAM_DATA_DIR")); v != "" {
		return filepath.Join(v, "badger")
	}
	return filepath.Join(".", "data", "badger")
}

func tierKey(id int) []byte {
	return []byte(fmt.Sprintf("tier:%06d", id))
}

// SaveTier persists a storage tier definition.
func (s *Store) SaveTier(t *storage.Tier) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(tierKey(t.ID), data)
	})
}

// GetTier retrieves a tier definition by ID.
func (s *Store) GetTier(id int) (*storage.Tier, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var tier storage.Tier
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(tierKey(id))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &tier)
		})
	})
	if err != nil {
		return nil, err
	}
	return &tier, nil
}

// GetAllTiers retrieves every persisted tier definition.
func (s *Store) GetAllTiers() ([]*storage.Tier, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []*storage.Tier
	prefix := []byte("tier:")

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			ok := item.Value(func(val []byte) error {
				var t storage.Tier
				if err := json.Unmarshal(val, &t); err == nil {
					list = append(list, &t)
				}
				return nil
			})
			if ok != nil {
				return ok
			}
		}
		return nil
	})
	return list, err
}

// DeleteTier removes a tier definition.
func (s *Store) DeleteTier(id int) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(tierKey(id))
	})
}

// GetAllNodeConfigs returns every persisted node configuration.
func (s *Store) GetAllNodeConfigs() (map[string]*NodeConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]*NodeConfig)
	prefix := []byte("config:")

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			ok := item.Value(func(val []byte) error {
				var cfg NodeConfig
				if err := json.Unmarshal(val, &cfg); err == nil && cfg.NodeID != "" {
					out[cfg.NodeID] = &cfg
				}
				return nil
			})
			if ok != nil {
				return ok
			}
		}
		return nil
	})
	return out, err
}
