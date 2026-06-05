package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/richardcornall/play-apk-distributor/adb"
	"github.com/richardcornall/play-apk-distributor/api"
	"github.com/richardcornall/play-apk-distributor/config"
	"github.com/richardcornall/play-apk-distributor/packages"
	"github.com/richardcornall/play-apk-distributor/sink"
	"github.com/richardcornall/play-apk-distributor/store"
	"github.com/richardcornall/play-apk-distributor/watcher"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	debug := flag.Bool("debug", false, "enable debug-level logging")
	flag.Parse()

	setupLogging(*debug)
	checkConfigPermissions(*cfgPath)

	cfg, snap := loadConfig(*cfgPath)
	st := openStore(snap.OutputDir)
	pkgMgr := openPackages(*cfgPath, snap)
	clients := connectEmulators(snap)

	s := sink.Noop{}
	w := watcher.New(cfg, pkgMgr, st, clients, s)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if snap.APIPort > 0 {
		startAPI(ctx, snap, pkgMgr, st)
	}

	slog.Info("apk-distributor started",
		"config", *cfgPath,
		"output_dir", snap.OutputDir,
		"packages", len(pkgMgr.List()),
		"max_apk_size_mb", snap.MaxAPKSizeMB,
		"api_enabled", snap.APIPort > 0,
	)
	w.Run(ctx)
	slog.Info("apk-distributor stopped")
}

func setupLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

func loadConfig(cfgPath string) (*config.Config, config.Snapshot) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "path", cfgPath, "err", err)
		os.Exit(1)
	}
	if err := cfg.Watch(); err != nil {
		slog.Error("failed to start config watcher", "err", err)
		os.Exit(1)
	}
	snap := cfg.Get()
	if err := os.MkdirAll(snap.OutputDir, 0750); err != nil {
		slog.Error("failed to create output dir", "path", snap.OutputDir, "err", err)
		os.Exit(1)
	}
	cleanTmpDir(filepath.Join(snap.OutputDir, ".tmp"))
	return cfg, snap
}

func openStore(outputDir string) *store.Store {
	st, err := store.Open(outputDir)
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}
	return st
}

func openPackages(cfgPath string, snap config.Snapshot) *packages.Manager {
	pkgMgr, err := packages.New(filepath.Dir(cfgPath))
	if err != nil {
		slog.Error("failed to open packages manager", "err", err)
		os.Exit(1)
	}
	if err := pkgMgr.Watch(); err != nil {
		slog.Error("failed to start packages watcher", "err", err)
		os.Exit(1)
	}
	if len(snap.Packages) > 0 {
		if err := pkgMgr.Seed(snap.Packages); err != nil {
			slog.Error("failed to seed packages from config", "err", err)
			os.Exit(1)
		}
	}
	return pkgMgr
}

func connectEmulators(snap config.Snapshot) []adb.Device {
	clients := make([]adb.Device, 0, len(snap.Emulators))
	for _, e := range snap.Emulators {
		c := adb.New(snap.ADBHost, e.Port, e.ABI)
		if err := c.Connect(); err != nil {
			slog.Warn("initial connect failed â€” will retry during polling", "serial", c.Serial(), "abi", e.ABI, "err", err)
		} else {
			slog.Info("connected to emulator", "serial", c.Serial(), "abi", e.ABI)
		}
		clients = append(clients, c)
	}
	return clients
}

func startAPI(ctx context.Context, snap config.Snapshot, pkgMgr *packages.Manager, st *store.Store) {
	warnAPISecurityPosture(snap)
	addr := fmt.Sprintf("%s:%d", snap.APIHost, snap.APIPort)
	srv := api.New(addr, snap.APIToken, pkgMgr, st)
	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			slog.Error("api server error", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("api server shutdown error", "err", err)
		}
	}()
}

// warnAPISecurityPosture logs prominent warnings when the API is running without
// recommended hardening. These are not fatal â€” operators may have compensating
// controls â€” but they must be visible at startup.
func warnAPISecurityPosture(snap config.Snapshot) {
	if snap.APIToken == "" {
		slog.Warn("SECURITY: api_token is not set â€” any process on this host can add/remove tracked packages;" +
			" set api_token in config.yaml (min 32 chars)")
	}
	localhost := snap.APIHost == "127.0.0.1" || snap.APIHost == "::1" || snap.APIHost == "localhost"
	if !localhost {
		slog.Warn("SECURITY: API is not bound to localhost â€” unauthenticated if api_token is not set",
			"api_host", snap.APIHost)
	}
}

// checkConfigPermissions warns on Unix if the config file has group or world
// read/write bits set. On Windows file permission bits are not enforced so
// the check is skipped.
func checkConfigPermissions(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return // Load() will fail with a better message
	}
	mode := info.Mode()
	if mode&0o044 != 0 {
		slog.Warn("config file is group or world readable â€” api_token and other secrets may be exposed",
			"path", path, "mode", fmt.Sprintf("%04o", mode.Perm()))
	}
	if mode&0o022 != 0 {
		slog.Warn("config file is group or world writable â€” an attacker could inject packages or redirect output",
			"path", path, "mode", fmt.Sprintf("%04o", mode.Perm()))
	}
}

// cleanTmpDir removes any leftover per-extraction temp directories from a prior
// unclean shutdown. Called before the watcher starts so stale APKs don't linger
// in a world-inaccessible but still disk-consuming location.
func cleanTmpDir(tmpBase string) {
	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		return // doesn't exist yet â€” fine
	}
	for _, e := range entries {
		if e.IsDir() {
			p := filepath.Join(tmpBase, e.Name())
			if err := os.RemoveAll(p); err != nil {
				slog.Warn("failed to clean leftover temp dir", "path", p, "err", err)
			} else {
				slog.Debug("cleaned leftover temp dir", "path", p)
			}
		}
	}
}
