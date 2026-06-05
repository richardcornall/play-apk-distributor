package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

var validPackageName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)

// validHost accepts hostnames and IPv4/IPv6 addresses. Rejects shell metacharacters,
// spaces, and other characters that have no place in a network address.
var validHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.\-:\[\]]*$`)

var knownABIs = map[string]bool{
	"arm64-v8a":   true,
	"armeabi-v7a": true,
	"x86_64":      true,
	"x86":         true,
}

// minAPITokenLen is the minimum acceptable length for api_token.
// Shorter tokens are trivially brutable on a high-throughput loopback interface.
const minAPITokenLen = 32

// defaultMaxAPKSizeMB is used when max_apk_size_mb is absent from config.
const defaultMaxAPKSizeMB = 1024

// Emulator describes a single running AVD instance.
type Emulator struct {
	Port int    `yaml:"port"`
	ABI  string `yaml:"abi"`
}

// Snapshot is an immutable copy of the active configuration values.
type Snapshot struct {
	ADBHost       string
	PollInterval  time.Duration
	OutputDir     string
	Emulators     []Emulator
	Packages      []string
	APIHost       string
	APIPort       int
	APIToken      string            // Bearer token required on non-health API endpoints; empty = no auth (warn)
	MaxAPKSizeMB  int               // hard ceiling per pulled APK file; 0 uses defaultMaxAPKSizeMB
	ExpectedCerts map[string]string // package → normalised SHA-256 cert fingerprint (64 lowercase hex chars)
}

type raw struct {
	ADBHost       string            `yaml:"adb_host"`
	PollInterval  int               `yaml:"poll_interval"`
	OutputDir     string            `yaml:"output_dir"`
	Emulators     []Emulator        `yaml:"emulators"`
	Packages      []string          `yaml:"packages"`
	APIHost       string            `yaml:"api_host"`
	APIPort       int               `yaml:"api_port"`
	APIToken      string            `yaml:"api_token"`
	MaxAPKSizeMB  int               `yaml:"max_apk_size_mb"`
	ExpectedCerts map[string]string `yaml:"expected_certs"`
}

// Config holds live configuration with hot-reload support.
type Config struct {
	mu       sync.RWMutex
	path     string
	snap     Snapshot
	handlers []func(Snapshot)
}

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	c := &Config{path: path}
	if err := c.reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// Get returns a copy of the current configuration snapshot. Safe for concurrent use.
func (c *Config) Get() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.snap
	s.Emulators = append([]Emulator(nil), c.snap.Emulators...)
	s.Packages = append([]string(nil), c.snap.Packages...)
	if len(c.snap.ExpectedCerts) > 0 {
		certs := make(map[string]string, len(c.snap.ExpectedCerts))
		for k, v := range c.snap.ExpectedCerts {
			certs[k] = v
		}
		s.ExpectedCerts = certs
	}
	return s
}

// OnChange registers fn to be called in a new goroutine after every successful reload.
func (c *Config) OnChange(fn func(Snapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers = append(c.handlers, fn)
}

// Watch starts background monitoring of the config file and applies changes without restart.
func (c *Config) Watch() error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(c.path); err != nil {
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
				if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
					continue
				}
				if err := c.reload(); err != nil {
					slog.Error("config reload failed, keeping current config", "err", err)
					continue
				}
				snap := c.Get()
				slog.Info("config reloaded", "packages", len(snap.Packages), "emulators", len(snap.Emulators))
				c.mu.RLock()
				handlers := append(([]func(Snapshot))(nil), c.handlers...)
				c.mu.RUnlock()
				for _, fn := range handlers {
					go fn(snap)
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				slog.Error("config watcher error", "err", err)
			}
		}
	}()
	return nil
}

func (c *Config) reload() error {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var r raw
	if err := yaml.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if err := validate(&r); err != nil {
		return err
	}

	apiHost := r.APIHost
	if apiHost == "" {
		apiHost = "127.0.0.1"
	}

	maxAPKSizeMB := r.MaxAPKSizeMB
	if maxAPKSizeMB == 0 {
		maxAPKSizeMB = defaultMaxAPKSizeMB
	}

	// Normalise fingerprints now so comparisons in the extractor are simple string equality.
	var expectedCerts map[string]string
	if len(r.ExpectedCerts) > 0 {
		expectedCerts = make(map[string]string, len(r.ExpectedCerts))
		for pkg, fp := range r.ExpectedCerts {
			normalized, _ := normalizeFingerprint(fp) // already validated above
			expectedCerts[pkg] = normalized
		}
	}

	snap := Snapshot{
		ADBHost:       r.ADBHost,
		PollInterval:  time.Duration(r.PollInterval) * time.Second,
		OutputDir:     r.OutputDir,
		Emulators:     r.Emulators,
		Packages:      r.Packages,
		APIHost:       apiHost,
		APIPort:       r.APIPort,
		APIToken:      r.APIToken,
		MaxAPKSizeMB:  maxAPKSizeMB,
		ExpectedCerts: expectedCerts,
	}
	c.mu.Lock()
	c.snap = snap
	c.mu.Unlock()
	return nil
}

func validate(r *raw) error {
	if err := validateCore(r); err != nil {
		return err
	}
	if err := validateEmulators(r.Emulators); err != nil {
		return err
	}
	if err := validatePackageNames(r.Packages); err != nil {
		return err
	}
	if err := validateAPI(r.APIPort, r.APIToken); err != nil {
		return err
	}
	return validateExpectedCerts(r.ExpectedCerts)
}

func validateCore(r *raw) error {
	if r.ADBHost == "" {
		return errors.New("adb_host is required")
	}
	if !validHost.MatchString(r.ADBHost) {
		return fmt.Errorf("adb_host %q is not a valid hostname or IP address", r.ADBHost)
	}
	if r.PollInterval <= 0 {
		return errors.New("poll_interval must be a positive number of seconds")
	}
	if r.OutputDir == "" {
		return errors.New("output_dir is required")
	}
	if r.MaxAPKSizeMB < 0 {
		return errors.New("max_apk_size_mb must be non-negative (0 uses the default of 1024 MB)")
	}
	return nil
}

func validateEmulators(emulators []Emulator) error {
	if len(emulators) == 0 {
		return errors.New("at least one emulator entry is required")
	}
	seen := map[int]bool{}
	for i, e := range emulators {
		if e.Port < 1 || e.Port > 65535 {
			return fmt.Errorf("emulators[%d]: port %d out of range", i, e.Port)
		}
		if seen[e.Port] {
			return fmt.Errorf("emulators[%d]: duplicate port %d", i, e.Port)
		}
		seen[e.Port] = true
		if !knownABIs[e.ABI] {
			return fmt.Errorf("emulators[%d]: unknown abi %q — valid: arm64-v8a, armeabi-v7a, x86_64, x86", i, e.ABI)
		}
	}
	return nil
}

func validatePackageNames(packages []string) error {
	for _, p := range packages {
		if !validPackageName.MatchString(p) {
			return fmt.Errorf("invalid package name %q", p)
		}
	}
	return nil
}

func validateAPI(port int, token string) error {
	if port != 0 && (port < 1 || port > 65535) {
		return fmt.Errorf("api_port %d out of range (1–65535)", port)
	}
	if token != "" && len(token) < minAPITokenLen {
		return fmt.Errorf("api_token must be at least %d characters", minAPITokenLen)
	}
	return nil
}

func validateExpectedCerts(certs map[string]string) error {
	for pkg, fp := range certs {
		if !validPackageName.MatchString(pkg) {
			return fmt.Errorf("expected_certs: invalid package name %q", pkg)
		}
		if _, err := normalizeFingerprint(fp); err != nil {
			return fmt.Errorf("expected_certs[%s]: %w", pkg, err)
		}
	}
	return nil
}

// normalizeFingerprint strips colons and lowercases a SHA-256 certificate fingerprint.
// Accepts "aa:bb:cc:..." (colon-separated) and "aabbcc..." (plain hex) formats.
// Returns an error if the result is not exactly 64 lowercase hex characters.
func normalizeFingerprint(fp string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(fp, ":", "")))
	if len(s) != 64 {
		return "", fmt.Errorf("certificate fingerprint must be 64 hex characters (got %d after stripping colons)", len(s))
	}
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return "", fmt.Errorf("certificate fingerprint contains non-hex character %q", r)
		}
	}
	return s, nil
}
