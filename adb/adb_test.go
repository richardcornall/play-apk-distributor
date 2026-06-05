package adb

import (
	"strings"
	"testing"
)

// --- parseVersionInfo ---

func TestParseVersionInfo_Typical(t *testing.T) {
	output := `
Packages:
  Package [com.example.app] (base.apk):
    userId=10234
    versionCode=1042 minSdk=21 targetSdk=33
    versionName=4.2.1
    flags=[ SYSTEM HAS_CODE ALLOW_CLEAR_USER_DATA ]
`
	info, err := parseVersionInfo("com.example.app", output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Code != 1042 {
		t.Errorf("Code = %d, want 1042", info.Code)
	}
	if info.Name != "4.2.1" {
		t.Errorf("Name = %q, want 4.2.1", info.Name)
	}
}

func TestParseVersionInfo_HighVersionCode(t *testing.T) {
	output := `
  Package [com.example.app] (base.apk):
    versionCode=999999999 minSdk=26 targetSdk=34
    versionName=99.9.999
`
	info, err := parseVersionInfo("com.example.app", output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Code != 999999999 {
		t.Errorf("Code = %d, want 999999999", info.Code)
	}
}

func TestParseVersionInfo_PackageNotFound(t *testing.T) {
	_, err := parseVersionInfo("com.example.app", "Packages:\n  (nothing)\n")
	if err == nil {
		t.Fatal("expected error for missing package")
	}
}

func TestParseVersionInfo_EmptyOutput(t *testing.T) {
	_, err := parseVersionInfo("com.example.app", "")
	if err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestParseVersionInfo_WrongPackage(t *testing.T) {
	// Output contains a different package — our target is not present.
	output := `
  Package [com.other.app] (base.apk):
    versionCode=5 minSdk=21 targetSdk=33
    versionName=1.0.0
`
	_, err := parseVersionInfo("com.example.app", output)
	if err == nil {
		t.Fatal("expected error when target package not in output")
	}
}

func TestParseVersionInfo_MultiplePackages_PicksCorrect(t *testing.T) {
	// dumpsys might return multiple packages in some edge cases.
	output := `
  Package [com.other.app] (base.apk):
    versionCode=1 minSdk=21 targetSdk=33
    versionName=1.0.0
  Package [com.example.app] (base.apk):
    versionCode=42 minSdk=21 targetSdk=33
    versionName=0.42.0
`
	info, err := parseVersionInfo("com.example.app", output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Code != 42 {
		t.Errorf("Code = %d, want 42 (should ignore com.other.app)", info.Code)
	}
}

func TestParseVersionInfo_MissingVersionName(t *testing.T) {
	// versionCode present but no versionName — should still succeed with empty name.
	output := `
  Package [com.example.app] (base.apk):
    versionCode=7 minSdk=21 targetSdk=33
`
	info, err := parseVersionInfo("com.example.app", output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Code != 7 {
		t.Errorf("Code = %d, want 7", info.Code)
	}
}

func TestParseVersionInfo_WindowsLineEndings(t *testing.T) {
	output := "  Package [com.example.app] (base.apk):\r\n    versionCode=55 minSdk=21 targetSdk=33\r\n    versionName=5.5\r\n"
	info, err := parseVersionInfo("com.example.app", output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Code != 55 {
		t.Errorf("Code = %d, want 55", info.Code)
	}
}

// --- parsePMPaths ---

func TestParsePMPaths_SingleAPK(t *testing.T) {
	output := "package:/data/app/~~abc123/com.example.app-xyz/base.apk\n"
	paths, err := parsePMPaths(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("want 1 path, got %d", len(paths))
	}
	if paths[0] != "/data/app/~~abc123/com.example.app-xyz/base.apk" {
		t.Errorf("unexpected path: %q", paths[0])
	}
}

func TestParsePMPaths_SplitAPKs(t *testing.T) {
	output := `package:/data/app/~~abc/com.example.app-1/base.apk
package:/data/app/~~abc/com.example.app-1/split_config.arm64_v8a.apk
package:/data/app/~~abc/com.example.app-1/split_config.en.apk
package:/data/app/~~abc/com.example.app-1/split_config.xxhdpi.apk
`
	paths, err := parsePMPaths(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 4 {
		t.Errorf("want 4 paths, got %d: %v", len(paths), paths)
	}
}

func TestParsePMPaths_EmptyOutput(t *testing.T) {
	_, err := parsePMPaths("")
	if err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestParsePMPaths_NoPackagePrefix(t *testing.T) {
	// Output with no "package:" prefix (e.g. error message).
	_, err := parsePMPaths("error: package not found\n")
	if err == nil {
		t.Fatal("expected error when no package: lines present")
	}
}

func TestParsePMPaths_TrailingWhitespace(t *testing.T) {
	output := "package:/data/app/~~abc/com.example.app-1/base.apk   \r\n"
	paths, err := parsePMPaths(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("want 1 path, got %d", len(paths))
	}
	if strings.ContainsAny(paths[0], " \t\r\n") {
		t.Errorf("path contains whitespace: %q", paths[0])
	}
}

func TestParsePMPaths_MixedContent(t *testing.T) {
	// Some devices emit extra lines before or after.
	output := `WARNING: linker: libdvm.so has text relocations.
package:/data/app/~~xyz/com.example.app-1/base.apk
`
	paths, err := parsePMPaths(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/data/app/~~xyz/com.example.app-1/base.apk" {
		t.Errorf("unexpected paths: %v", paths)
	}
}
