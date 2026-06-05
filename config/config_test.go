package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

const validYAML = `
adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app
`

func TestLoad_Valid(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snap := cfg.Get()
	if snap.ADBHost != "localhost" {
		t.Errorf("ADBHost = %q, want localhost", snap.ADBHost)
	}
	if snap.PollInterval != 60*time.Second {
		t.Errorf("PollInterval = %v, want 60s", snap.PollInterval)
	}
	if len(snap.Emulators) != 1 || snap.Emulators[0].Port != 5555 || snap.Emulators[0].ABI != "arm64-v8a" {
		t.Errorf("unexpected emulators: %+v", snap.Emulators)
	}
	if len(snap.Packages) != 1 || snap.Packages[0] != "com.example.app" {
		t.Errorf("unexpected packages: %v", snap.Packages)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

var validationCases = []struct {
	name    string
	yaml    string
	wantErr string
}{
	{
		name:    "missing adb_host",
		wantErr: "adb_host is required",
		yaml: `poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app`,
	},
	{
		name:    "zero poll_interval",
		wantErr: "poll_interval",
		yaml: `adb_host: localhost
poll_interval: 0
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app`,
	},
	{
		name:    "negative poll_interval",
		wantErr: "poll_interval",
		yaml: `adb_host: localhost
poll_interval: -10
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app`,
	},
	{
		name:    "missing output_dir",
		wantErr: "output_dir is required",
		yaml: `adb_host: localhost
poll_interval: 60
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app`,
	},
	{
		name:    "empty emulators list",
		wantErr: "at least one emulator",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators: []
packages:
  - com.example.app`,
	},
	{
		name:    "port zero",
		wantErr: "port 0 out of range",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 0
    abi: arm64-v8a
packages:
  - com.example.app`,
	},
	{
		name:    "port out of range high",
		wantErr: "out of range",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 99999
    abi: arm64-v8a
packages:
  - com.example.app`,
	},
	{
		name:    "duplicate emulator ports",
		wantErr: "duplicate port 5555",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
  - port: 5555
    abi: x86_64
packages:
  - com.example.app`,
	},
	{
		name:    "unknown abi",
		wantErr: "unknown abi",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: mips
packages:
  - com.example.app`,
	},
	{
		name:    "package starts with number",
		wantErr: `invalid package name "1com.app"`,
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - 1com.app`,
	},
	{
		name:    "package single segment — no dot",
		wantErr: `invalid package name "singleword"`,
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - singleword`,
	},
	{
		name:    "package trailing dot",
		wantErr: "invalid package name",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.`,
	},
	{
		name:    "package path traversal attempt",
		wantErr: "invalid package name",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - ../../../etc/passwd`,
	},
	{
		name:    "package with slash",
		wantErr: "invalid package name",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com/example/app`,
	},
	{
		name:    "package with semicolon",
		wantErr: "invalid package name",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example;drop`,
	},
	{
		name:    "package with shell metacharacter",
		wantErr: "invalid package name",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example$(whoami)`,
	},
	{
		name:    "expected_certs invalid package name",
		wantErr: "expected_certs: invalid package name",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
expected_certs:
  notapackage: aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899`,
	},
	{
		name:    "expected_certs fingerprint too short",
		wantErr: "expected_certs[com.example.app]",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
expected_certs:
  com.example.app: tooshort`,
	},
	{
		name:    "expected_certs fingerprint non-hex chars",
		wantErr: "expected_certs[com.example.app]",
		yaml: `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
expected_certs:
  com.example.app: zzbbccddeeff00112233445566778899aabbccddeeff00112233445566778899`,
	},
}

func TestValidation(t *testing.T) {
	for _, tc := range validationCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yaml))
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q\nwant it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestLoad_AllKnownABIs(t *testing.T) {
	abis := []string{"arm64-v8a", "armeabi-v7a", "x86_64", "x86"}
	for i, abi := range abis {
		t.Run(abi, func(t *testing.T) {
			_, err := Load(writeConfig(t, minimalConfigWithABI(abi, 5555+i*2)))
			if err != nil {
				t.Errorf("abi %q should be valid, got: %v", abi, err)
			}
		})
	}
}

func TestLoad_MultipleEmulators(t *testing.T) {
	yaml := `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
  - port: 5557
    abi: armeabi-v7a
  - port: 5559
    abi: x86_64
packages:
  - com.example.app`
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := len(cfg.Get().Emulators); n != 3 {
		t.Errorf("want 3 emulators, got %d", n)
	}
}

func TestGet_ReturnsCopy(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	s1 := cfg.Get()
	s1.Packages = append(s1.Packages, "com.mutated.app")
	s1.Emulators = append(s1.Emulators, Emulator{Port: 9999, ABI: "x86"})

	s2 := cfg.Get()
	if len(s2.Packages) != 1 {
		t.Errorf("Get() leaked package mutation: %v", s2.Packages)
	}
	if len(s2.Emulators) != 1 {
		t.Errorf("Get() leaked emulator mutation: %v", s2.Emulators)
	}
}

func TestHotReload_UpdatesConfig(t *testing.T) {
	path := writeConfig(t, validYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Watch(); err != nil {
		t.Fatal(err)
	}

	changed := make(chan Snapshot, 1)
	cfg.OnChange(func(s Snapshot) { changed <- s })

	updated := `adb_host: localhost
poll_interval: 120
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app
  - com.example.two`

	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}

	select {
	case snap := <-changed:
		if snap.PollInterval != 120*time.Second {
			t.Errorf("PollInterval = %v, want 120s", snap.PollInterval)
		}
		if len(snap.Packages) != 2 {
			t.Errorf("want 2 packages after reload, got %d", len(snap.Packages))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for hot reload callback")
	}
}

func TestHotReload_InvalidConfigKeepsCurrent(t *testing.T) {
	path := writeConfig(t, validYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Watch(); err != nil {
		t.Fatal(err)
	}

	reloaded := make(chan struct{}, 1)
	cfg.OnChange(func(_ Snapshot) { reloaded <- struct{}{} })

	if err := os.WriteFile(path, []byte("adb_host: ''"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	select {
	case <-reloaded:
		t.Fatal("OnChange must not fire when reload produces an invalid config")
	default:
	}

	if snap := cfg.Get(); snap.ADBHost != "localhost" {
		t.Errorf("original config was replaced: ADBHost = %q", snap.ADBHost)
	}
}

func TestLoad_EmptyPackagesIsValid(t *testing.T) {
	yaml := `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages: []`
	if _, err := Load(writeConfig(t, yaml)); err != nil {
		t.Errorf("empty packages list should be allowed, got: %v", err)
	}
}

func TestLoad_APIPortOutOfRange(t *testing.T) {
	yaml := `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app
api_port: 99999`
	if _, err := Load(writeConfig(t, yaml)); err == nil {
		t.Error("expected error for api_port out of range")
	}
}

func TestLoad_APIHostDefault(t *testing.T) {
	// api_host should default to 127.0.0.1 when not set.
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if snap := cfg.Get(); snap.APIHost != "127.0.0.1" {
		t.Errorf("APIHost default = %q, want 127.0.0.1", snap.APIHost)
	}
}

func TestLoad_ExpectedCertsValid(t *testing.T) {
	// Plain 64-char hex fingerprint.
	yaml := `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app
expected_certs:
  com.example.app: aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899`
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snap := cfg.Get()
	fp, ok := snap.ExpectedCerts["com.example.app"]
	if !ok {
		t.Fatal("expected_certs not present in snapshot")
	}
	if fp != "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899" {
		t.Errorf("fingerprint = %q", fp)
	}
}

func TestLoad_ExpectedCertsNormalisesColons(t *testing.T) {
	// Colon-separated format (as printed by keytool and apksigner).
	yaml := `adb_host: localhost
poll_interval: 60
output_dir: ./apks
emulators:
  - port: 5555
    abi: arm64-v8a
packages:
  - com.example.app
expected_certs:
  com.example.app: "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"`
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fp := cfg.Get().ExpectedCerts["com.example.app"]
	// Must be normalised: lowercase, no colons.
	if fp != "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899" {
		t.Errorf("normalised fingerprint = %q", fp)
	}
}

func TestLoad_APIPortZeroIsDisabled(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if snap := cfg.Get(); snap.APIPort != 0 {
		t.Errorf("APIPort default = %d, want 0 (disabled)", snap.APIPort)
	}
}

func minimalConfigWithABI(abi string, port int) string {
	return "adb_host: localhost\npoll_interval: 60\noutput_dir: ./apks\nemulators:\n  - port: " +
		strconv.Itoa(port) + "\n    abi: " + abi + "\npackages:\n  - com.example.app\n"
}
