package extractor

import (
	"os"
	"path/filepath"
	"testing"
)

// --- certDigestRE ---

func TestCertDigestRE_Matches(t *testing.T) {
	cases := []struct {
		output string
		want   string
	}{
		{
			output: "Signer #1 certificate SHA-256 digest: aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899\n",
			want:   "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		},
		{
			// Mixed case from some apksigner versions.
			output: "signer #1 certificate sha-256 digest: AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899\n",
			want:   "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899",
		},
		{
			// Whitespace before digest.
			output: "  Signer #1 certificate SHA-256 digest:   1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef\n",
			want:   "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		},
	}
	for _, tc := range cases {
		m := certDigestRE.FindStringSubmatch(tc.output)
		if m == nil {
			t.Errorf("certDigestRE did not match %q", tc.output)
			continue
		}
		if m[1] != tc.want {
			t.Errorf("got %q, want %q", m[1], tc.want)
		}
	}
}

func TestCertDigestRE_NoMatch(t *testing.T) {
	cases := []string{
		"Signer #1 certificate SHA-256 digest: tooshort",
		"Signer #2 certificate SHA-256 digest: aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		"Verified using v1 scheme: true",
		"",
	}
	for _, s := range cases {
		if m := certDigestRE.FindStringSubmatch(s); m != nil {
			t.Errorf("certDigestRE unexpectedly matched %q → %q", s, m[1])
		}
	}
}

// --- truncateOutput ---

func TestTruncateOutput_ShortString(t *testing.T) {
	s := "hello"
	got := truncateOutput(s, 10)
	if got != s {
		t.Errorf("truncateOutput(%q, 10) = %q, want unchanged", s, got)
	}
}

func TestTruncateOutput_ExactLimit(t *testing.T) {
	s := "exactly10c"
	got := truncateOutput(s, 10)
	if got != s {
		t.Errorf("got %q, want %q", got, s)
	}
}

func TestTruncateOutput_Truncates(t *testing.T) {
	s := "this is a longer string"
	got := truncateOutput(s, 7)
	if len(got) <= 7 {
		t.Errorf("expected suffix appended, got %q", got)
	}
	if got[:7] != s[:7] {
		t.Errorf("prefix mismatch: got %q, want %q", got[:7], s[:7])
	}
}

// --- latestBuildTool ---

func TestLatestBuildTool_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if got := latestBuildTool(dir, "apksigner"); got != "" {
		t.Errorf("expected empty result for empty dir, got %q", got)
	}
}

func TestLatestBuildTool_NoBuildToolsSubdir(t *testing.T) {
	dir := t.TempDir()
	// build-tools subdirectory does not exist at all.
	if got := latestBuildTool(dir, "apksigner"); got != "" {
		t.Errorf("expected empty result when build-tools missing, got %q", got)
	}
}

func TestLatestBuildTool_PicksLatestVersion(t *testing.T) {
	dir := t.TempDir()
	bt := filepath.Join(dir, "build-tools")

	// Create fake versioned directories with apksigner executables.
	for _, ver := range []string{"34.0.0", "35.0.1", "33.0.2"} {
		d := filepath.Join(bt, ver)
		os.MkdirAll(d, 0700)
		os.WriteFile(filepath.Join(d, "apksigner"), []byte("#!/bin/sh"), 0700)
	}

	got := latestBuildTool(dir, "apksigner")
	// Reverse-sorted: "35.0.1" > "34.0.0" > "33.0.2"
	want := filepath.Join(bt, "35.0.1", "apksigner")
	if got != want {
		t.Errorf("latestBuildTool = %q, want %q", got, want)
	}
}

func TestLatestBuildTool_SkipsDirsWithoutExe(t *testing.T) {
	dir := t.TempDir()
	bt := filepath.Join(dir, "build-tools")

	// "35.0.0" exists but has no apksigner; "34.0.0" has apksigner.
	os.MkdirAll(filepath.Join(bt, "35.0.0"), 0700)
	os.MkdirAll(filepath.Join(bt, "34.0.0"), 0700)
	os.WriteFile(filepath.Join(bt, "34.0.0", "apksigner"), []byte("#!/bin/sh"), 0700)

	got := latestBuildTool(dir, "apksigner")
	want := filepath.Join(bt, "34.0.0", "apksigner")
	if got != want {
		t.Errorf("latestBuildTool = %q, want %q", got, want)
	}
}
