package extractor

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/richardcornall/apk-distributor/adb"
	"github.com/richardcornall/apk-distributor/sink"
)

// Extract pulls APKs for pkg from all clients, bundles them, and writes versioned output
// under outputDir/pkg/. Calls s.OnArtifact after a successful write.
//
// maxAPKSizeMB is a hard ceiling applied to each individual pulled file — set to 0 to
// use the package default of 1024 MB.
//
// expectedCerts maps package name to its expected SHA-256 signing certificate fingerprint
// (64 lowercase hex chars, pre-normalised by config). If an entry exists for pkg, every
// pulled APK is verified with apksigner before being accepted. A fingerprint mismatch
// fails the extraction — a compromised emulator cannot forge the developer's signing cert.
func Extract(ctx context.Context, pkg string, versionCode int, versionName string, outputDir string, maxAPKSizeMB int, expectedCerts map[string]string, clients []adb.Device, s sink.Sink) (string, error) {
	if maxAPKSizeMB <= 0 {
		maxAPKSizeMB = 1024
	}
	maxBytes := int64(maxAPKSizeMB) * 1024 * 1024

	pkgDir := filepath.Join(outputDir, pkg)
	if err := os.MkdirAll(pkgDir, 0750); err != nil {
		return "", fmt.Errorf("create package dir: %w", err)
	}

	// Use a private temp dir inside outputDir so pulled APKs are never
	// readable by other OS users while in transit. System temp is world-readable
	// on most platforms.
	tmpBase := filepath.Join(outputDir, ".tmp")
	if err := os.MkdirAll(tmpBase, 0700); err != nil {
		return "", fmt.Errorf("create private temp base: %w", err)
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "extract-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// collected maps sanitised filename → local temp path.
	// First write wins for shared splits (base, density, language) so we avoid
	// duplicating identical content from multiple emulators.
	collected := map[string]string{}
	isSplit := false

	expectedCert := expectedCerts[pkg]
	for _, client := range clients {
		split, err := pullFromClient(ctx, client, pkg, tmpDir, maxBytes, expectedCert, collected)
		if err != nil {
			return "", err
		}
		if split {
			isSplit = true
		}
	}

	if len(collected) == 0 {
		return "", fmt.Errorf("no APKs collected for %s — all emulators may be offline or package not installed", pkg)
	}

	abis := make([]string, 0, len(clients))
	for _, c := range clients {
		abis = append(abis, c.ABI())
	}

	var outPath string
	if isSplit {
		outPath, err = writeXAPK(pkgDir, pkg, versionCode, versionName, collected, abis)
	} else {
		outPath, err = writeSingleAPK(pkgDir, versionCode, collected)
	}
	if err != nil {
		return "", err
	}

	checksum, err := writeChecksum(outPath)
	if err != nil {
		return "", fmt.Errorf("write checksum: %w", err)
	}
	if err := writeLatestJSON(pkgDir, versionCode, versionName, filepath.Base(outPath)); err != nil {
		return "", fmt.Errorf("write latest.json: %w", err)
	}
	if err := pruneOldVersions(pkgDir, 3); err != nil {
		slog.Error("failed to prune old versions", "pkg", pkg, "err", err)
	}

	absPath, _ := filepath.Abs(outPath)
	if err := s.OnArtifact(ctx, sink.Artifact{
		Package:     pkg,
		VersionCode: versionCode,
		VersionName: versionName,
		Path:        absPath,
		Checksum:    checksum,
		IsXAPK:      isSplit,
	}); err != nil {
		slog.Error("sink OnArtifact failed", "pkg", pkg, "err", err)
	}
	return outPath, nil
}

// pullFromClient pulls all APK splits for pkg from a single client into collected.
// Returns true if the app has multiple splits (indicating an XAPK is needed).
func pullFromClient(ctx context.Context, client adb.Device, pkg, tmpDir string, maxBytes int64, expectedCert string, collected map[string]string) (isSplit bool, err error) {
	if !client.IsConnected() {
		slog.Warn("skipping offline emulator during extraction", "serial", client.Serial(), "abi", client.ABI(), "pkg", pkg)
		return false, nil
	}
	devicePaths, err := client.PullAPKPaths(pkg)
	if err != nil {
		slog.Error("failed to get APK paths", "pkg", pkg, "serial", client.Serial(), "err", err)
		return false, nil
	}
	if len(devicePaths) > 1 {
		isSplit = true
	}
	emulatorDir := filepath.Join(tmpDir, client.ABI())
	if err := os.MkdirAll(emulatorDir, 0700); err != nil {
		return false, err
	}
	for _, devicePath := range devicePaths {
		if err := pullOneAPK(ctx, client, devicePath, emulatorDir, maxBytes, expectedCert, collected); err != nil {
			return false, err
		}
	}
	return isSplit, nil
}

// pullOneAPK pulls a single APK from the device, enforces the size limit,
// verifies the signing certificate if configured, and adds it to collected.
func pullOneAPK(ctx context.Context, client adb.Device, devicePath, emulatorDir string, maxBytes int64, expectedCert string, collected map[string]string) error {
	filename, err := safeFilename(devicePath)
	if err != nil {
		return fmt.Errorf("unsafe path from device on %s: %w", client.Serial(), err)
	}
	if _, exists := collected[filename]; exists {
		return nil // shared split already pulled from another emulator
	}
	localPath := filepath.Join(emulatorDir, filename)
	if err := client.PullFile(devicePath, localPath); err != nil {
		return fmt.Errorf("pull %s from %s: %w", filename, client.Serial(), err)
	}
	if info, err := os.Stat(localPath); err == nil && info.Size() > maxBytes {
		os.Remove(localPath)
		return fmt.Errorf("APK %s from %s is %d MB, exceeds limit — aborting extraction",
			filename, client.Serial(), info.Size()/(1024*1024))
	}
	if expectedCert != "" {
		if err := verifyAPKSignature(ctx, localPath, expectedCert); err != nil {
			os.Remove(localPath)
			return fmt.Errorf("signature verification failed for %s pulled from %s: %w", filename, client.Serial(), err)
		}
	}
	collected[filename] = localPath
	return nil
}

// safeFilename extracts the base filename from a device path and validates it.
// Requires a .apk extension, safe characters only, and no leading dot.
// Prevents a compromised emulator from injecting arbitrary filenames into the output dir.
func safeFilename(devicePath string) (string, error) {
	name := path.Base(devicePath) // device paths are Linux — use path, not filepath
	if !strings.HasSuffix(name, ".apk") {
		return "", fmt.Errorf("filename %q does not have .apk extension", name)
	}
	for _, r := range name {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '_' || r == '-' || r == '.') {
			return "", fmt.Errorf("filename %q contains disallowed character %q", name, r)
		}
	}
	if name == "" || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("filename %q is not allowed", name)
	}
	return name, nil
}

