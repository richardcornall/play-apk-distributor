package extractor

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- safeFilename ---

func TestSafeFilename_Valid(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/data/app/com.example.app/base.apk", "base.apk"},
		{"/data/app/com.example.app/split_config.arm64_v8a.apk", "split_config.arm64_v8a.apk"},
		{"/data/app/com.example.app/split_config.xxhdpi.apk", "split_config.xxhdpi.apk"},
		{"/data/app/com.example.app/split_config.en.apk", "split_config.en.apk"},
		{"base.apk", "base.apk"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := safeFilename(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSafeFilename_Blocked(t *testing.T) {
	cases := []string{
		"../../etc/passwd",    // no .apk extension after path.Base → "passwd"
		"/etc/shadow",         // no .apk extension → "shadow"
		"file with spaces.apk",
		"file\x00null.apk",
		"file;rm.apk",
		".hidden",
		".",
		"",
		"file$(whoami).apk",
		"file`cmd`.apk",
		"file|pipe.apk",
		"file>redirect.apk",
		"C:\\Windows\\system32\\cmd.exe", // Windows path — no .apk
	}
	for _, tc := range cases {
		t.Run(strconv.Quote(tc), func(t *testing.T) {
			_, err := safeFilename(tc)
			if err == nil {
				t.Errorf("safeFilename(%q) should have returned an error", tc)
			}
		})
	}
}

// --- splitID ---

func TestSplitID(t *testing.T) {
	cases := []struct{ input, want string }{
		{"base.apk", "base"},
		{"split_config.arm64_v8a.apk", "config.arm64_v8a"},
		{"split_config.xxhdpi.apk", "config.xxhdpi"},
		{"split_config.en.apk", "config.en"},
		{"split_config.x86_64.apk", "config.x86_64"},
	}
	for _, tc := range cases {
		if got := splitID(tc.input); got != tc.want {
			t.Errorf("splitID(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- writeChecksum ---

func TestWriteChecksum_CorrectHash(t *testing.T) {
	dir := t.TempDir()
	content := []byte("fake apk content for testing")
	filePath := filepath.Join(dir, "test.apk")
	if err := os.WriteFile(filePath, content, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := writeChecksum(filePath); err != nil {
		t.Fatalf("writeChecksum: %v", err)
	}
	sumBytes, err := os.ReadFile(filePath + ".sha256")
	if err != nil {
		t.Fatalf("read .sha256: %v", err)
	}
	got := strings.TrimSpace(string(sumBytes))
	h := sha256.Sum256(content)
	want := hex.EncodeToString(h[:])
	if got != want {
		t.Errorf("checksum = %q, want %q", got, want)
	}
}

func TestWriteChecksum_LargeFile(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte("A"), 5*1024*1024) // 5 MB
	filePath := filepath.Join(dir, "large.apk")
	if err := os.WriteFile(filePath, content, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := writeChecksum(filePath); err != nil {
		t.Fatalf("writeChecksum on large file: %v", err)
	}
}

func TestWriteChecksum_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "empty.apk")
	if err := os.WriteFile(filePath, []byte{}, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := writeChecksum(filePath); err != nil {
		t.Fatalf("writeChecksum on empty file: %v", err)
	}
}

// --- writeLatestJSON ---

func TestWriteLatestJSON_Content(t *testing.T) {
	dir := t.TempDir()
	if err := writeLatestJSON(dir, 1042, "4.2.1", "1042.xapk"); err != nil {
		t.Fatalf("writeLatestJSON: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lj latestJSON
	if err := json.Unmarshal(b, &lj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if lj.VersionCode != 1042 {
		t.Errorf("VersionCode = %d, want 1042", lj.VersionCode)
	}
	if lj.VersionName != "4.2.1" {
		t.Errorf("VersionName = %q, want 4.2.1", lj.VersionName)
	}
	if lj.File != "1042.xapk" {
		t.Errorf("File = %q, want 1042.xapk", lj.File)
	}
	if lj.UpdatedAt == "" {
		t.Error("UpdatedAt must not be empty")
	}
}

func TestWriteLatestJSON_Overwrites(t *testing.T) {
	dir := t.TempDir()
	_ = writeLatestJSON(dir, 100, "1.0.0", "100.xapk")
	if err := writeLatestJSON(dir, 200, "2.0.0", "200.xapk"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "latest.json"))
	var lj latestJSON
	_ = json.Unmarshal(b, &lj)
	if lj.VersionCode != 200 {
		t.Errorf("expected version 200 after overwrite, got %d", lj.VersionCode)
	}
}

func TestWriteLatestJSON_NoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeLatestJSON(dir, 1, "1.0", "1.apk"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "latest.json.tmp")); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up after writeLatestJSON")
	}
}

// --- pruneOldVersions ---

func TestPruneOldVersions_UnderLimit(t *testing.T) {
	dir := t.TempDir()
	createVersionFile(t, dir, 1042, ".xapk")
	createVersionFile(t, dir, 1039, ".xapk")

	if err := pruneOldVersions(dir, 3); err != nil {
		t.Fatal(err)
	}
	if n := countVersionFiles(t, dir); n != 2 {
		t.Errorf("want 2 files (under limit), got %d", n)
	}
}

func TestPruneOldVersions_ExactLimit(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []int{100, 101, 102} {
		createVersionFile(t, dir, v, ".xapk")
	}
	if err := pruneOldVersions(dir, 3); err != nil {
		t.Fatal(err)
	}
	if n := countVersionFiles(t, dir); n != 3 {
		t.Errorf("want 3 files at exact limit, got %d", n)
	}
}

func TestPruneOldVersions_KeepsNewest(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []int{100, 101, 102, 103, 104} {
		createVersionFile(t, dir, v, ".xapk")
	}
	if err := pruneOldVersions(dir, 3); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{102, 103, 104} {
		if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(v)+".xapk")); err != nil {
			t.Errorf("version %d should be kept: %v", v, err)
		}
	}
	for _, v := range []int{100, 101} {
		if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(v)+".xapk")); !os.IsNotExist(err) {
			t.Errorf("version %d should be deleted", v)
		}
	}
}

func TestPruneOldVersions_SidecarsCleaned(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []int{1, 2, 3, 4} {
		createVersionFile(t, dir, v, ".apk")
		os.WriteFile(filepath.Join(dir, strconv.Itoa(v)+".apk.sha256"), []byte("abc\n"), 0640)
	}
	if err := pruneOldVersions(dir, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.apk")); !os.IsNotExist(err) {
		t.Error("1.apk should be pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "1.apk.sha256")); !os.IsNotExist(err) {
		t.Error("1.apk.sha256 sidecar should be pruned with its APK")
	}
}

func TestPruneOldVersions_IgnoresNonVersionFiles(t *testing.T) {
	dir := t.TempDir()
	createVersionFile(t, dir, 1, ".xapk")
	os.WriteFile(filepath.Join(dir, "latest.json"), []byte("{}"), 0640)

	if err := pruneOldVersions(dir, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "latest.json")); err != nil {
		t.Error("latest.json must not be deleted by prune")
	}
}

func TestPruneOldVersions_MixedExtensions(t *testing.T) {
	dir := t.TempDir()
	createVersionFile(t, dir, 900, ".apk")
	createVersionFile(t, dir, 950, ".apk")
	createVersionFile(t, dir, 1000, ".xapk")
	createVersionFile(t, dir, 1050, ".xapk")

	if err := pruneOldVersions(dir, 3); err != nil {
		t.Fatal(err)
	}
	if n := countVersionFiles(t, dir); n != 3 {
		t.Errorf("want 3 files after prune, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "900.apk")); !os.IsNotExist(err) {
		t.Error("900.apk (oldest) should be pruned")
	}
}

// --- writeSingleAPK ---

func TestWriteSingleAPK_CopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.apk")
	if err := os.WriteFile(src, []byte("fake-apk-bytes"), 0640); err != nil {
		t.Fatal(err)
	}
	outPath, err := writeSingleAPK(dir, 1042, map[string]string{"base.apk": src})
	if err != nil {
		t.Fatalf("writeSingleAPK: %v", err)
	}
	if filepath.Base(outPath) != "1042.apk" {
		t.Errorf("output filename = %q, want 1042.apk", filepath.Base(outPath))
	}
	got, _ := os.ReadFile(outPath)
	if string(got) != "fake-apk-bytes" {
		t.Errorf("content mismatch: %q", string(got))
	}
}

func TestWriteSingleAPK_NoTempFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.apk")
	os.WriteFile(src, []byte("x"), 0640)
	outPath, _ := writeSingleAPK(dir, 1, map[string]string{"base.apk": src})
	if _, err := os.Stat(outPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up after writeSingleAPK")
	}
}

// --- writeXAPK ---

func TestWriteXAPK_ContainsAllSplits(t *testing.T) {
	dir := t.TempDir()
	collected := map[string]string{
		"base.apk":                   makeFakeAPK(t, dir, "base"),
		"split_config.arm64_v8a.apk": makeFakeAPK(t, dir, "arm64"),
		"split_config.en.apk":        makeFakeAPK(t, dir, "en"),
	}
	outPath, err := writeXAPK(dir, "com.example.app", 1042, "4.2.1", collected, []string{"arm64-v8a"})
	if err != nil {
		t.Fatalf("writeXAPK: %v", err)
	}
	if filepath.Base(outPath) != "1042.xapk" {
		t.Errorf("filename = %q, want 1042.xapk", filepath.Base(outPath))
	}
	entries := xapkEntrySet(t, outPath)
	for name := range collected {
		if !entries[name] {
			t.Errorf("XAPK missing entry: %q", name)
		}
	}
	if !entries["manifest.json"] {
		t.Error("XAPK missing manifest.json")
	}
}

func TestWriteXAPK_ManifestFields(t *testing.T) {
	dir := t.TempDir()
	collected := map[string]string{
		"base.apk":                   makeFakeAPK(t, dir, "base"),
		"split_config.arm64_v8a.apk": makeFakeAPK(t, dir, "arm64"),
	}
	outPath, _ := writeXAPK(dir, "com.example.app", 1042, "4.2.1", collected, []string{"arm64-v8a", "x86_64"})

	zr, err := zip.OpenReader(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var manifest xapkManifest
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			json.Unmarshal(b, &manifest)
		}
	}
	if manifest.XAPKVersion != 2 {
		t.Errorf("XAPKVersion = %d, want 2", manifest.XAPKVersion)
	}
	if manifest.PackageName != "com.example.app" {
		t.Errorf("PackageName = %q", manifest.PackageName)
	}
	if manifest.VersionCode != "1042" {
		t.Errorf("VersionCode = %q, want 1042", manifest.VersionCode)
	}
	if manifest.VersionName != "4.2.1" {
		t.Errorf("VersionName = %q, want 4.2.1", manifest.VersionName)
	}
	if len(manifest.Architectures) != 2 {
		t.Errorf("want 2 architectures, got %v", manifest.Architectures)
	}
	if len(manifest.SplitAPKs) == 0 {
		t.Error("split_apks must not be empty")
	}
}

