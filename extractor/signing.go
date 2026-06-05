package extractor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// certDigestRE matches the SHA-256 certificate fingerprint line from `apksigner verify --print-certs`.
// Example line: "Signer #1 certificate SHA-256 digest: aabbcc..."
var certDigestRE = regexp.MustCompile(`(?i)Signer #1 certificate SHA-256 digest:\s*([0-9a-fA-F]{64})`)

// verifyAPKSignature checks that apkPath is signed with expectedFingerprint.
// expectedFingerprint must be 64 lowercase hex chars with no separators — the
// normalised form produced by config.normalizeFingerprint during config load.
//
// Returns an error if apksigner is unavailable, the APK is unsigned or invalid,
// or the certificate does not match. A mismatch is treated as a hard failure:
// a compromised emulator cannot forge Google's Play Store signing certificate.
func verifyAPKSignature(ctx context.Context, apkPath, expectedFingerprint string) error {
	apksigner, err := findApksigner()
	if err != nil {
		return fmt.Errorf("apksigner unavailable — required for certificate verification: %w\n"+
			"Ensure ANDROID_HOME is set or add build-tools to PATH", err)
	}

	// exec.CommandContext with discrete args — no shell, no injection risk.
	cmd := exec.CommandContext(ctx, apksigner, "verify", "--print-certs", apkPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apksigner verify failed for %s: %w\n%s",
			filepath.Base(apkPath), err, truncateOutput(string(out), 512))
	}

	m := certDigestRE.FindSubmatch(out)
	if m == nil {
		return fmt.Errorf("could not parse certificate fingerprint from apksigner output for %s", filepath.Base(apkPath))
	}
	actual := strings.ToLower(string(m[1]))

	if actual != expectedFingerprint {
		// Log both fingerprints so an operator can diagnose whether this is a
		// legitimate signing-key rotation or evidence of tampering.
		return fmt.Errorf("certificate mismatch for %s: got %s, want %s — APK may have been tampered with",
			filepath.Base(apkPath), actual, expectedFingerprint)
	}
	return nil
}

// findApksigner returns the path to the apksigner executable.
// Search order: PATH → ANDROID_HOME/build-tools → common platform defaults.
func findApksigner() (string, error) {
	exe := "apksigner"
	if runtime.GOOS == "windows" {
		exe = "apksigner.bat"
	}

	if p, err := exec.LookPath(exe); err == nil {
		return p, nil
	}

	if home := os.Getenv("ANDROID_HOME"); home != "" {
		if p := latestBuildTool(home, exe); p != "" {
			return p, nil
		}
	}

	// Fall back to well-known SDK locations when ANDROID_HOME is not set.
	var defaults []string
	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			defaults = append(defaults, filepath.Join(local, "Android", "Sdk"))
		}
	case "darwin":
		if h := os.Getenv("HOME"); h != "" {
			defaults = append(defaults, filepath.Join(h, "Library", "Android", "sdk"))
		}
	default:
		if h := os.Getenv("HOME"); h != "" {
			defaults = append(defaults, filepath.Join(h, "Android", "Sdk"))
		}
	}
	for _, d := range defaults {
		if p := latestBuildTool(d, exe); p != "" {
			return p, nil
		}
	}

	return "", fmt.Errorf("%s not found in PATH, ANDROID_HOME/build-tools, or common SDK locations", exe)
}

// latestBuildTool finds the newest build-tools version directory under sdkRoot
// and returns the path to exe within it. Returns "" if not found.
// Directories are sorted in reverse lexicographic order; for semver-named
// directories (e.g. "35.0.0", "34.0.4") this gives the newest version first.
func latestBuildTool(sdkRoot, exe string) string {
	buildToolsDir := filepath.Join(sdkRoot, "build-tools")
	entries, err := os.ReadDir(buildToolsDir)
	if err != nil {
		return ""
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		p := filepath.Join(buildToolsDir, d, exe)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// truncateOutput caps s at maxLen bytes for safe embedding in error messages.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}
