package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/richardcornall/play-apk-distributor/adb"
	"github.com/richardcornall/play-apk-distributor/config"
	"github.com/richardcornall/play-apk-distributor/extractor"
	"github.com/richardcornall/play-apk-distributor/packages"
	"github.com/richardcornall/play-apk-distributor/sink"
	"github.com/richardcornall/play-apk-distributor/store"
)

// Watcher polls all configured emulators and triggers extraction when a version changes.
type Watcher struct {
	cfg     *config.Config
	pkgs    *packages.Manager
	store   *store.Store
	sink    sink.Sink
	mu      sync.Mutex
	clients []adb.Device
}

// New creates a Watcher. It registers config-change and package-change callbacks
// so both hot-reload without restart.
func New(cfg *config.Config, pkgs *packages.Manager, st *store.Store, clients []adb.Device, s sink.Sink) *Watcher {
	w := &Watcher{cfg: cfg, pkgs: pkgs, store: st, sink: s, clients: clients}
	cfg.OnChange(func(snap config.Snapshot) {
		w.syncClients(snap)
	})
	return w
}

// Run starts the poll loop and blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	snap := w.cfg.Get()
	ticker := time.NewTicker(snap.PollInterval)
	defer ticker.Stop()

	pkgs := w.effectivePackages(snap)
	slog.Info("watcher started",
		"poll_interval", snap.PollInterval,
		"packages", len(pkgs),
		"emulators", len(snap.Emulators),
	)

	w.poll(ctx, snap)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := w.cfg.Get()
			ticker.Reset(current.PollInterval)
			w.poll(ctx, current)
		}
	}
}

func (w *Watcher) poll(ctx context.Context, snap config.Snapshot) {
	pkgs := w.effectivePackages(snap)
	slog.Debug("poll cycle started", "packages", len(pkgs))
	for _, pkg := range pkgs {
		if ctx.Err() != nil {
			return
		}
		w.checkPackage(ctx, pkg, snap.OutputDir)
	}
	slog.Debug("poll cycle complete")
}

// effectivePackages merges packages from config.yaml (static) and packages.Manager
// (dynamic). Config packages come first; duplicates are deduplicated.
func (w *Watcher) effectivePackages(snap config.Snapshot) []string {
	seen := make(map[string]bool)
	var result []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	for _, p := range snap.Packages {
		add(p)
	}
	for _, p := range w.pkgs.List() {
		add(p)
	}
	return result
}

func (w *Watcher) checkPackage(ctx context.Context, pkg, outputDir string) {
	w.mu.Lock()
	clients := append([]adb.Device(nil), w.clients...)
	w.mu.Unlock()

	type result struct {
		client adb.Device
		info   adb.VersionInfo
		err    error
	}

	results := make([]result, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c adb.Device) {
			defer wg.Done()
			if !c.IsConnected() {
				slog.Warn("emulator offline, attempting reconnect", "serial", c.Serial(), "pkg", pkg)
				if err := c.Reconnect(); err != nil {
					results[i] = result{client: c, err: err}
					return
				}
			}
			info, err := c.GetInstalledVersion(pkg)
			results[i] = result{client: c, info: info, err: err}
		}(i, c)
	}
	wg.Wait()

	var connected []result
	for _, r := range results {
		if r.err != nil {
			slog.Warn("could not get version from emulator", "pkg", pkg, "serial", r.client.Serial(), "err", r.err)
			continue
		}
		connected = append(connected, r)
	}

	if len(connected) == 0 {
		slog.Error("no emulators available for package", "pkg", pkg)
		return
	}

	// All connected emulators must agree on version before extracting â€”
	// a mismatch means one Play Store hasn't updated yet, producing mismatched splits.
	base := connected[0]
	for _, r := range connected[1:] {
		if r.info.Code != base.info.Code {
			slog.Warn("version mismatch between emulators â€” waiting for consistency",
				"pkg", pkg,
				"serial_a", base.client.Serial(), "code_a", base.info.Code,
				"serial_b", r.client.Serial(), "code_b", r.info.Code,
			)
			return
		}
	}

	agreedCode := base.info.Code
	agreedName := base.info.Name

	last, known := w.store.GetLastKnown(pkg)
	if known && last.VersionCode >= agreedCode {
		slog.Debug("no update", "pkg", pkg, "version_code", agreedCode)
		return
	}

	if known {
		slog.Info("update detected",
			"pkg", pkg,
			"old_version_code", last.VersionCode,
			"old_version_name", last.VersionName,
			"new_version_code", agreedCode,
			"new_version_name", agreedName,
		)
	} else {
		slog.Info("new package detected", "pkg", pkg, "version_code", agreedCode, "version_name", agreedName)
	}

	snap2 := w.cfg.Get()
	outPath, err := extractor.Extract(ctx, pkg, agreedCode, agreedName, outputDir, snap2.MaxAPKSizeMB, snap2.ExpectedCerts, clients, w.sink)
	if err != nil {
		slog.Error("extraction failed â€” will retry next poll", "pkg", pkg, "version_code", agreedCode, "err", err)
		return
	}

	if err := w.store.SetLastKnown(pkg, agreedCode, agreedName); err != nil {
		slog.Error("failed to update store after extraction", "pkg", pkg, "err", err)
		return
	}

	slog.Info("extraction complete",
		"pkg", pkg,
		"version_code", agreedCode,
		"version_name", agreedName,
		"output", outPath,
	)
}

// syncClients reconciles the client list against a new config snapshot.
func (w *Watcher) syncClients(snap config.Snapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()

	bySerial := make(map[string]adb.Device, len(w.clients))
	for _, c := range w.clients {
		bySerial[c.Serial()] = c
	}

	next := make([]adb.Device, 0, len(snap.Emulators))
	for _, e := range snap.Emulators {
		serial := fmt.Sprintf("%s:%d", snap.ADBHost, e.Port)
		if c, ok := bySerial[serial]; ok {
			next = append(next, c)
		} else {
			c := adb.New(snap.ADBHost, e.Port, e.ABI)
			if err := c.Connect(); err != nil {
				slog.Warn("connect to newly configured emulator failed", "serial", c.Serial(), "err", err)
			} else {
				slog.Info("added emulator from config reload", "serial", c.Serial(), "abi", e.ABI)
			}
			next = append(next, c)
		}
	}
	w.clients = next
	slog.Info("emulator client list updated", "count", len(w.clients))
}

// Clients returns a snapshot of the current client list.
func (w *Watcher) Clients() []adb.Device {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]adb.Device(nil), w.clients...)
}