func TestWriteXAPK_Deterministic(t *testing.T) {
	dir := t.TempDir()
	collected := map[string]string{
		"base.apk":                   makeFakeAPK(t, dir, "base"),
		"split_config.arm64_v8a.apk": makeFakeAPK(t, dir, "arm64"),
		"split_config.en.apk":        makeFakeAPK(t, dir, "en"),
	}
	abis := []string{"arm64-v8a"}

	dir2 := t.TempDir()
	p1, _ := writeXAPK(dir, "com.example.app", 1, "1.0", collected, abis)
	p2, _ := writeXAPK(dir2, "com.example.app", 1, "1.0", collected, abis)

	names1 := xapkEntryNames(t, p1)
	names2 := xapkEntryNames(t, p2)
	if strings.Join(names1, ",") != strings.Join(names2, ",") {
		t.Errorf("zip entry order is non-deterministic:\n  run1: %v\n  run2: %v", names1, names2)
	}
}

func TestWriteXAPK_IsValidZip(t *testing.T) {
	dir := t.TempDir()
	collected := map[string]string{"base.apk": makeFakeAPK(t, dir, "base")}
	outPath, err := writeXAPK(dir, "com.example.app", 1, "1.0", collected, nil)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(outPath)
	if err != nil {
		t.Fatalf("output is not a valid zip file: %v", err)
	}
	zr.Close()
}

func TestWriteXAPK_NoTempFile(t *testing.T) {
	dir := t.TempDir()
	collected := map[string]string{"base.apk": makeFakeAPK(t, dir, "base")}
	outPath, _ := writeXAPK(dir, "com.example.app", 1, "1.0", collected, nil)
	if _, err := os.Stat(outPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up after successful writeXAPK")
	}
}

// --- helpers ---

func makeFakeAPK(t *testing.T, dir, tag string) string {
	t.Helper()
	name := tag + ".apk"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("PK fake apk content "+tag), 0640); err != nil {
		t.Fatal(err)
	}
	return p
}

func createVersionFile(t *testing.T, dir string, version int, ext string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(version)+ext), []byte("content"), 0640); err != nil {
		t.Fatal(err)
	}
}

func countVersionFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".apk") || strings.HasSuffix(name, ".xapk") {
			n++
		}
	}
	return n
}

func xapkEntrySet(t *testing.T, path string) map[string]bool {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	m := map[string]bool{}
	for _, f := range zr.File {
		m[f.Name] = true
	}
	return m
}

func xapkEntryNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}
