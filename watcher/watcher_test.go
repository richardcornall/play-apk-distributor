package watcher_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/richardcornall/play-apk-distributor/adb"
	"github.com/richardcornall/play-apk-distributor/config"
	"github.com/richardcornall/play-apk-distributor/packages"
	"github.com/richardcornall/play-apk-distributor/sink"
	"github.com/richardcornall/play-apk-distributor/store"
	"github.com/richardcornall/play-apk-distributor/watcher"
)

// fakeDevice implements adb.Device for tests. All fields are set directly.
type fakeDevice struct {
	serial      string
	abi         string
	connected   bool
	reconnected bool
	version     adb.VersionInfo
	versionErr  error
	paths       []string
	pathsErr    error
	pullErr     error
}

func (f *fakeDevice) Serial() string { return f.serial }
func (f *fakeDevice) ABI() string    { return f.abi }
func (f *fakeDevice) IsConnected() bool {
	return f.connected
}
func (f *fakeDevice) Reconnect() error {
	f.reconnected = true
	f.connected = true
	return nil
}
func (f *fakeDevice) GetInstalledVersion(_ string) (adb.VersionInfo, error) {
	return f.version, f.versionErr
}
func (f *fakeDevice) PullAPKPaths(_ string) ([]string, error) {
	return f.paths, f.pathsErr
}
func (f *fakeDevice) PullFile(_, destPath string) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	return os.WriteFile(destPath, []byte("PK fake apk"), 0640)
}

// captureSink records every artifact delivered to it.
type captureSink struct {
	artifacts []sink.Artifact
}

func (c *captureSink) OnArtifact(_ context.Context, a sink.Artifact) error {
	c.artifacts = append(c.artifacts, a)
	return nil
}

// setup builds a watcher with the given devices and returns it alongside
// the output dir, store, and packages manager.
func setup(t *testing.T, devices []adb.Device, pkgNames []string) (*watcher.Watcher, string, *captureSink) {
	t.Helper()
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.yaml")
	pkgLines := ""
	for _, p := range pkgNames {
		pkgLines += "\n  - " + p
	}
	cfgContent := "adb_host: localhost\npoll_interval: 1\noutput_dir: " + filepath.Join(dir, "apks") + "\nemulators:\n  - port: 5555\n    abi: x86_64\npackages:" + pkgLines + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0640); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	outDir := filepath.Join(dir, "apks")
	if err := os.MkdirAll(outDir, 0750); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(outDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	pkgMgr, err := packages.New(dir)
	if err != nil {
		t.Fatalf("packages.New: %v", err)
	}

	s := &captureSink{}
	w := watcher.New(cfg, pkgMgr, st, devices, s)
	return w, outDir, s
}

// runOnePoll runs a single poll cycle synchronously with a short timeout.
func runOnePoll(t *testing.T, w *watcher.Watcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Run in goroutine; cancel after a brief moment to get exactly one poll.
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
}

// --- tests ---

func TestWatcher_NewPackageTriggersExtraction(t *testing.T) {
	device := &fakeDevice{
		serial:    "localhost:5555",
		abi:       "x86_64",
		connected: true,
		version:   adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:     []string{"/data/app/com.example.app/base.apk"},
	}
	w, outDir, s := setup(t, []adb.Device{device}, []string{"com.example.app"})
	runOnePoll(t, w)

	if len(s.artifacts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(s.artifacts))
	}
	a := s.artifacts[0]
	if a.Package != "com.example.app" {
		t.Errorf("artifact package = %q", a.Package)
	}
	if a.VersionCode != 100 {
		t.Errorf("artifact version_code = %d, want 100", a.VersionCode)
	}
	if _, err := os.Stat(filepath.Join(outDir, "com.example.app", "100.apk")); err != nil {
		t.Errorf("output file not found: %v", err)
	}
}

func TestWatcher_NoUpdateSkipsExtraction(t *testing.T) {
	device := &fakeDevice{
		serial:    "localhost:5555",
		abi:       "x86_64",
		connected: true,
		version:   adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:     []string{"/data/app/com.example.app/base.apk"},
	}
	w, _, s := setup(t, []adb.Device{device}, []string{"com.example.app"})

	// First poll extracts.
	runOnePoll(t, w)
	if len(s.artifacts) != 1 {
		t.Fatalf("want 1 artifact after first poll, got %d", len(s.artifacts))
	}

	// Second poll â€” same version â€” should not extract again.
	runOnePoll(t, w)
	if len(s.artifacts) != 1 {
		t.Errorf("want no additional artifact on second poll, got %d total", len(s.artifacts))
	}
}

