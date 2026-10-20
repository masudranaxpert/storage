package db

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	userTablePrefix    = "user:"
	sessionTablePrefix = "session:"
	authConfigKey      = "auth_config:admin"
)

// UserRecord represents an authenticated administrator account in BadgerDB.
type UserRecord struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SessionRecord represents an active user session token in BadgerDB.
type SessionRecord struct {
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func userKey(username string) []byte {
	return []byte(userTablePrefix + strings.ToLower(strings.TrimSpace(username)))
}

func sessionKey(token string) []byte {
	return []byte(sessionTablePrefix + token)
}

// HasAnyUsers checks if at least one admin account exists in BadgerDB.
func (s *Store) HasAnyUsers() (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	has := false
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte(userTablePrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			has = true
			break
		}
		return nil
	})
	return has, err
}

// CreateUser hashes the password with bcrypt and stores the new user in BadgerDB.
func (s *Store) CreateUser(username, password string) (*UserRecord, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) < 3 {
		return nil, errors.New("username must be at least 3 characters")
	}
	if len(password) < 6 {
		return nil, errors.New("password must be at least 6 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	now := time.Now().UTC()
	user := &UserRecord{
		Username:     username,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	data, err := json.Marshal(user)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err = s.db.Update(func(txn *badger.Txn) error {
		// Prevent overwriting existing user with CreateUser
		_, err := txn.Get(userKey(username))
		if err == nil {
			return fmt.Errorf("user '%s' already exists", username)
		}
		if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		return txn.Set(userKey(username), data)
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// AuthenticateUser validates username and password using bcrypt.
func (s *Store) AuthenticateUser(username, password string) (*UserRecord, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" || password == "" {
		return nil, errors.New("invalid credentials")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var user UserRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(userKey(username))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &user)
		})
	})
	if err != nil {
		return nil, errors.New("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, errors.New("invalid credentials")
	}
	return &user, nil
}

// UpdateUserPassword updates the password of an existing user.
func (s *Store) UpdateUserPassword(username, oldPassword, newPassword string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(newPassword) < 6 {
		return errors.New("new password must be at least 6 characters")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(userKey(username))
		if err != nil {
			return errors.New("user not found")
		}

		var user UserRecord
		if err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &user)
		}); err != nil {
			return err
		}

		if oldPassword != "" {
			if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)); err != nil {
				return errors.New("current password incorrect")
			}
		}

		newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("failed to hash new password: %w", err)
		}

		user.PasswordHash = string(newHash)
		user.UpdatedAt = time.Now().UTC()

		data, err := json.Marshal(user)
		if err != nil {
			return err
		}
		return txn.Set(userKey(username), data)
	})
}

// CreateSession creates a secure 256-bit random session token valid for the given duration.
func (s *Store) CreateSession(username string, ttl time.Duration) (*SessionRecord, error) {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour // Default 7 days
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(b)
	now := time.Now().UTC()

	sess := &SessionRecord{
		Token:     token,
		Username:  strings.ToLower(strings.TrimSpace(username)),
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}

	data, err := json.Marshal(sess)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err = s.db.Update(func(txn *badger.Txn) error {
		e := badger.NewEntry(sessionKey(token), data).WithTTL(ttl)
		return txn.SetEntry(e)
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// ValidateSession verifies if a session token is active and non-expired.
func (s *Store) ValidateSession(token string) (*SessionRecord, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("empty token")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var sess SessionRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(sessionKey(token))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &sess)
		})
	})
	if err != nil {
		return nil, errors.New("invalid or expired session")
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		return nil, errors.New("session expired")
	}
	return &sess, nil
}

// DeleteSession invalidates an active session token.
func (s *Store) DeleteSession(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(sessionKey(token))
	})
}

// ListUsers returns all registered administrator accounts (excluding password hashes).
func (s *Store) ListUsers() ([]*UserRecord, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var users []*UserRecord
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte(userTablePrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				var u UserRecord
				if err := json.Unmarshal(val, &u); err != nil {
					return nil
				}
				u.PasswordHash = "" // never expose hash
				users = append(users, &u)
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return users, err
}

// DeleteUser deletes an administrator account by username.
func (s *Store) DeleteUser(username string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return errors.New("empty username")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		key := userKey(username)
		_, err := txn.Get(key)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return errors.New("user not found")
			}
			return err
		}
		return txn.Delete(key)
	})
}

const clusterSecretKey = "cluster:secret"

// GetOrSetClusterSecret returns the persistent cluster secret from BadgerDB.
// If explicitSecret is provided, it saves and returns it.
// If no secret exists in DB, it generates a secure 32-byte hex secret, persists it, and returns it.
func (s *Store) GetOrSetClusterSecret(explicitSecret string) (string, error) {
	if s == nil || s.db == nil {
		if explicitSecret != "" {
			return explicitSecret, nil
		}
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b), nil
	}

	explicitSecret = strings.TrimSpace(explicitSecret)
	if explicitSecret != "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		err := s.db.Update(func(txn *badger.Txn) error {
			return txn.Set([]byte(clusterSecretKey), []byte(explicitSecret))
		})
		return explicitSecret, err
	}

	s.mu.RLock()
	var existing string
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(clusterSecretKey))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			existing = string(val)
			return nil
		})
	})
	s.mu.RUnlock()

	if err == nil && existing != "" {
		return existing, nil
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate cluster secret: %w", err)
	}
	generated := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(clusterSecretKey), []byte(generated))
	})
	if err != nil {
		return "", err
	}
	return generated, nil
}