func writeSingleAPK(pkgDir string, versionCode int, collected map[string]string) (string, error) {
	var src string
	for _, p := range collected {
		src = p
		break
	}
	outPath := filepath.Join(pkgDir, strconv.Itoa(versionCode)+".apk")
	return outPath, atomicCopy(src, outPath)
}

type xapkManifest struct {
	XAPKVersion   int          `json:"xapk_version"`
	PackageName   string       `json:"package_name"`
	VersionCode   string       `json:"version_code"`
	VersionName   string       `json:"version_name"`
	Architectures []string     `json:"architectures"`
	SplitAPKs     []splitEntry `json:"split_apks"`
}

type splitEntry struct {
	File string `json:"file"`
	ID   string `json:"id"`
}

func writeXAPK(pkgDir, pkg string, versionCode int, versionName string, collected map[string]string, abis []string) (string, error) {
	outPath := filepath.Join(pkgDir, strconv.Itoa(versionCode)+".xapk")
	tmpPath := outPath + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return "", err
	}

	// Sorted filenames for deterministic zip output.
	filenames := make([]string, 0, len(collected))
	for name := range collected {
		filenames = append(filenames, name)
	}
	sort.Strings(filenames)

	zw := zip.NewWriter(f)
	var splits []splitEntry

	for _, name := range filenames {
		splits = append(splits, splitEntry{File: name, ID: splitID(name)})

		zEntry, err := zw.Create(name)
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(tmpPath)
			return "", fmt.Errorf("zip create entry %s: %w", name, err)
		}
		src, err := os.Open(collected[name])
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(tmpPath)
			return "", err
		}
		_, copyErr := io.Copy(zEntry, src)
		src.Close()
		if copyErr != nil {
			zw.Close()
			f.Close()
			os.Remove(tmpPath)
			return "", fmt.Errorf("zip write %s: %w", name, copyErr)
		}
	}

	manifest := xapkManifest{
		XAPKVersion:   2,
		PackageName:   pkg,
		VersionCode:   strconv.Itoa(versionCode),
		VersionName:   versionName,
		Architectures: abis,
		SplitAPKs:     splits,
	}
	mj, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		zw.Close()
		f.Close()
		os.Remove(tmpPath)
		return "", err
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		zw.Close()
		f.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if _, err := mw.Write(mj); err != nil {
		zw.Close()
		f.Close()
		os.Remove(tmpPath)
		return "", err
	}

	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return outPath, nil
}