func TestWatcher_VersionBumpTriggersReExtraction(t *testing.T) {
	device := &fakeDevice{
		serial:    "localhost:5555",
		abi:       "x86_64",
		connected: true,
		version:   adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:     []string{"/data/app/com.example.app/base.apk"},
	}
	w, _, s := setup(t, []adb.Device{device}, []string{"com.example.app"})

	runOnePoll(t, w)
	if len(s.artifacts) != 1 {
		t.Fatalf("first poll: want 1 artifact, got %d", len(s.artifacts))
	}

	device.version = adb.VersionInfo{Code: 200, Name: "2.0"}
	runOnePoll(t, w)
	if len(s.artifacts) != 2 {
		t.Errorf("after version bump: want 2 artifacts total, got %d", len(s.artifacts))
	}
	if s.artifacts[1].VersionCode != 200 {
		t.Errorf("second artifact version_code = %d, want 200", s.artifacts[1].VersionCode)
	}
}

func TestWatcher_TwoDevicesAgreeBeforeExtraction(t *testing.T) {
	d1 := &fakeDevice{serial: "localhost:5555", abi: "x86_64", connected: true,
		version: adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:   []string{"/data/app/com.example.app/base.apk", "/data/app/com.example.app/split_config.x86_64.apk"},
	}
	d2 := &fakeDevice{serial: "localhost:5557", abi: "arm64-v8a", connected: true,
		version: adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:   []string{"/data/app/com.example.app/base.apk", "/data/app/com.example.app/split_config.arm64_v8a.apk"},
	}
	w, outDir, s := setup(t, []adb.Device{d1, d2}, []string{"com.example.app"})
	runOnePoll(t, w)

	if len(s.artifacts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(s.artifacts))
	}
	if !s.artifacts[0].IsXAPK {
		t.Error("two-device extraction should produce an XAPK")
	}
	if _, err := os.Stat(filepath.Join(outDir, "com.example.app", "100.xapk")); err != nil {
		t.Errorf("xapk output not found: %v", err)
	}
}

func TestWatcher_VersionMismatchBlocksExtraction(t *testing.T) {
	d1 := &fakeDevice{serial: "localhost:5555", abi: "x86_64", connected: true,
		version: adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:   []string{"/data/app/com.example.app/base.apk"},
	}
	d2 := &fakeDevice{serial: "localhost:5557", abi: "arm64-v8a", connected: true,
		version: adb.VersionInfo{Code: 99, Name: "0.9"}, // behind â€” not yet updated
		paths:   []string{"/data/app/com.example.app/base.apk"},
	}
	w, _, s := setup(t, []adb.Device{d1, d2}, []string{"com.example.app"})
	runOnePoll(t, w)

	if len(s.artifacts) != 0 {
		t.Errorf("version mismatch should block extraction, got %d artifacts", len(s.artifacts))
	}
}

func TestWatcher_VersionMismatchResolvesOnNextPoll(t *testing.T) {
	d1 := &fakeDevice{serial: "localhost:5555", abi: "x86_64", connected: true,
		version: adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:   []string{"/data/app/com.example.app/base.apk"},
	}
	d2 := &fakeDevice{serial: "localhost:5557", abi: "arm64-v8a", connected: true,
		version: adb.VersionInfo{Code: 99, Name: "0.9"},
		paths:   []string{"/data/app/com.example.app/base.apk"},
	}
	w, _, s := setup(t, []adb.Device{d1, d2}, []string{"com.example.app"})

	runOnePoll(t, w) // mismatch â€” no extraction
	if len(s.artifacts) != 0 {
		t.Fatalf("first poll: mismatch should block extraction")
	}

	d2.version = adb.VersionInfo{Code: 100, Name: "1.0"} // d2 catches up
	runOnePoll(t, w)
	if len(s.artifacts) != 1 {
		t.Errorf("second poll: both agree, want 1 artifact, got %d", len(s.artifacts))
	}
}

