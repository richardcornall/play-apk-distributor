// Package packages manages the dynamic list of Android packages to track.
// It is the programmatic alternative to editing config.yaml directly —
// the HTTP API and any importer use Manager to add or remove packages at
// runtime without restarting the service.
//
// The list is persisted to packages.json in the provided directory.
// If the file does not exist it is created on the first write.
// External edits are detected via fsnotify and applied without restart.
package packages

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
	"sync"

	"github.com/fsnotify/fsnotify"
)

var validPackageName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)

// ValidatePackageName returns an error if name is not a valid Android package name.
// Exported so API handlers and importers can validate before calling Add.
func ValidatePackageName(name string) error {
	if !validPackageName.MatchString(name) {
		return fmt.Errorf("invalid package name %q — must be dot-separated identifiers starting with a letter", name)
	}
	return nil
}

// Manager maintains a persisted, hot-reloadable set of package names.
type Manager struct {
	mu       sync.RWMutex
	path     string
	set      map[string]bool
	handlers []func([]string)
}

// New opens (or creates) the package list at dir/packages.json.
func New(dir string) (*Manager, error) {
	m := &Manager{
		path: filepath.Join(dir, "packages.json"),
		set:  make(map[string]bool),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Add adds name to the list. Returns an error if the name is invalid or already present.
func (m *Manager) Add(name string) error {
	if err := ValidatePackageName(name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set[name] {
		return fmt.Errorf("package %q is already tracked", name)
	}
	m.set[name] = true
	if err := m.flush(); err != nil {
		delete(m.set, name)
		return err
	}
	m.notify()
	return nil
}

// Remove removes name from the list. Returns an error if the name is not present.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set[name] {
		return fmt.Errorf("package %q is not tracked", name)
	}
	delete(m.set, name)
	if err := m.flush(); err != nil {
		m.set[name] = true
		return err
	}
	m.notify()
	return nil
}

// List returns a sorted copy of the current package names.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sorted()
}

// Has reports whether name is currently tracked.
func (m *Manager) Has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.set[name]
}

// Seed adds any names not already present without returning an error for
// duplicates. Used by main to seed the manager from config.yaml on first run.
func (m *Manager) Seed(names []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	added := []string{}
	for _, name := range names {
		if err := ValidatePackageName(name); err != nil {
			return err
		}
		if !m.set[name] {
			m.set[name] = true
			added = append(added, name)
		}
	}
	if len(added) == 0 {
		return nil
	}
	if err := m.flush(); err != nil {
		for _, name := range added {
			delete(m.set, name)
		}
		return err
	}
	m.notify()
	return nil
}

// OnChange registers fn to be called (without the lock held) after every
// successful change to the package list, whether triggered via Add/Remove
// or an external file edit picked up by Watch.
func (m *Manager) OnChange(fn func([]string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers = append(m.handlers, fn)
}

// Watch starts background monitoring of packages.json for external edits.
func (m *Manager) Watch() error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	// Watch the directory — the file may not exist yet.
	if err := w.Add(filepath.Dir(m.path)); err != nil {
		w.Close()
		return err
	}
	go func() {
		defer w.Close()
		for {
			select {
			case event, ok := <-w.Events:
				if !ok {
					return
				}
				if event.Name != m.path {
					continue
				}
				if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
					continue
				}
				if err := m.load(); err != nil {
					slog.Error("packages reload failed", "err", err)
					continue
				}
				slog.Info("packages reloaded from file", "count", len(m.List()))
				m.mu.RLock()
				handlers := append(([]func([]string))(nil), m.handlers...)
				m.mu.RUnlock()
				for _, fn := range handlers {
					go fn(m.List())
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				slog.Error("packages watcher error", "err", err)
			}
		}
	}()
	return nil
}

func (m *Manager) load() error {
	// Retry up to 3 times with a short sleep — on Windows, fsnotify can fire
	// before the writing process has fully released the file handle.
	var b []byte
	var err error
	for i := 0; i < 3; i++ {
		b, err = os.ReadFile(m.path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil // first run — empty list is fine
	}
	if err != nil {
		return fmt.Errorf("read packages.json: %w", err)
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return fmt.Errorf("parse packages.json: %w", err)
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		if err := ValidatePackageName(name); err != nil {
			return fmt.Errorf("packages.json contains %w", err)
		}
		set[name] = true
	}
	m.mu.Lock()
	m.set = set
	m.mu.Unlock()
	return nil
}

// flush writes the current set to packages.json atomically.
// Caller must hold m.mu (write lock).
func (m *Manager) flush() error {
	b, err := json.MarshalIndent(m.sorted(), "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return fmt.Errorf("write packages.json tmp: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit packages.json: %w", err)
	}
	return nil
}

// sorted returns a sorted slice from the current set. Caller must hold at least a read lock.
func (m *Manager) sorted() []string {
	out := make([]string, 0, len(m.set))
	for name := range m.set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// notify calls all registered handlers with the current list.
// Caller must hold m.mu (write lock). Handlers are called without the lock.
func (m *Manager) notify() {
	list := m.sorted()
	handlers := append(([]func([]string))(nil), m.handlers...)
	go func() {
		for _, fn := range handlers {
			fn(list)
		}
	}()
}