// splitID derives the XAPK split ID from an APK filename.
// "base.apk" → "base", "split_config.arm64_v8a.apk" → "config.arm64_v8a"
func splitID(filename string) string {
	name := strings.TrimSuffix(filename, ".apk")
	if name == "base" {
		return "base"
	}
	return strings.TrimPrefix(name, "split_")
}

type latestJSON struct {
	VersionCode int    `json:"version_code"`
	VersionName string `json:"version_name"`
	File        string `json:"file"`
	UpdatedAt   string `json:"updated_at"`
}

func writeLatestJSON(pkgDir string, versionCode int, versionName, filename string) error {
	data := latestJSON{
		VersionCode: versionCode,
		VersionName: versionName,
		File:        filename,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(pkgDir, "latest.json.tmp")
	if err := os.WriteFile(tmp, b, 0640); err != nil {
		return err
	}
	dst := filepath.Join(pkgDir, "latest.json")
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func writeChecksum(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return sum, os.WriteFile(filePath+".sha256", []byte(sum+"\n"), 0640)
}

// atomicCopy copies src to dst via a same-directory temp file and rename,
// ensuring dst is never in a partially-written state.
func atomicCopy(src, dst string) error {
	tmp := dst + ".tmp"
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		in.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		in.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	in.Close()
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// pruneOldVersions keeps the `keep` most recent versioned APK/XAPK files in pkgDir,
// deleting older ones with their .sha256 sidecars. Runs after store is updated.
func pruneOldVersions(pkgDir string, keep int) error {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return err
	}

	type versioned struct {
		code int
		name string
	}
	var files []versioned
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var ext string
		switch {
		case strings.HasSuffix(name, ".xapk"):
			ext = ".xapk"
		case strings.HasSuffix(name, ".apk"):
			ext = ".apk"
		default:
			continue
		}
		code, err := strconv.Atoi(strings.TrimSuffix(name, ext))
		if err != nil {
			continue
		}
		files = append(files, versioned{code: code, name: name})
	}

	if len(files) <= keep {
		return nil
	}

	sort.Slice(files, func(i, j int) bool { return files[i].code > files[j].code })
	for _, f := range files[keep:] {
		base := filepath.Join(pkgDir, f.name)
		os.Remove(base)
		os.Remove(base + ".sha256")
		slog.Debug("pruned old version", "file", f.name)
	}
	return nil
}