func TestWatcher_OfflineDeviceReconnects(t *testing.T) {
	device := &fakeDevice{
		serial:    "localhost:5555",
		abi:       "x86_64",
		connected: false, // starts offline
		version:   adb.VersionInfo{Code: 100, Name: "1.0"},
		paths:     []string{"/data/app/com.example.app/base.apk"},
	}
	w, _, s := setup(t, []adb.Device{device}, []string{"com.example.app"})
	runOnePoll(t, w)

	if !device.reconnected {
		t.Error("expected Reconnect() to be called for offline device")
	}
	if len(s.artifacts) != 1 {
		t.Errorf("want 1 artifact after reconnect, got %d", len(s.artifacts))
	}
}

func TestWatcher_AllOfflineNoExtraction(t *testing.T) {
	device := &fakeDevice{
		serial:    "localhost:5555",
		abi:       "x86_64",
		connected: false,
	}
	// Reconnect also fails â€” simulate a truly dead emulator.
	// We override Reconnect by leaving connected=false so GetInstalledVersion
	// will still be called but fail via versionErr.
	device.versionErr = os.ErrNotExist

	w, _, s := setup(t, []adb.Device{device}, []string{"com.example.app"})
	runOnePoll(t, w)

	if len(s.artifacts) != 0 {
		t.Errorf("all-offline: want 0 artifacts, got %d", len(s.artifacts))
	}
}

func TestWatcher_EffectivePackagesMergesManagerAndConfig(t *testing.T) {
	var extracted []string
	var count int32

	device := &fakeDevice{
		serial:    "localhost:5555",
		abi:       "x86_64",
		connected: true,
		version:   adb.VersionInfo{Code: 1, Name: "1.0"},
		paths:     []string{"/data/app/placeholder/base.apk"},
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgContent := "adb_host: localhost\npoll_interval: 1\noutput_dir: " + filepath.Join(dir, "apks") + "\nemulators:\n  - port: 5555\n    abi: x86_64\npackages:\n  - com.config.app\n"
	os.WriteFile(cfgPath, []byte(cfgContent), 0640)

	cfg, _ := config.Load(cfgPath)
	outDir := filepath.Join(dir, "apks")
	os.MkdirAll(outDir, 0750)
	st, _ := store.Open(outDir)
	pkgMgr, _ := packages.New(dir)
	_ = pkgMgr.Add("com.dynamic.app") // added via manager, not config

	s := &struct{ sink.Sink }{sink.Noop{}}
	_ = s

	// Use captureSink to record which packages were extracted.
	capSink := &captureSink{}
	w := watcher.New(cfg, pkgMgr, st, []adb.Device{device}, capSink)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		w.Run(ctx)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()

	for _, a := range capSink.artifacts {
		extracted = append(extracted, a.Package)
		atomic.AddInt32(&count, 1)
	}

	if int(count) < 2 {
		t.Errorf("want both config and dynamic packages extracted, got: %v", extracted)
	}
	hasConfig, hasDynamic := false, false
	for _, p := range extracted {
		if p == "com.config.app" {
			hasConfig = true
		}
		if p == "com.dynamic.app" {
			hasDynamic = true
		}
	}
	if !hasConfig {
		t.Error("com.config.app (from config.yaml) was not extracted")
	}
	if !hasDynamic {
		t.Error("com.dynamic.app (from packages.Manager) was not extracted")
	}
}

func TestWatcher_SinkReceivesChecksumAndPath(t *testing.T) {
	device := &fakeDevice{
		serial:    "localhost:5555",
		abi:       "x86_64",
		connected: true,
		version:   adb.VersionInfo{Code: 42, Name: "4.2"},
		paths:     []string{"/data/app/com.example.app/base.apk"},
	}
	w, _, s := setup(t, []adb.Device{device}, []string{"com.example.app"})
	runOnePoll(t, w)

	if len(s.artifacts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(s.artifacts))
	}
	a := s.artifacts[0]
	if a.Checksum == "" {
		t.Error("artifact checksum must not be empty")
	}
	if a.Path == "" {
		t.Error("artifact path must not be empty")
	}
	if _, err := os.Stat(a.Path); err != nil {
		t.Errorf("artifact path does not exist on disk: %v", err)
	}
}
