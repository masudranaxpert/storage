package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// Admin internals (e.g. "!badger!" headroom keys) are hidden from the studio.
const badgerInternalPrefix = "!"

// AdminTableInfo describes one logical table (key prefix) in the store.
type AdminTableInfo struct {
	Name  string `json:"name"`  // prefix without trailing colon, e.g. "node"
	Count int    `json:"count"` // number of keys
	Bytes int64  `json:"bytes"` // summed value size
}

// AdminRow is one row of a table as shown in the studio grid.
type AdminRow struct {
	Key     string          `json:"key"`     // key without the table prefix
	FullKey string          `json:"full_key"`
	Size    int64           `json:"size"`
	Version uint64          `json:"version"`
	Value   json.RawMessage `json:"value"`
}

// AdminPage is a paginated slice of rows plus the total match count.
type AdminPage struct {
	Rows      []AdminRow `json:"rows"`
	Total     int        `json:"total"`
	Page      int        `json:"page"`
	Limit     int        `json:"limit"`
	TotalPage int        `json:"total_page"`
}

// AdminStats reports store-level size metrics for the header cards.
type AdminStats struct {
	LSMSize     int64 `json:"lsm_size"`     // sst files on disk
	StoredBytes int64 `json:"stored_bytes"` // actual value payload across all keys
	KeyCount    int   `json:"key_count"`
	TableCnt    int   `json:"table_count"`
}

// adminTxn runs fn inside a read-view on the raw badger handle.
func (s *Store) adminView(fn func(txn *badger.Txn) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return badger.ErrDBClosed
	}
	return s.db.View(fn)
}

// AdminTables scans the keyspace and groups it into logical tables.
func (s *Store) AdminTables() ([]AdminTableInfo, error) {
	byName := map[string]*AdminTableInfo{}

	err := s.adminView(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.KeyCopy(nil))
			if strings.HasPrefix(key, badgerInternalPrefix) {
				continue // engine internals stay hidden
			}
			name := key
			if idx := strings.IndexByte(key, ':'); idx > 0 {
				name = key[:idx]
			}
			info, ok := byName[name]
			if !ok {
				info = &AdminTableInfo{Name: name}
				byName[name] = info
			}
			info.Count++
			info.Bytes += item.ValueSize()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]AdminTableInfo, 0, len(byName))
	for _, info := range byName {
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// AdminStats combines file sizes with real data volume and table/key counts.
// Note: Badger preallocates its value-log file via mmap (2 GB by default), so
// db.Size()'s vlog number reflects reserved disk space, not stored data. The
// studio therefore reports actual value payload bytes instead.
func (s *Store) AdminStats() (*AdminStats, error) {
	lsm, _ := s.db.Size()
	tables, err := s.AdminTables()
	if err != nil {
		return nil, err
	}
	st := &AdminStats{LSMSize: lsm, TableCnt: len(tables)}
	for _, t := range tables {
		st.KeyCount += t.Count
		st.StoredBytes += t.Bytes
	}
	return st, nil
}

// fullKeyOf reassembles "prefix:key". Keys already containing a colon pass through.
func fullKeyOf(table, key string) string {
	if strings.Contains(key, ":") {
		return key
	}
	return table + ":" + key
}

// AdminListRows returns one page of rows for a table, optionally filtered by
// a substring search over key and value.
func (s *Store) AdminListRows(table, search string, page, limit int) (*AdminPage, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if page <= 0 {
		page = 1
	}
	if table == "" || strings.ContainsAny(table, "! \t") {
		return nil, fmt.Errorf("invalid table name")
	}
	search = strings.ToLower(strings.TrimSpace(search))
	prefix := []byte(table + ":")

	var all []AdminRow
	err := s.adminView(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			fullKey := string(item.KeyCopy(nil))
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			if search != "" &&
				!strings.Contains(strings.ToLower(fullKey), search) &&
				!strings.Contains(strings.ToLower(string(val)), search) {
				continue
			}
			all = append(all, AdminRow{
				Key:     strings.TrimPrefix(fullKey, string(prefix)),
				FullKey: fullKey,
				Size:    item.ValueSize(),
				Version: item.Version(),
				Value:   json.RawMessage(prettyJSON(val)),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })

	total := len(all)
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	return &AdminPage{
		Rows:      all[start:end],
		Total:     total,
		Page:      page,
		Limit:     limit,
		TotalPage: (total + limit - 1) / limit,
	}, nil
}

// AdminGetEntry fetches a single entry's value as pretty JSON.
func (s *Store) AdminGetEntry(table, key string) (*AdminRow, error) {
	full := fullKeyOf(table, key)
	var row AdminRow
	err := s.adminView(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(full))
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		row = AdminRow{
			Key:     strings.TrimPrefix(full, table+":"),
			FullKey: full,
			Size:    item.ValueSize(),
			Version: item.Version(),
			Value:   json.RawMessage(prettyJSON(val)),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// AdminSetEntry validates the payload as JSON and upserts it under table:key.
// create=true rejects overwriting an existing key so accidental edits surface.
func (s *Store) AdminSetEntry(table, key string, value json.RawMessage, create bool) error {
	if table == "" || key == "" || strings.ContainsAny(table+key, "! \t") {
		return fmt.Errorf("invalid table or key")
	}
	if !json.Valid(value) {
		return fmt.Errorf("value is not valid JSON")
	}
	full := fullKeyOf(table, key)
	compact := bytes.TrimSpace(value)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return badger.ErrDBClosed
	}
	return s.db.Update(func(txn *badger.Txn) error {
		if create {
			if _, err := txn.Get([]byte(full)); err == nil {
				return fmt.Errorf("key '%s' already exists in table '%s'", key, table)
			}
		}
		return txn.SetEntry(badger.NewEntry([]byte(full), compact))
	})
}

// AdminDeleteEntry removes a single key.
func (s *Store) AdminDeleteEntry(table, key string) error {
	full := fullKeyOf(table, key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return badger.ErrDBClosed
	}
	return s.db.Update(func(txn *badger.Txn) error {
		if err := txn.Delete([]byte(full)); err != nil {
			return err
		}
		return nil
	})
}

// AdminTruncate drops every row of a table (engine internals are protected).
func (s *Store) AdminTruncate(table string) (int, error) {
	if table == "" || strings.HasPrefix(table, badgerInternalPrefix) {
		return 0, fmt.Errorf("invalid table name")
	}
	prefix := []byte(table + ":")
	removed := 0

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0, badger.ErrDBClosed
	}

	var keys [][]byte
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			keys = append(keys, it.Item().KeyCopy(nil))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	err = s.db.Update(func(txn *badger.Txn) error {
		for _, k := range keys {
			if err := txn.Delete(k); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}

// prettyJSON renders stored bytes as indented JSON when parseable, raw otherwise.
func prettyJSON(val []byte) []byte {
	var out bytes.Buffer
	if err := json.Indent(&out, val, "", "  "); err == nil {
		return out.Bytes()
	}
	return val
}
