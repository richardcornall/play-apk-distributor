package packages_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/richardcornall/apk-distributor/packages"
)

// --- ValidatePackageName ---

func TestValidatePackageName_Valid(t *testing.T) {
	cases := []string{
		"com.example.app",
		"com.example.my_app",
		"a.b",
		"com.google.android.apps.maps",
		"org.example123.app",
	}
	for _, name := range cases {
		if err := packages.ValidatePackageName(name); err != nil {
			t.Errorf("ValidatePackageName(%q) returned unexpected error: %v", name, err)
		}
	}
}

func TestValidatePackageName_Invalid(t *testing.T) {
	cases := []string{
		"",
		"com",
		"1com.example",
		".com.example",
		"com.example.",
		"com..example",
		"com.example app",
		"com.example/app",
	}
	for _, name := range cases {
		if err := packages.ValidatePackageName(name); err == nil {
			t.Errorf("ValidatePackageName(%q) should have returned an error", name)
		}
	}
}

// --- Manager ---

func newManager(t *testing.T) *packages.Manager {
	t.Helper()
	m, err := packages.New(t.TempDir())
	if err != nil {
		t.Fatalf("packages.New: %v", err)
	}
	return m
}

func TestManager_AddAndList(t *testing.T) {
	m := newManager(t)
	if err := m.Add("com.example.one"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := m.Add("com.example.two"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	list := m.List()
	if len(list) != 2 {
		t.Fatalf("want 2 packages, got %d", len(list))
	}
}

func TestManager_ListIsSorted(t *testing.T) {
	m := newManager(t)
	_ = m.Add("com.z.app")
	_ = m.Add("com.a.app")
	_ = m.Add("com.m.app")
	list := m.List()
	if !sort.StringsAreSorted(list) {
		t.Errorf("List() result is not sorted: %v", list)
	}
}

func TestManager_Add_DuplicateReturnsError(t *testing.T) {
	m := newManager(t)
	_ = m.Add("com.example.app")
	if err := m.Add("com.example.app"); err == nil {
		t.Error("expected error on duplicate Add")
	}
}

func TestManager_Add_InvalidNameReturnsError(t *testing.T) {
	m := newManager(t)
	if err := m.Add("notapackage"); err == nil {
		t.Error("expected error for invalid package name")
	}
}

func TestManager_Remove(t *testing.T) {
	m := newManager(t)
	_ = m.Add("com.example.app")
	if err := m.Remove("com.example.app"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if m.Has("com.example.app") {
		t.Error("package still present after Remove")
	}
}

func TestManager_Remove_NotPresentReturnsError(t *testing.T) {
	m := newManager(t)
	if err := m.Remove("com.example.app"); err == nil {
		t.Error("expected error removing non-existent package")
	}
}

func TestManager_Has(t *testing.T) {
	m := newManager(t)
	_ = m.Add("com.example.app")
	if !m.Has("com.example.app") {
		t.Error("Has() returned false for present package")
	}
	if m.Has("com.other.app") {
		t.Error("Has() returned true for absent package")
	}
}

func TestManager_Seed_NoErrorForDuplicates(t *testing.T) {
	m := newManager(t)
	_ = m.Add("com.example.one")
	if err := m.Seed([]string{"com.example.one", "com.example.two"}); err != nil {
		t.Fatalf("Seed returned error for partial duplicate: %v", err)
	}
	if len(m.List()) != 2 {
		t.Errorf("want 2 packages after Seed, got %d", len(m.List()))
	}
}

func TestManager_Seed_InvalidNameReturnsError(t *testing.T) {
	m := newManager(t)
	if err := m.Seed([]string{"com.valid.app", "bad"}); err == nil {
		t.Error("Seed should return error for invalid name")
	}
}

func TestManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	m, err := packages.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Add("com.example.app")

	// Open a second manager from the same directory.
	m2, err := packages.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m2.Has("com.example.app") {
		t.Error("package not persisted — second Manager doesn't see it")
	}
}

func TestManager_PersistenceFileIsJSON(t *testing.T) {
	dir := t.TempDir()
	m, _ := packages.New(dir)
	_ = m.Add("com.example.app")
	_ = m.Add("com.example.other")

	b, err := os.ReadFile(filepath.Join(dir, "packages.json"))
	if err != nil {
		t.Fatalf("packages.json not written: %v", err)
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		t.Fatalf("packages.json is not valid JSON: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("want 2 entries in packages.json, got %d", len(names))
	}
}

func TestManager_ConcurrentAddRemove(t *testing.T) {
	m := newManager(t)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "com.example." + string(rune('a'+i))
			_ = m.Add(name)
		}(i)
	}
	wg.Wait()
	if len(m.List()) != n {
		t.Errorf("want %d packages after concurrent adds, got %d", n, len(m.List()))
	}
}

func TestManager_OnChangeFires(t *testing.T) {
	m := newManager(t)
	done := make(chan []string, 1)
	m.OnChange(func(list []string) {
		done <- list
	})
	_ = m.Add("com.example.app")
	list := <-done
	if len(list) != 1 || list[0] != "com.example.app" {
		t.Errorf("OnChange list unexpected: %v", list)
	}
}

// TestManager_WatchAndSeed verifies that starting Watch() then immediately
// calling Seed() does not produce a file-lock error — the race that occurs on
// Windows where fsnotify fires before the write handle is released.
func TestManager_WatchAndSeed(t *testing.T) {
	dir := t.TempDir()
	m, err := packages.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Watch(); err != nil {
		t.Fatal(err)
	}
	// Seed writes packages.json, which fires fsnotify immediately on Windows.
	// The retry loop in load() must absorb any transient lock without error.
	if err := m.Seed([]string{"com.example.one", "com.example.two"}); err != nil {
		t.Fatalf("Seed after Watch returned error: %v", err)
	}
	// Allow the fsnotify goroutine time to process the event.
	time.Sleep(200 * time.Millisecond)
	list := m.List()
	if len(list) != 2 {
		t.Errorf("want 2 packages after Watch+Seed, got %d: %v", len(list), list)
	}
}

// TestManager_WatchPicksUpExternalEdit verifies that editing packages.json on
// disk (simulating a user or another process) is picked up by the watcher.
func TestManager_WatchPicksUpExternalEdit(t *testing.T) {
	dir := t.TempDir()
	m, err := packages.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Add("com.example.original")
	if err := m.Watch(); err != nil {
		t.Fatal(err)
	}

	changed := make(chan []string, 1)
	m.OnChange(func(list []string) { changed <- list })

	// Simulate an external process rewriting packages.json.
	updated, _ := json.Marshal([]string{"com.example.original", "com.example.added"})
	if err := os.WriteFile(filepath.Join(dir, "packages.json"), updated, 0640); err != nil {
		t.Fatal(err)
	}

	select {
	case list := <-changed:
		if len(list) != 2 {
			t.Errorf("want 2 packages after external edit, got %d: %v", len(list), list)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Watch to pick up external edit")
	}
}

// TestManager_RapidSeedAndWatch stress-tests the Watch+Seed race by running
// multiple Seed calls immediately after Watch starts.
func TestManager_RapidSeedAndWatch(t *testing.T) {
	for i := 0; i < 5; i++ {
		dir := t.TempDir()
		m, err := packages.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Watch(); err != nil {
			t.Fatal(err)
		}
		if err := m.Seed([]string{"com.example.app"}); err != nil {
			t.Fatalf("iteration %d: Seed after Watch returned error: %v", i, err)
		}
	}
}

func TestManager_ListReturnsIsolatedCopy(t *testing.T) {
	m := newManager(t)
	_ = m.Add("com.example.app")
	list := m.List()
	list[0] = "mutated"
	if m.Has("mutated") {
		t.Error("List() returned a reference into internal state")
	}
}
