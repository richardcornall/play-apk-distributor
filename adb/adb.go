package adb

import (
	"bufio"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// maxVersionNameLen caps the versionName string from device output.
// A compromised emulator could return an arbitrarily long string which would
// inflate store.json, latest.json, XAPK manifests, and structured log output.
const maxVersionNameLen = 128

// VersionInfo holds the installed version metadata for a package.
type VersionInfo struct {
	Code int
	Name string
}

// Device is the interface watcher and extractor use to talk to an emulator.
// *Client satisfies it; tests can substitute a fake.
type Device interface {
	Serial() string
	ABI() string
	IsConnected() bool
	Reconnect() error
	GetInstalledVersion(pkg string) (VersionInfo, error)
	PullAPKPaths(pkg string) ([]string, error)
	PullFile(devicePath, destPath string) error
}

// Client wraps adb commands for a single emulator instance.
// All commands are issued via exec.Command with discrete arguments — no shell interpolation.
type Client struct {
	serial string // host:port
	abi    string
}

// New creates a Client for the emulator at host:port with the given ABI tag.
func New(host string, port int, abi string) *Client {
	return &Client{
		serial: fmt.Sprintf("%s:%d", host, port),
		abi:    abi,
	}
}

func (c *Client) ABI() string    { return c.abi }
func (c *Client) Serial() string { return c.serial }

// Connect establishes an ADB TCP connection to the emulator.
func (c *Client) Connect() error {
	out, err := run("connect", c.serial)
	if err != nil {
		return fmt.Errorf("adb connect %s: %w", c.serial, err)
	}
	if strings.Contains(out, "failed") || strings.Contains(out, "error") {
		return fmt.Errorf("adb connect %s: %s", c.serial, strings.TrimSpace(out))
	}
	return nil
}

// IsConnected reports whether the emulator is listed as connected in adb devices.
func (c *Client) IsConnected() bool {
	out, err := run("devices")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, c.serial) && strings.Contains(line, "device") {
			return true
		}
	}
	return false
}

// Reconnect retries Connect with exponential backoff up to 5 attempts.
func (c *Client) Reconnect() error {
	backoff := time.Second
	for attempt := 1; attempt <= 5; attempt++ {
		if err := c.Connect(); err == nil {
			slog.Info("adb reconnected", "serial", c.serial)
			return nil
		}
		slog.Warn("adb reconnect attempt failed", "serial", c.serial, "attempt", attempt, "next_in", backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return fmt.Errorf("could not reconnect to %s after 5 attempts", c.serial)
}

// GetInstalledVersion returns the versionCode and versionName for pkg via dumpsys package.
// Package names are validated at config load time — no shell injection risk.
func (c *Client) GetInstalledVersion(pkg string) (VersionInfo, error) {
	out, err := c.shell("dumpsys", "package", pkg)
	if err != nil {
		return VersionInfo{}, fmt.Errorf("dumpsys package %s on %s: %w", pkg, c.serial, err)
	}
	info, err := parseVersionInfo(pkg, out)
	if err != nil {
		return VersionInfo{}, fmt.Errorf("%w on %s", err, c.serial)
	}
	return info, nil
}

// PullAPKPaths returns the on-device paths for all APK splits of pkg.
// Multiple paths indicate a split APK install.
func (c *Client) PullAPKPaths(pkg string) ([]string, error) {
	out, err := c.shell("pm", "path", pkg)
	if err != nil {
		return nil, fmt.Errorf("pm path %s on %s: %w", pkg, c.serial, err)
	}
	paths, err := parsePMPaths(out)
	if err != nil {
		return nil, fmt.Errorf("%w for %s on %s", err, pkg, c.serial)
	}
	return paths, nil
}

// parseVersionInfo extracts VersionInfo from dumpsys package output.
func parseVersionInfo(pkg, output string) (VersionInfo, error) {
	var info VersionInfo
	inPkg := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Package ["+pkg+"]") {
			inPkg = true
			continue
		}
		if !inPkg {
			continue
		}
		// "versionCode=1042 minSdk=21 targetSdk=33"
		if strings.HasPrefix(line, "versionCode=") {
			field := strings.Fields(strings.TrimPrefix(line, "versionCode="))[0]
			code, err := strconv.Atoi(field)
			if err != nil {
				return VersionInfo{}, fmt.Errorf("parse versionCode for %s: %w", pkg, err)
			}
			info.Code = code
		}
		if strings.HasPrefix(line, "versionName=") {
			name := strings.TrimPrefix(line, "versionName=")
			if len(name) > maxVersionNameLen {
				name = name[:maxVersionNameLen]
			}
			info.Name = name
		}
		if info.Code != 0 && info.Name != "" {
			break
		}
	}
	if info.Code == 0 {
		return VersionInfo{}, fmt.Errorf("package %s not found or not installed", pkg)
	}
	return info, nil
}

// parsePMPaths extracts device APK paths from pm path output.
func parsePMPaths(output string) ([]string, error) {
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "package:"); ok {
			p := strings.TrimSpace(after)
			if p != "" {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no paths returned — package may not be installed")
	}
	return paths, nil
}

// PullFile copies a single file from the device to destPath on the host.
func (c *Client) PullFile(devicePath, destPath string) error {
	if _, err := run("-s", c.serial, "pull", devicePath, destPath); err != nil {
		return fmt.Errorf("adb pull %s: %w", devicePath, err)
	}
	return nil
}

func (c *Client) shell(args ...string) (string, error) {
	return run(append([]string{"-s", c.serial, "shell"}, args...)...)
}

// run executes an adb command with discrete arguments (no shell interpolation).
func run(args ...string) (string, error) {
	out, err := exec.Command("adb", args...).Output() //nolint:gosec — args are validated before reaching here
	if err != nil {
		return "", err
	}
	return string(out), nil
}
