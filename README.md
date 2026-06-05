# play-apk-distributor

[![CI](https://github.com/richardcornall/play-apk-distributor/actions/workflows/ci.yml/badge.svg)](https://github.com/richardcornall/play-apk-distributor/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/richardcornall/play-apk-distributor)](https://goreportcard.com/report/github.com/richardcornall/play-apk-distributor)
[![Go 1.22+](https://img.shields.io/badge/go-1.22+-blue.svg)](https://golang.org/dl/)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

A self-hosted Go service that monitors headless Android emulators, detects Play Store app updates, extracts the latest APKs, and writes them to a local directory — ready to be consumed by any downstream system. 

Built for production MDM pipelines, fleet management, cloud APK distribution, automated testing, and any other use case where you need the canonical, Play-Store-sourced APK for an Android package without manual intervention.

---

## Table of contents

- [Why this exists](#why-this-exists)
- [Use cases](#use-cases)
- [How it works](#how-it-works)
- [Architecture](#architecture)
- [Requirements](#requirements)
- [Quick start](#quick-start)
- [Configuration reference](#configuration-reference)
- [Multi-ABI and XAPK support](#multi-abi-and-xapk-support)
- [Output format](#output-format)
- [HTTP API](#http-api)
- [Sink interface](#sink-interface)
- [Security](#security)
- [Running in production](#running-in-production)
- [Contributing](#contributing)

---

## Why this exists

Getting the latest version of an Android app from Google Play programmatically is surprisingly hard. The Play Store has no public API for APK download. Third-party APK mirror sites are often outdated, unverifiable, or legally grey. Play Protect and signing certificate chains exist precisely to prevent unsigned or tampered APKs from reaching devices — but most workarounds bypass these entirely.

**apk-distributor takes a different approach:** it uses a real Android emulator running a real Google Play Store to install apps exactly as a device would, then extracts the installed APKs directly from the emulator filesystem via ADB. The result is bit-for-bit identical to what Play would push to a physical device — correct ABI splits, correct signing certificates, no third parties involved.

The service runs continuously, polls on a configurable interval, detects version changes, and writes new artifacts to disk. Everything downstream — MDM uploads, S3 syncs, CI/CD pipelines — reads from that directory.

---

## Use cases

**Mobile Device Management (MDM)**
The primary use case. MDM platforms (Jamf, Microsoft Intune, Workspace ONE, custom solutions) often need to serve APKs to managed fleets without routing every device through the Play Store. apk-distributor provides a continuously updated, locally hosted APK source for MDM upload pipelines. Particularly useful for carer devices, logistics hardware, kiosks, and other managed fleets where direct Play Store access is restricted or unreliable.

**Cloud and object storage distribution**
Implement the `Sink` interface to push every new artifact to S3, GCS, Azure Blob, or any other object store. Your fleet downloads from your own CDN, not Google's infrastructure. No per-device Play Store authentication, no geographic rate limits.

**APK mirroring and archiving**
Maintain a versioned archive of every release of every app you depend on. The output directory keeps the three most recent versions per package by default (configurable). Roll back to any prior version without re-extracting.

**Automated testing pipelines**
CI/CD systems that run instrumented Android tests need a reliable APK source. Wire apk-distributor into your build pipeline: the test runner polls the output directory (or listens via webhook through a custom Sink) for new artifacts and triggers test runs automatically.

**Multi-architecture fleet support**
One run of apk-distributor can extract APKs for every ABI your fleet uses — x86_64, arm64-v8a, armeabi-v7a, x86 — and bundle them into a single XAPK. Downstream devices receive the correct splits regardless of architecture, without you managing per-architecture distribution separately.

**Offline and air-gapped environments**
Organisations that operate Android fleets in network-restricted environments can use apk-distributor on a management host with Play Store access, then serve APKs to the fleet from internal infrastructure.

**Compliance and audit**
Every extracted artifact gets a SHA-256 checksum sidecar and a `latest.json` metadata file. Certificate pinning (see [Security](#security)) ensures every file in the output directory was signed by the expected developer key. This gives you a verifiable, tamper-evident audit trail of every app version deployed to managed devices.

---

## How it works

```
┌─────────────────────────────────────────────────────────────┐
│  Configured emulator(s)                                     │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐   │
│  │  x86_64 AVD  │   │ arm64-v8a    │   │  armeabi-v7a │   │
│  │  Play Store  │   │  AVD         │   │  AVD         │   │
│  └──────┬───────┘   └──────┬───────┘   └──────┬───────┘   │
│         │  ADB             │  ADB             │  ADB       │
└─────────┼──────────────────┼──────────────────┼────────────┘
          │                  │                  │
          └──────────────────┴──────────────────┘
                             │
                    ┌────────▼────────┐
                    │    Watcher      │
                    │  poll loop      │
                    │  version agree  │
                    └────────┬────────┘
                             │  version changed
                    ┌────────▼────────┐
                    │   Extractor     │
                    │  pull APKs      │
                    │  verify certs   │
                    │  bundle XAPK    │
                    │  write checksum │
                    └────────┬────────┘
                             │
              ┌──────────────┴──────────────┐
              │                             │
    ┌─────────▼──────────┐      ┌──────────▼──────────┐
    │   Output directory  │      │   Sink interface    │
    │   apks/             │      │   MDM / S3 / hook   │
    │   com.example.app/  │      │   (implement yours) │
    │   ├─ 1042.xapk      │      └─────────────────────┘
    │   ├─ 1042.xapk.sha256
    │   └─ latest.json    │
    └────────────────────┘
```

1. The watcher polls every configured emulator for the installed version of each tracked package.
2. All emulators must report the **same version code** before extraction proceeds. A mismatch means Play Store hasn't finished rolling out the update and is resolved on the next poll.
3. When a new version is detected, the extractor pulls every APK split from every connected emulator over ADB.
4. If certificate pinning is configured, each pulled APK is verified against the expected SHA-256 fingerprint using `apksigner` before being accepted.
5. Splits are deduplicated (shared base/density/language APKs only appear once) and bundled into an XAPK. Single-APK apps produce a plain `.apk`.
6. The output file and a SHA-256 checksum sidecar are written atomically. A `latest.json` metadata file is updated.
7. The configured `Sink` is called with the artifact details. The default no-op sink does nothing; implement your own to push to MDM, cloud storage, or a webhook.

---

## Architecture

The codebase is intentionally structured as a library with an executable on top. Every package is independently importable and testable.

| Package | Responsibility |
|---|---|
| `adb` | Wraps ADB commands to connect to emulators, query installed package versions, and pull APK files. Exposes a `Device` interface for testability. |
| `config` | Reads and validates `config.yaml`. Hot-reloads on file change via fsnotify without restarting the process. All values are validated at load time; an invalid reload leaves the current config in place. |
| `extractor` | Pulls APK files from one or more devices, verifies signing certificates, deduplicates shared splits, bundles XAPKs, writes checksums, and calls the sink. |
| `watcher` | The poll loop. Queries all emulators in parallel, enforces version agreement, compares against the store, and drives extraction on change. |
| `packages` | A thread-safe, file-backed set of package names persisted to `packages.json`. Supports hot-reload of external edits and change callbacks. |
| `api` | An HTTP server built on Go 1.22+ `http.ServeMux` method+path routing. Manages tracked packages at runtime. Bearer token authenticated. |
| `sink` | The `Sink` interface and built-in implementations (`Noop`, `Fanout`). Implement this to integrate with downstream systems. |
| `store` | Persists the last successfully extracted version per package to `state/store.json` so the service survives restarts without re-extracting unchanged packages. |

---

## Requirements

- **Go 1.22 or later** — required for method+path routing in `net/http.ServeMux`.
- **ADB** (`android-tools-adb`) — must be on `PATH` or reachable from the host running apk-distributor.
- **Android SDK build-tools** — required if using certificate pinning (`expected_certs`). The service searches `ANDROID_HOME/build-tools`, common platform defaults, and `PATH` for `apksigner`. Install via Android Studio → SDK Manager → SDK Tools → Android SDK Build-Tools.
- **One or more running Android AVDs** with a Google Play system image, already signed into a Google account with the target apps installed. API 33+ (Android 13+) is recommended; API 37 is tested.

### Setting up emulators

1. Install Android Studio and open the SDK Manager.
2. Under **SDK Platforms**, install your target API level with the **Google Play** system image for each required ABI.
3. In the AVD Manager, create one device per ABI you need. Use a Google Play image — other images do not include Play Store.
4. Start each AVD with a console port: `emulator -avd <name> -port 5554`
5. Connect ADB: `adb connect localhost:5555` (console port + 1)
6. Open Play Store inside the emulator, sign in, and install your target apps.
7. Verify: `adb -s localhost:5555 shell pm list packages com.example.app` should return a result.

---

## Quick start

```bash
# Clone
git clone https://github.com/richardcornall/play-apk-distributor
cd play-apk-distributor

# Build
go build -o apk-distributor .

# Generate an API token (strongly recommended)
openssl rand -hex 32

# Edit config.yaml — add your packages, emulator port, API token
# Then run
./apk-distributor -config config.yaml
```

Extracted APKs appear under `./apks/<package.name>/<versionCode>.(apk|xapk)`.

---

## Configuration reference

All configuration lives in a single YAML file. The service hot-reloads on write — no restart needed. An invalid config is rejected and the previous values are kept.

```yaml
# ── Core ──────────────────────────────────────────────────────────────────────

# Host running the Android emulator(s).
# Accepts: hostname, IPv4, or IPv6 address. No shell metacharacters.
# Default: none (required)
adb_host: localhost

# Seconds between version checks for all configured packages.
# Lower values detect updates faster at the cost of more ADB calls.
# Default: none (required, must be > 0)
poll_interval: 30

# Directory where extracted APKs and XAPKs are written.
# Created on startup if it does not exist (mode 0750).
# Structure: output_dir/<package.name>/<versionCode>.(apk|xapk)
# Default: none (required)
output_dir: ./apks

# ── Emulators ─────────────────────────────────────────────────────────────────

# One entry per ABI. Each emulator must be running a Google Play system image
# and already have the target apps installed.
#
# ADB port = emulator console port + 1
# (e.g. `emulator -port 5554` → ADB on 5555)
#
# Valid ABIs: arm64-v8a  armeabi-v7a  x86_64  x86
#
# For a homogeneous fleet, one entry is sufficient.
# For mixed-architecture fleets, add one entry per ABI and all splits will be
# merged into a single XAPK at extraction time.
emulators:
  - port: 5555
    abi: x86_64
  # - port: 5557
  #   abi: arm64-v8a

# ── Package list ──────────────────────────────────────────────────────────────

# Android package names to track. Must be installed on all configured emulators.
# This list is static — edit config.yaml and the service reloads automatically.
# You can also manage packages dynamically via the HTTP API or by editing
# packages.json in the same directory. Both sources are merged; duplicates are
# silently deduplicated.
# Default: empty (optional — packages can be added entirely via the API)
packages:
  - com.example.app
  - com.example.other

# ── HTTP API ──────────────────────────────────────────────────────────────────

# Bind address for the management API. Defaults to 127.0.0.1 (localhost only).
# SECURITY: Do not expose to untrusted networks without a reverse proxy + TLS.
# Default: 127.0.0.1
api_host: 127.0.0.1

# Port for the management API. Set to 0 or omit to disable entirely.
# Valid range: 1–65535
# Default: 0 (disabled)
api_port: 8080

# Bearer token required on all API endpoints except /health.
# STRONGLY recommended — without this, any local process can add or remove
# tracked packages, and therefore influence which APKs reach your fleet.
# Generate with: openssl rand -hex 32
# Must be at least 32 characters.
# Keep this file owner-readable only: chmod 600 config.yaml
# Default: empty (no auth — warns at startup)
api_token: replace-this-with-output-of-openssl-rand-hex-32

# ── Size limiting ─────────────────────────────────────────────────────────────

# Hard ceiling on each individual APK file pulled from an emulator (megabytes).
# If a pulled file exceeds this, extraction is aborted and the file is deleted.
# Prevents a compromised emulator from exhausting disk by returning an oversized
# or synthetic file.
# Real-world APKs are typically under 500 MB. Teams (a large split app) is ~300 MB
# per split. Set higher if your apps require it.
# Default: 1024
max_apk_size_mb: 1024

# ── Certificate pinning ───────────────────────────────────────────────────────

# Per-package expected signing certificate SHA-256 fingerprints.
# STRONGLY recommended for production.
#
# If configured for a package, every APK pulled for that package is verified
# with `apksigner verify --print-certs` before being accepted. A fingerprint
# mismatch causes extraction to fail — the file is deleted and never written
# to the output directory.
#
# This is the deepest layer of defence: even a fully compromised emulator with
# a replaced Play Store cannot forge the developer's signing certificate.
#
# Fingerprint formats accepted:
#   Plain hex:         aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899
#   Colon-separated:   AA:BB:CC:DD:EE:FF:... (as printed by apksigner and keytool)
# Both are normalised to lowercase hex at load time.
#
# How to get the fingerprint from a known-good APK:
#   apksigner verify --print-certs path/to/app.apk
#   keytool -printcert -jarfile path/to/app.apk
#
# Requires apksigner from Android SDK build-tools. Set ANDROID_HOME or add
# build-tools to PATH. If expected_certs is set for a package and apksigner
# cannot be found, extraction fails rather than proceeding unverified.
#
# Default: empty (no certificate verification)
# expected_certs:
#   com.example.app: "AA:BB:CC:DD:EE:FF:..."
#   com.example.other: "aabbccddeeff..."
```

### Full field summary

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `adb_host` | string | yes | — | Hostname or IP of the ADB host |
| `poll_interval` | int (seconds) | yes | — | Seconds between version checks |
| `output_dir` | string | yes | — | Directory for extracted APKs |
| `emulators` | list | yes | — | One entry per ABI |
| `emulators[].port` | int | yes | — | ADB port (console port + 1) |
| `emulators[].abi` | string | yes | — | One of: `arm64-v8a`, `armeabi-v7a`, `x86_64`, `x86` |
| `packages` | list | no | `[]` | Static package names to track |
| `api_host` | string | no | `127.0.0.1` | API bind address |
| `api_port` | int | no | `0` (off) | API port; 0 disables the API |
| `api_token` | string | no | `""` (warn) | Bearer token; min 32 chars |
| `max_apk_size_mb` | int | no | `1024` | Per-file size ceiling in MB |
| `expected_certs` | map | no | `{}` | Package → SHA-256 cert fingerprint |

---

## Multi-ABI and XAPK support

Google Play installs only the ABI-specific native splits relevant to each device. An x86_64 emulator will only have `split_config.x86_64.apk`; an arm64-v8a emulator will only have `split_config.arm64_v8a.apk`. To distribute an APK to a mixed-architecture fleet, you need both.

apk-distributor solves this by running one emulator per target ABI simultaneously. At extraction time:

1. Each emulator is queried for its version of each tracked package.
2. All emulators must agree on the version code before extraction starts. A mismatch (one emulator hasn't updated yet) causes the cycle to be skipped and retried next poll.
3. APK paths are pulled from every emulator. Splits shared across ABIs (base APK, density splits, language splits) are deduplicated — if both emulators have `base.apk`, it is only pulled once.
4. All unique splits are bundled into an `.xapk` file (a ZIP containing all APK files and a `manifest.json`).
5. A single XAPK is written to the output directory. Downstream MDM systems or installers that support XAPK receive the right splits for each device automatically.

For single-ABI fleets or apps that produce only a base APK (no splits), the output is a plain `.apk`.

XAPK `manifest.json` conforms to the XAPK v2 format and includes `package_name`, `version_code`, `version_name`, `architectures`, and a `split_apks` list with file and split-ID entries.

---

## Output format

For each tracked package, the output directory contains:

```
output_dir/
└── com.example.app/
    ├── 1042.xapk            # versioned artifact (or .apk for single-split apps)
    ├── 1042.xapk.sha256     # SHA-256 hex checksum (one line, no filename)
    ├── 1039.xapk            # previous version (up to 3 kept by default)
    ├── 1039.xapk.sha256
    └── latest.json          # metadata for the most recent version
```

`latest.json` schema:
```json
{
  "version_code": 1042,
  "version_name": "10.4.2",
  "file": "1042.xapk",
  "updated_at": "2026-06-05T14:23:01Z"
}
```

The three most recent versioned files are kept. Older artifacts and their `.sha256` sidecars are pruned automatically after each successful extraction.

The internal state directory (`output_dir/state/store.json`) records the last successfully extracted version of each package and survives service restarts. The private temp directory (`output_dir/.tmp/`) is used for in-flight pulls and is cleared on startup; APKs there are never readable by other OS users.

---

## HTTP API

The management API is optional. Enable it by setting `api_port` in config.yaml.

All endpoints except `/health` require a `Authorization: Bearer <token>` header when `api_token` is configured. The token comparison uses constant-time comparison to prevent timing-based enumeration.

### Endpoints

#### `GET /health`
Liveness check. Always returns 200. Does not require authentication — safe to expose to monitoring systems.

```json
{"status": "ok"}
```

#### `GET /packages`
List all tracked packages (static from config + dynamic from packages.json) with their last-seen version info.

```json
{
  "packages": [
    {
      "name": "com.example.app",
      "version_code": 1042,
      "version_name": "10.4.2",
      "updated_at": "2026-06-05T14:23:01Z"
    }
  ]
}
```

#### `POST /packages`
Add a package to the dynamic tracking list. Requires `Content-Type: application/json`.

```json
{"name": "com.example.app"}
```

Returns `201 Created` on success, `409 Conflict` if already tracked, `400 Bad Request` for an invalid package name.

#### `GET /packages/{name}`
Get version info for a single package.

Returns `200 OK` with version info, or `404 Not Found` if not tracked.

#### `DELETE /packages/{name}`
Remove a package from the dynamic tracking list. Does not delete previously extracted artifacts.

Returns `204 No Content` on success, `404 Not Found` if not tracked.

### Package name validation

Package names must follow the Android convention: dot-separated identifiers, each starting with a letter (`[a-zA-Z][a-zA-Z0-9_]*`), with at least two segments. Requests with invalid names return `400 Bad Request`.

### Dynamic vs static packages

Packages added via the API are written to `packages.json` in the config directory. This file is hot-reloaded — external edits are picked up automatically. On startup, packages from `config.yaml` are merged into `packages.json`; duplicates are silently ignored. Both sources are always combined for polling.

---

## Sink interface

The `sink.Sink` interface is the integration point for downstream systems. It is called after every successful extraction — after the file is written to disk and the checksum is verified.

```go
type Artifact struct {
    Package     string
    VersionCode int
    VersionName string
    Path        string // absolute path to the written file
    Checksum    string // SHA-256 hex
    IsXAPK      bool
}

type Sink interface {
    OnArtifact(ctx context.Context, a Artifact) error
}
```

### Built-in implementations

| Type | Behaviour |
|---|---|
| `sink.Noop` | Does nothing. Default when you only need filesystem output. |
| `sink.Fanout` | Calls a list of sinks in sequence. First error aborts the chain. |

### Implementing your own

```go
type S3Sink struct {
    client *s3.Client
    bucket string
}

func (s *S3Sink) OnArtifact(ctx context.Context, a sink.Artifact) error {
    f, err := os.Open(a.Path)
    if err != nil {
        return err
    }
    defer f.Close()
    key := fmt.Sprintf("%s/%d%s", a.Package, a.VersionCode, filepath.Ext(a.Path))
    _, err = s.client.PutObject(ctx, &s3.PutObjectInput{
        Bucket: &s.bucket,
        Key:    &key,
        Body:   f,
    })
    return err
}
```

Wire it in `main.go`:

```go
s := sink.NewFanout(
    &S3Sink{client: s3Client, bucket: "my-apk-bucket"},
    &WebhookSink{url: "https://mdm.example.com/apk-ready"},
)
w := watcher.New(cfg, pkgMgr, st, clients, s)
```

---

## Security

apk-distributor is designed to be the APK source for production device fleets. A compromise here means potentially pushing malicious software to every managed device. Security is treated as a first-class concern throughout the codebase.

### Threat model

The primary threats are:

1. **Compromised emulator** — a rooted or modified emulator serving a tampered or malicious APK in place of the legitimate Play Store version.
2. **Network attacker** — an attacker on the same network as the service injecting packages via the HTTP API or intercepting ADB traffic.
3. **Local privilege escalation** — a process running on the same host reading APKs in transit, reading the config (and API token), or writing to the output directory.

### Mitigations implemented

**APK signature verification (certificate pinning)**
Every APK pulled from an emulator is verified with `apksigner verify --print-certs` against a configured expected SHA-256 certificate fingerprint before it is accepted. A certificate mismatch causes extraction to abort and the file to be deleted. This is the strongest defence against a compromised emulator: the developer's signing key is never on the emulator and cannot be forged. Configured via `expected_certs` in config.yaml.

**Bearer token authentication**
All API endpoints except `/health` require a `Authorization: Bearer <token>` header. The comparison uses `crypto/subtle.ConstantTimeCompare` — token length and content are compared in constant time, preventing timing-based token enumeration even on a loopback interface. The minimum token length is 32 characters. Generate with `openssl rand -hex 32`.

**ADB command injection prevention**
Every ADB invocation uses `exec.Command` with discrete arguments. No ADB commands are constructed as shell strings and no shell is ever invoked. Package names, device serials, and file paths are validated before use. A compromised emulator returning a maliciously crafted `pm dump` response cannot inject shell commands.

**Safe filename allowlist**
APK filenames pulled from the device are validated against a strict allowlist: `[a-zA-Z0-9._-]` characters only, `.apk` extension required, no leading dot. Any filename outside this set causes extraction to fail for that device. This prevents a compromised emulator from returning a path like `../../etc/cron.d/malicious`.

**Private temporary directory**
APKs in transit are written to `output_dir/.tmp/` (mode `0700`), not the system temp directory. System temp is world-readable on most platforms. Files here are only readable by the service process owner and are cleaned up on successful extraction and on next startup.

**Per-file size limiting**
Each individual APK pulled from an emulator is checked against a configurable size ceiling (`max_apk_size_mb`, default 1024 MB). Files exceeding the limit are deleted and extraction is aborted. This prevents a compromised emulator from exhausting disk by returning a synthetic multi-gigabyte file.

**Version agreement enforcement**
All configured emulators must report the same version code for a package before extraction proceeds. A mismatch is treated as an in-progress Play Store rollout and is retried next poll. This prevents producing a mismatched XAPK where the base APK is from a different version than the ABI splits, which could result in a corrupt or exploitable bundle.

**versionName length cap**
Version names reported by `pm dump` are capped at 128 characters. This prevents a compromised emulator from inflating log files, JSON state, or XAPK manifests with arbitrarily large strings.

**Atomic file writes**
Every file written to the output directory (APK, XAPK, checksum, `latest.json`, `packages.json`, `store.json`) is written to a temporary file first and then renamed atomically. Readers never see a partially-written file. A crash mid-write leaves a `.tmp` file that is cleaned up on next startup.

**File permission hardening**
| Path | Mode | Reason |
|---|---|---|
| `output_dir/` | `0750` | Group-readable for web server integration; no world access |
| `output_dir/state/` | `0700` | State directory; owner only |
| `output_dir/state/store.json` | `0600` | Version state; owner only |
| `output_dir/.tmp/` | `0700` | In-transit APKs; owner only |
| `packages.json` | `0600` | Package list; owner only |

**adb_host input validation**
The `adb_host` value is validated against a regex that permits only valid hostname and IP address characters (`[a-zA-Z0-9.\-:\[\]]`). Values containing shell metacharacters, spaces, or other unexpected characters are rejected at config load time.

**Config permission warnings (Unix)**
On startup on Unix systems, the config file's permissions are checked. If it is group-readable, world-readable, or world-writable, a prominent warning is logged. A world-readable config exposes `api_token`; a world-writable config allows injection of arbitrary package names or redirection of the output directory.

**API security posture warnings**
At startup, if the API is enabled and `api_token` is not set, a `SECURITY` warning is logged. If `api_host` is not bound to localhost, an additional warning is logged. These are visible rather than silent — an operator cannot accidentally run an unauthenticated internet-facing API without being told.

**Startup temp cleanup**
On startup, any leftover `extract-*` directories from a prior unclean shutdown are removed from `output_dir/.tmp/`. These could contain partially-pulled APKs that failed size or certificate checks.

### Recommendations for production

1. Set `api_token` in config.yaml. Use `openssl rand -hex 32`. Run `chmod 600 config.yaml`.
2. Configure `expected_certs` for every tracked package. Get fingerprints with `apksigner verify --print-certs` on a known-good APK.
3. Keep `api_host: 127.0.0.1`. If the API must be network-accessible, place a TLS-terminating reverse proxy (nginx, Caddy) in front.
4. Run the service as a dedicated low-privilege user. Do not run as root.
5. Place the emulators on an isolated network segment. ADB traffic is unauthenticated.
6. Monitor the structured log output. Every security event (cert mismatch, size exceeded, version mismatch, API auth failure) is logged with `log/slog` at `WARN` or `ERROR`.

---

## Running in production

### As a systemd service

```ini
[Unit]
Description=APK Distributor
After=network.target

[Service]
Type=simple
User=apk-distributor
WorkingDirectory=/opt/apk-distributor
ExecStart=/opt/apk-distributor/apk-distributor -config /etc/apk-distributor/config.yaml
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now apk-distributor
sudo journalctl -fu apk-distributor
```

### Command-line flags

| Flag | Default | Description |
|---|---|---|
| `-config` | `config.yaml` | Path to configuration file |
| `-debug` | `false` | Enable debug-level logging (includes per-poll cycle detail) |

### Health checking

```bash
curl http://127.0.0.1:8080/health
# {"status":"ok"}
```

The `/health` endpoint does not require authentication and is suitable for load balancer and monitoring probes.

### Log output

The service uses `log/slog` structured text logging. Key events:

| Level | Event |
|---|---|
| `INFO` | Service start/stop, emulator connect, new/updated package detected, extraction complete, config/packages reload |
| `WARN` | Emulator offline (reconnecting), version mismatch between emulators, API security posture issues |
| `ERROR` | Extraction failed, ADB error, cert mismatch, file too large, config reload failed |
| `DEBUG` | Per-poll cycle start/complete, pruned old versions (enable with `-debug`) |

---

## Contributing

Contributions are welcome. The codebase is structured to make extending it straightforward.

### Adding a new sink

Implement `sink.Sink` in a new file and pass it to `watcher.New` in `main.go`. Use `sink.NewFanout` to combine with the existing no-op if needed. See [Sink interface](#sink-interface) above.

### Running tests

```bash
go test ./...
```

All packages have unit tests. The watcher tests use a `fakeDevice` implementation of `adb.Device` so they run without a real emulator. The packages, config, API, and extractor tests run entirely in-process with temporary directories.

Integration testing against a real emulator requires a running AVD with Google Play and at least one installed app. The service can be pointed at it directly with `go run . -config config.yaml`.

### Code organisation principles

- Every externally-consumed type has an interface counterpart (e.g. `adb.Device`) to enable testing without real hardware.
- No package imports `main`. The library packages are all independently usable.
- Security properties are enforced in the layer closest to the data: filenames are validated in the extractor, not the caller; package names are validated in the packages manager, not the API handler.
- All file writes are atomic (temp + rename). All maps returned from exported functions are copies.

### Areas for contribution

- **Additional sink implementations** — S3, GCS, Azure Blob, webhook, MDM platform APIs
- **Metrics** — Prometheus endpoint exposing extraction count, failure rate, last-seen version per package
- **TLS support** — HTTPS for the management API without requiring a reverse proxy
- **Notification support** — Slack, PagerDuty, or email alerts on extraction failure or cert mismatch
- **Windows service wrapper** — `golang.org/x/sys/windows/svc` integration for native Windows service support
- **Docker image** — multi-stage Dockerfile with Android SDK and emulator included

---

## License

MIT License. See [LICENSE](LICENSE) for details.
