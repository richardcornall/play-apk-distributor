package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry records the last successfully extracted version of a package.
type Entry struct {
	VersionCode int       `json:"version_code"`
	VersionName string    `json:"version_name"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Store is a thread-safe, file-backed version registry.
type Store struct {
	mu   sync.RWMutex
	path string
	data map[string]Entry
}

// Open loads (or creates) the store at outputDir/state/store.json.
func Open(outputDir string) (*Store, error) {
	dir := filepath.Join(outputDir, "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "store.json")
	s := &Store{path: path, data: make(map[string]Entry)}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read store: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("parse store: %w", err)
	}
	return s, nil
}

// GetLastKnown returns the stored entry for pkg, or false if never seen.
func (s *Store) GetLastKnown(pkg string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[pkg]
	return e, ok
}

// SetLastKnown records a successful extraction for pkg.
func (s *Store) SetLastKnown(pkg string, versionCode int, versionName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[pkg] = Entry{
		VersionCode: versionCode,
		VersionName: versionName,
		UpdatedAt:   time.Now().UTC(),
	}
	return s.flush()
}

// flush writes the store atomically via a temp file + rename.
func (s *Store) flush() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return fmt.Errorf("write store tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit store: %w", err)
	}
	return nil
}
