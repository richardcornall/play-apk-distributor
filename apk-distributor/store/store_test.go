package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestOpen_NewStore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, ok := s.GetLastKnown("com.example.app")
	if ok {
		t.Error("expected empty store, got an entry")
	}
}

func TestOpen_StateDir_Permissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("state is not a directory")
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0700 {
			t.Errorf("state dir permissions = %o, want 0700", perm)
		}
	}
}

func TestSetAndGet(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastKnown("com.example.app", 1042, "4.2.1"); err != nil {
		t.Fatalf("SetLastKnown: %v", err)
	}
	e, ok := s.GetLastKnown("com.example.app")
	if !ok {
		t.Fatal("entry not found after set")
	}
	if e.VersionCode != 1042 {
		t.Errorf("VersionCode = %d, want 1042", e.VersionCode)
	}
	if e.VersionName != "4.2.1" {
		t.Errorf("VersionName = %q, want 4.2.1", e.VersionName)
	}
	if e.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.SetLastKnown("com.example.app", 1042, "4.2.1"); err != nil {
		t.Fatal(err)
	}

	// Re-open from same directory — data must survive.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := s2.GetLastKnown("com.example.app")
	if !ok {
		t.Fatal("entry not found after re-open")
	}
	if e.VersionCode != 1042 {
		t.Errorf("VersionCode = %d, want 1042", e.VersionCode)
	}
}

func TestAtomicWrite_NoPartialState(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := s.SetLastKnown("com.example.app", i+1, "1.0"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		// Each write must leave a valid JSON file.
		b, err := os.ReadFile(filepath.Join(dir, "state", "store.json"))
		if err != nil {
			t.Fatalf("read after write %d: %v", i, err)
		}
		var m map[string]Entry
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("invalid JSON after write %d: %v", i, err)
		}
	}
}

func TestStoreFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastKnown("com.example.app", 1, "1.0"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "state", "store.json"))
	if err != nil {
		t.Fatalf("store.json not found: %v", err)
	}
	if info.IsDir() {
		t.Error("store.json should be a file, not a directory")
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("store.json permissions = %o, want 0600", perm)
		}
	}
}

func TestGetLastKnown_Unknown(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, ok := s.GetLastKnown("com.never.seen")
	if ok {
		t.Error("expected false for unknown package")
	}
}

func TestOverwrite(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetLastKnown("com.example.app", 100, "1.0.0")
	_ = s.SetLastKnown("com.example.app", 200, "2.0.0")

	e, ok := s.GetLastKnown("com.example.app")
	if !ok || e.VersionCode != 200 {
		t.Errorf("expected version 200, got %+v (ok=%v)", e, ok)
	}
}

func TestConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 50
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pkg := "com.stress.pkg" + string(rune('a'+i%26))
			errs[i] = s.SetLastKnown(pkg, i+1, "1.0")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	b, err := os.ReadFile(filepath.Join(dir, "state", "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]Entry
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("corrupt store.json after concurrent writes: %v", err)
	}
}

func TestConcurrentReadsAndWrites(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetLastKnown("com.example.app", 1, "1.0")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s.SetLastKnown("com.example.app", i+2, "1.0") //nolint:errcheck
		}(i)
		go func() {
			defer wg.Done()
			s.GetLastKnown("com.example.app")
		}()
	}
	wg.Wait()
}

func TestUpdatedAt_IsUTC(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	_ = s.SetLastKnown("com.example.app", 1, "1.0")
	after := time.Now().UTC()

	e, _ := s.GetLastKnown("com.example.app")
	if e.UpdatedAt.Before(before) || e.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt %v outside expected range [%v, %v]", e.UpdatedAt, before, after)
	}
	if e.UpdatedAt.Location() != time.UTC {
		t.Errorf("UpdatedAt not in UTC: %v", e.UpdatedAt.Location())
	}
}

func TestOpen_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "store.json"), []byte("not valid json{{{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir)
	if err == nil {
		t.Error("expected error for corrupt store.json, got nil")
	}
}
