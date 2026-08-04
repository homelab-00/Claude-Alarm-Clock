# Linux Release Packaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On a pushed tag `vX.Y.Z`, GitHub Actions publishes an AppImage, a `fyne package` tar.xz and a SHA256SUMS file to a GitHub Release, with the same packaging path exercised on every push to `main`.

**Architecture:** All packaging logic lives in one shell script, `scripts/package-linux.sh`, which both workflows call and which the maintainer can run locally. The workflows contribute only the environment: an `ubuntu:22.04` container that fixes the glibc floor at 2.35, the Fyne build dependencies, and the credentials to publish. Version identity flows from the git tag through the script as two arguments; nothing writes a version back into the repository.

**Tech Stack:** Go 1.26.5 with CGO_ENABLED=1, Fyne v2.8.0, the `fyne` CLI from `fyne.io/tools`, `linuxdeploy` (continuous), GitHub Actions, `gh`.

**Spec:** `docs/superpowers/specs/2026-08-04-linux-release-packaging-design.md`

## Global Constraints

Every task's requirements implicitly include this section.

- **Architecture:** x86_64 only. No arm64 anywhere.
- **glibc ceiling:** `GLIBC_2.35`, guaranteed by building inside `container: ubuntu:22.04`, and what the README promises. The assertion is `floor <= GLIBC_2.35`, **never an equality check**. Measured on 2026-08-04 the produced binary's actual floor is `GLIBC_2.34`, because a binary only references the versioned symbols it actually calls — the container's glibc is an upper bound on what the build *can* require, not what it *does*. An equality assertion against either value is wrong: `== 2.35` fails today, and `== 2.34` would fail spuriously the day any dependency touches a 2.35-only symbol, which is a harmless change that must not block a release.
- **Runner label:** `ubuntu-24.04`, pinned. Never `ubuntu-latest`. Never `ubuntu-22.04` (deprecation begins 2026-09-17).
- **`--app-version` must receive the version WITHOUT a leading `v`.** It evaluates `semver.IsValid("v" + ver)`, so `v1.2.3` becomes `vv1.2.3` and fails with `invalid --app-version parameter, integer and '.' characters only up to x.y.z`.
- **`main.version` must stay an uninitialised package-level `string`.** The linker's `-X` is only effective on a string variable that is uninitialised or initialised to a constant expression, and silently does nothing otherwise. The symbol prefix is the literal `main`, never the module path `claudealarm`.
- **`fyne package` rewrites `FyneApp.toml` in place** — it strips every comment and increments `Build`. Any invocation must be paired with a restore.
- **`fyne package` must run from the repository root with `--executable`.** `--src ./cmd/alarmclock` cannot see the root `FyneApp.toml` and fails with `Missing application icon`; a bare `fyne package` at the root fails with `function main is undeclared in the main package`.
- **Never pass `-l` to `linuxdeploy`.** It force-deploys and bypasses the excludelist. `libGL`, `libEGL`, `libGLX`, `libX11`, `libxcb`, `libdrm`, Mesa and driver libraries must not be bundled.
- **`APPIMAGE_EXTRACT_AND_RUN=1` is an environment variable, not a CLI flag.** `linuxdeploy` spawns `appimagetool` as a child process and only the environment propagates.
- **Fyne apt dependencies:** `libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev`. None are preinstalled on any image.
- **No `sudo` inside the container.** `apt-get` runs as root directly, and the install step must come *before* `actions/checkout`, because `git` and `ca-certificates` are absent from the base image.
- **Never write "install libfuse2" in user-facing docs.** The current AppImage runtime is static-pie linked against libfuse3 + musl.
- **Asset names are lowercase kebab-case:** `claude-alarm-clock-<version>-linux-amd64.tar.xz`, `claude-alarm-clock-<version>-x86_64.AppImage`, `SHA256SUMS`.
- **Commit style:** conventional commits (`feat:`, `fix:`, `docs:`, `ci:`, `chore:`). No attribution trailer.
- **Test command:** `go test -race -cover ./...`. Output must never contain `Error in Fyne call thread`.

## File Structure

| Path | Responsibility | Task |
|---|---|---|
| `.github/workflows/spike-container.yml` | Throwaway. Proves the container assumption, then is deleted. | 1 |
| `internal/buildinfo/buildinfo.go` | Resolve and render the build identity. Pure, no I/O, fully unit-testable. | 2 |
| `internal/buildinfo/buildinfo_test.go` | Tests for the above. | 2 |
| `cmd/alarmclock/main.go` | Add `var version string` and the `-version` flag. Wiring only, per the existing convention. | 2 |
| `FyneApp.toml` | Add `[LinuxAndBSD]` so the generated `.desktop` carries `Categories=`. | 3 |
| `scripts/package-linux.sh` | The entire packaging pipeline. The single place both workflows and the maintainer call. | 4, 5 |
| `.gitignore` | Ignore the packaging working directories. | 4 |
| `.github/workflows/ci.yml` | `test` job + `package-smoke` job. | 6 |
| `.github/workflows/release.yml` | Tag-triggered publish. | 7 |
| `README.md` | Install-first restructure. | 8 |
| `MANUAL-TESTS.md` | What CI cannot prove. | 9 |

The packaging logic deliberately does **not** live in YAML. Duplicating a
twelve-step pipeline across `ci.yml` and `release.yml` would guarantee they
drift, and neither could be run locally.

---

### Task 1: Prove the container assumption

The spec's largest open risk. `actions/checkout` and `actions/setup-go` are
JavaScript actions; inside a `container:` job the runner must inject its own
Node into the container. This is expected to work with `ubuntu:22.04` but was
never verified here, and everything else in the plan sits on top of it. It also
incidentally validates the action major versions, which move over time.

Nothing else may be built until this passes.

**Files:**
- Create: `.github/workflows/spike-container.yml` (deleted in step 7)

**Interfaces:**
- Consumes: nothing
- Produces: a verified `runs-on` / `container` / apt / checkout / setup-go
  preamble that Tasks 6 and 7 copy verbatim, and confirmed action major
  versions.

- [ ] **Step 1: Write the spike workflow**

Create `.github/workflows/spike-container.yml`:

```yaml
name: spike-container

# Manual only. This workflow is a one-off experiment and is deleted once it
# has answered its question.
on:
  workflow_dispatch:

permissions:
  contents: read

jobs:
  probe:
    runs-on: ubuntu-24.04
    container:
      image: ubuntu:22.04
    env:
      DEBIAN_FRONTEND: noninteractive
      CGO_ENABLED: "1"
    steps:
      # First, before checkout: the base image has no git, no curl and no CA
      # certificates, and actions/checkout needs git to do a real clone rather
      # than silently degrading to a tarball download. There is no sudo here.
      - name: Install toolchain and Fyne build dependencies
        run: |
          apt-get update
          apt-get install -y --no-install-recommends \
            build-essential pkg-config git curl ca-certificates xz-utils file binutils \
            libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev

      - uses: actions/checkout@v7

      # The checkout is owned by a different uid than the one the steps run as,
      # which makes git refuse to operate on it and breaks Go's VCS stamping.
      - name: Mark the workspace safe
        run: git config --global --add safe.directory "$GITHUB_WORKSPACE"

      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      - name: Report the environment
        run: |
          echo "--- glibc ---"
          ldd --version | head -1
          echo "--- go ---"
          go version
          echo "--- git ---"
          git --version
          git log --oneline -1
          echo "--- cgo build ---"
          go build -o /tmp/probe ./cmd/alarmclock
          echo "--- glibc floor of the produced binary ---"
          objdump -T /tmp/probe | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1
```

- [ ] **Step 2: Commit and push**

```bash
git add .github/workflows/spike-container.yml
git commit -m "ci: spike to prove container + JS actions viability"
git push -u origin feat/linux-release-packaging
```

- [ ] **Step 3: Run it and watch**

```bash
gh workflow run spike-container.yml --ref feat/linux-release-packaging
sleep 10
gh run watch "$(gh run list --workflow=spike-container.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
```

- [ ] **Step 4: Read the log and check four things**

```bash
gh run view "$(gh run list --workflow=spike-container.yml --limit 1 --json databaseId --jq '.[0].databaseId')" --log
```

Expected:
- `ldd (Ubuntu GLIBC 2.35-0ubuntu3.x) 2.35` — the container sets the floor
- `go version go1.26.5 linux/amd64`
- `git log --oneline -1` prints the real commit, i.e. checkout did a git clone
- The final line is exactly `GLIBC_2.35`

- [ ] **Step 5: If the run FAILED, take the fallback**

Only if a JS action refused to run in the container. Replace the container
approach with a plain job that shells into Docker for the build step alone:

```yaml
  probe:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v7
      - name: Build inside the container
        run: |
          docker run --rm -v "$PWD:/src" -w /src ubuntu:22.04 bash -c '
            set -e
            export DEBIAN_FRONTEND=noninteractive
            apt-get update
            apt-get install -y --no-install-recommends \
              build-essential pkg-config git curl ca-certificates xz-utils file binutils \
              libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev golang-go
            ldd --version | head -1
          '
```

Record which route was taken in the plan file before continuing — Tasks 6 and 7
depend on it.

- [ ] **Step 6: Record the outcome**

Append to this plan file, under Task 1, one line stating which route was proven
and the exact glibc string observed. Later tasks assert against that string.

- [ ] **Step 7: Delete the spike and commit**

```bash
git rm .github/workflows/spike-container.yml
git add docs/superpowers/plans/2026-08-04-linux-release-packaging.md
git commit -m "ci: remove container spike, assumption proven"
git push
```

**Outcome:** The container route (`container: image: ubuntu:22.04` on `runs-on: ubuntu-24.04`, with `actions/checkout@v7` and `actions/setup-go@v7`) was proven — no docker-run fallback was needed. `ldd --version` reported `2.35` and `go build ./cmd/alarmclock` succeeded with `CGO_ENABLED=1`, but the produced binary's actual glibc floor (`objdump -T | grep GLIBC | sort -uV | tail -1`) was **`GLIBC_2.34`**, not the expected `GLIBC_2.35` — later tasks must assert against `GLIBC_2.34`. `workflow_dispatch` alone could not trigger the run since GitHub only registers manually-dispatchable workflows that exist on the default branch; a temporary `push` trigger scoped to `feat/linux-release-packaging` was added to observe the run (see run [30947943280](https://github.com/homelab-00/Claude-Alarm-Clock/actions/runs/30947943280)).

---

### Task 2: The `-version` flag

The release workflow's smoke test. Because `cmd/alarmclock` is wiring-only by
convention (0% coverage, exercised manually), the logic goes in a new
`internal/` package where it can be tested properly, and `main.go` gains only
the flag and the injected variable.

**Files:**
- Create: `internal/buildinfo/buildinfo.go`
- Create: `internal/buildinfo/buildinfo_test.go`
- Modify: `cmd/alarmclock/main.go` (imports; the flag block at lines 34-36)

**Interfaces:**
- Consumes: nothing
- Produces:
  - `buildinfo.Info` — struct with fields `Version string`, `Revision string`, `Modified bool`
  - `buildinfo.DefaultVersion` — `const` string `"dev"`
  - `buildinfo.Resolve(injected string, bi *debug.BuildInfo, ok bool) Info`
  - `buildinfo.String(i Info) string`
  - `main.version` — uninitialised `string`, the `-X` target

- [ ] **Step 1: Write the failing tests**

Create `internal/buildinfo/buildinfo_test.go`:

```go
package buildinfo

import (
	"runtime/debug"
	"testing"
)

// stamp assembles a *debug.BuildInfo the way the Go toolchain would, so the
// tests never need a real build to exercise the fallback paths.
func stamp(mainVersion, revision string, modified bool) *debug.BuildInfo {
	bi := &debug.BuildInfo{}
	bi.Main.Version = mainVersion
	if revision != "" {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: "vcs.revision", Value: revision})
	}
	bi.Settings = append(bi.Settings, debug.BuildSetting{
		Key:   "vcs.modified",
		Value: map[bool]string{true: "true", false: "false"}[modified],
	})
	return bi
}

// The linker-injected value always wins. This is the whole point of using
// ldflags: it is the only source that survives a build from a source tarball
// with no .git, and the only one immune to the module path lacking a /vN
// suffix once a v2.0.0 is ever tagged.
func TestResolvePrefersInjectedVersion(t *testing.T) {
	got := Resolve("v1.4.0", stamp("v9.9.9", "f0d1965abc", false), true)

	if got.Version != "v1.4.0" {
		t.Fatalf("Version = %q, want the injected %q", got.Version, "v1.4.0")
	}
}

// A plain `go build` on a tagged checkout injects nothing, but since Go 1.24
// the toolchain stamps the tag itself. Falling back to it means a locally
// built binary still tells the truth.
func TestResolveFallsBackToBuildInfo(t *testing.T) {
	got := Resolve("", stamp("v1.4.0", "f0d1965abc", false), true)

	if got.Version != "v1.4.0" {
		t.Fatalf("Version = %q, want the stamped %q", got.Version, "v1.4.0")
	}
}

// "(devel)" is the toolchain's own placeholder for "VCS stamping was
// unavailable". It is strictly less informative than our default, so it must
// never be shown to a user.
func TestResolveRejectsDevelSentinel(t *testing.T) {
	got := Resolve("", stamp("(devel)", "f0d1965abc", false), true)

	if got.Version != DefaultVersion {
		t.Fatalf("Version = %q, want %q", got.Version, DefaultVersion)
	}
}

// A source tarball with no .git: ReadBuildInfo reports nothing usable.
func TestResolveWithoutBuildInfo(t *testing.T) {
	got := Resolve("", nil, false)

	if got.Version != DefaultVersion {
		t.Fatalf("Version = %q, want %q", got.Version, DefaultVersion)
	}
	if got.Revision != "" {
		t.Fatalf("Revision = %q, want empty", got.Revision)
	}
}

// Even with no build info at all, an injected version must survive.
func TestResolveInjectedSurvivesMissingBuildInfo(t *testing.T) {
	got := Resolve("v1.4.0", nil, false)

	if got.Version != "v1.4.0" {
		t.Fatalf("Version = %q, want %q", got.Version, "v1.4.0")
	}
}

func TestResolveReadsRevisionAndModified(t *testing.T) {
	got := Resolve("v1.4.0", stamp("v1.4.0", "f0d1965abcdef", true), true)

	if got.Revision != "f0d1965abcdef" {
		t.Fatalf("Revision = %q, want the full revision", got.Revision)
	}
	if !got.Modified {
		t.Fatal("Modified = false, want true")
	}
}

// Seven hex digits is what git abbreviates to by default and is enough to
// paste into a bug report.
func TestStringTruncatesRevision(t *testing.T) {
	got := String(Info{Version: "v1.4.0", Revision: "f0d1965abcdef0123456"})

	if got != "v1.4.0 (f0d1965)" {
		t.Fatalf("String = %q, want %q", got, "v1.4.0 (f0d1965)")
	}
}

// A revision shorter than the truncation length must not be padded or sliced.
func TestStringKeepsShortRevision(t *testing.T) {
	got := String(Info{Version: "v1.4.0", Revision: "f0d19"})

	if got != "v1.4.0 (f0d19)" {
		t.Fatalf("String = %q, want %q", got, "v1.4.0 (f0d19)")
	}
}

func TestStringOmitsEmptyRevision(t *testing.T) {
	got := String(Info{Version: "dev"})

	if got != "dev" {
		t.Fatalf("String = %q, want %q", got, "dev")
	}
}

// A dirty tree must be visible: a bug report from a modified build is not a
// bug report about the released version.
func TestStringMarksModified(t *testing.T) {
	got := String(Info{Version: "v1.4.0", Revision: "f0d1965abcdef", Modified: true})

	if got != "v1.4.0 (f0d1965) (modified)" {
		t.Fatalf("String = %q, want %q", got, "v1.4.0 (f0d1965) (modified)")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/buildinfo/ -v`
Expected: FAIL — `no required module provides package claudealarm/internal/buildinfo` (the directory does not exist yet).

- [ ] **Step 3: Write the implementation**

Create `internal/buildinfo/buildinfo.go`:

```go
// Package buildinfo resolves and renders the identity of the running binary.
//
// The version is injected at link time (-X main.version=...) rather than read
// from debug.ReadBuildInfo alone. ReadBuildInfo does report the tag on a
// tagged checkout since Go 1.24 -- the widely repeated claim that it reports
// "(devel)" is outdated -- but it is silently unavailable in three situations
// this project actually hits: a build with -buildvcs=false, a build from a
// source tarball with no .git, and, once a v2.0.0 is ever tagged, a module
// path with no /vN suffix, which makes it report a v0.0.0-<pseudo> version
// instead of the tag.
//
// So the linker is the primary source and ReadBuildInfo is the fallback that
// keeps a plain `go build` honest.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// DefaultVersion is reported when neither the linker nor the toolchain could
// supply anything better.
const DefaultVersion = "dev"

// shortRevLen is how much of the git revision to show. Seven hex digits is
// git's own default abbreviation.
const shortRevLen = 7

// develSentinel is what the toolchain writes into Main.Version when VCS
// stamping was unavailable. It is less informative than DefaultVersion.
const develSentinel = "(devel)"

// Info is the resolved build identity.
type Info struct {
	Version  string
	Revision string
	Modified bool
}

// Resolve merges the link-time injected version with whatever the toolchain
// stamped into the binary. injected is main.version; bi and ok are the two
// results of debug.ReadBuildInfo.
func Resolve(injected string, bi *debug.BuildInfo, ok bool) Info {
	info := Info{Version: injected}

	if ok && bi != nil {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != develSentinel {
			info.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Revision = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}

	if info.Version == "" {
		info.Version = DefaultVersion
	}
	return info
}

// String renders the one-line human-readable form.
func String(i Info) string {
	var b strings.Builder
	b.WriteString(i.Version)

	if rev := i.Revision; rev != "" {
		if len(rev) > shortRevLen {
			rev = rev[:shortRevLen]
		}
		b.WriteString(" (")
		b.WriteString(rev)
		b.WriteString(")")
	}
	if i.Modified {
		b.WriteString(" (modified)")
	}
	return b.String()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/buildinfo/ -v -cover`
Expected: PASS, all ten tests, coverage 100.0%.

- [ ] **Step 5: Wire it into main.go**

In `cmd/alarmclock/main.go`, add to the import block:

```go
	"runtime/debug"

	"claudealarm/internal/buildinfo"
```

Add above `func main()`:

```go
// version is injected at link time by the release workflow:
//
//	go build -ldflags "-X main.version=$GITHUB_REF_NAME"
//
// It must remain an UNINITIALISED package-level string. The linker's -X is
// only effective on a string variable that is uninitialised or initialised to
// a constant expression; if this ever becomes a const, a struct field, or is
// initialised by a function call, -X silently does nothing and every release
// reports "dev".
//
// The symbol the linker looks for is literally "main.version". The module path
// (claudealarm) is not part of it.
var version string
```

Replace the flag block at the top of `main()`:

```go
	showVersion := flag.Bool("version", false, "print version information and exit")
	hidden := flag.Bool("hidden", false, "start minimised to the tray")
	flag.Parse()

	// Before any Fyne initialisation. -version has to work with no display,
	// because the release workflow runs it as the smoke test that proves the
	// linker actually injected the tag.
	if *showVersion {
		bi, ok := debug.ReadBuildInfo()
		fmt.Println("Claude Alarm Clock " + buildinfo.String(buildinfo.Resolve(version, bi, ok)))
		return
	}
```

- [ ] **Step 6: Verify both paths by hand**

```bash
go build -o /tmp/av ./cmd/alarmclock && /tmp/av -version
```
Expected: `Claude Alarm Clock <something> (<7 hex>)` — the tag if HEAD is tagged, otherwise a pseudo-version; never empty.

```bash
go build -ldflags "-X main.version=v9.9.9" -o /tmp/av ./cmd/alarmclock && /tmp/av -version
```
Expected: `Claude Alarm Clock v9.9.9 (<7 hex>)` — proves `-X` reaches the variable.

```bash
go build -buildvcs=false -o /tmp/av ./cmd/alarmclock && /tmp/av -version
```
Expected: `Claude Alarm Clock dev` — proves the fallback.

- [ ] **Step 7: Run the whole suite**

Run: `go test -race -cover ./...`
Expected: PASS. No `Error in Fyne call thread` anywhere in the output.

- [ ] **Step 8: Commit**

```bash
git add internal/buildinfo/ cmd/alarmclock/main.go
git commit -m "feat: report the build version via -version

The flag is handled before any Fyne initialisation so it works headlessly,
which is what lets the release workflow use it to prove the linker injected
the tag. The logic lives in internal/buildinfo because cmd/alarmclock is
wiring only."
```

---

### Task 3: `FyneApp.toml` gains `[LinuxAndBSD]`

Without `Categories=` in the generated `.desktop`, `appimagetool` aborts with
`.desktop file is missing a Categories= key` and no AppImage can be built at
all. This task is a hard prerequisite for Task 5.

**Files:**
- Modify: `FyneApp.toml`

**Interfaces:**
- Consumes: nothing
- Produces: a `.desktop` file at
  `usr/local/share/applications/gr.polaris.claudealarm.desktop` inside the
  fyne archive, carrying a `Categories=` line. Task 5 depends on this path and
  on that key.

- [ ] **Step 1: Add the section**

Insert into `FyneApp.toml`, between `[Details]` and the `[Migrations]` comment
block:

```toml
# appimagetool hard-aborts with ".desktop file is missing a Categories= key"
# if this section is absent, because Fyne's .desktop template only emits
# Categories when [LinuxAndBSD] provides it. The AppImage build depends on it.
#
# WARNING: `fyne package` REWRITES this file in place -- it re-serialises the
# whole thing, stripping every comment including this one, and increments
# Build. scripts/package-linux.sh restores it afterwards. If you run
# `fyne package` by hand, run `git checkout -- FyneApp.toml` after.
[LinuxAndBSD]
Categories = ["Utility"]
GenericName = "Alarm Clock"
Comment = "Runs Claude Code at a chosen time"
```

- [ ] **Step 2: Prove it produces the key**

```bash
go build -o alarmclock ./cmd/alarmclock
go run fyne.io/tools/cmd/fyne@latest package --target linux --executable ./alarmclock
mkdir -p /tmp/fynecheck && tar -xJf "Claude Alarm Clock.tar.xz" -C /tmp/fynecheck
cat /tmp/fynecheck/alarmclock/usr/local/share/applications/gr.polaris.claudealarm.desktop
```

Expected: the file contains `Categories=Utility;`, `Exec=alarmclock` and
`Icon=gr.polaris.claudealarm`.

- [ ] **Step 3: Observe the damage, then undo it**

```bash
git diff FyneApp.toml
```

Expected: the comments are gone, keys are reordered, `Build` went `1` → `2`.
This is the behaviour the guard in Task 4 exists to neutralise.

```bash
git checkout -- FyneApp.toml
rm -rf "Claude Alarm Clock.tar.xz" /tmp/fynecheck alarmclock
git diff --stat
```

Expected: only the intended `[LinuxAndBSD]` addition remains staged/unstaged.

- [ ] **Step 4: Commit**

```bash
git add FyneApp.toml
git commit -m "feat: declare Linux desktop categories

appimagetool refuses a .desktop with no Categories= key, and Fyne only emits
one when [LinuxAndBSD] supplies it."
```

---

### Task 4: `scripts/package-linux.sh` — binary and tar.xz

Half the pipeline. Ends with a versioned, URL-safe tarball in `dist/` and a
clean working tree.

**Files:**
- Create: `scripts/package-linux.sh`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: `buildinfo` (Task 2) via `./alarmclock -version`; `[LinuxAndBSD]` (Task 3)
- Produces:
  - `dist/claude-alarm-clock-<version>-linux-amd64.tar.xz`
  - the built binary at `./alarmclock`, reused by Task 5
  - CLI contract: `scripts/package-linux.sh <version-without-v> [build-number]`
  - env contract: `TAG` (default `v<version>`), `MAX_GLIBC` (optional ceiling; assert floor ≤ this, never equality)

- [ ] **Step 1: Ignore the working directories**

Append to `.gitignore` under the existing `# Fyne packaging output` heading:

```
/dist/
/stage/
/AppDir/
/linuxdeploy-x86_64.AppImage
```

Note `*.tar.xz` is already ignored by the existing rules.

- [ ] **Step 2: Write the script**

Create `scripts/package-linux.sh`:

```bash
#!/usr/bin/env bash
#
# Builds the Linux release artifacts into dist/.
#
# Usage:
#   scripts/package-linux.sh <version> [build-number]
#
#   version       release version WITHOUT a leading "v". fyne's --app-version
#                 evaluates semver.IsValid("v" + arg), so passing "v1.2.3"
#                 becomes "vv1.2.3" and is rejected.
#   build-number  monotonic counter for the .desktop metadata. Default 1.
#
# Environment:
#   TAG           version string stamped into the binary. Defaults to v<version>.
#                 The binary shows the tag verbatim (with the v) because that is
#                 what a user pastes into a bug report; only fyne and the asset
#                 filenames use the stripped form.
#   MAX_GLIBC     e.g. "GLIBC_2.35". When set, assert the binary's highest
#                 required glibc symbol is AT MOST this. Set in CI, unset
#                 locally (an Arch build legitimately requires a newer glibc).
#
#                 A ceiling, never an equality check. The floor is an emergent
#                 property of which versioned symbols the code happens to call,
#                 so it sits BELOW the container's glibc (2.34 against a 2.35
#                 container, measured 2026-08-04) and rises harmlessly whenever
#                 a dependency starts touching a newer symbol. Only exceeding
#                 the promise matters.
#
set -euo pipefail

VERSION="${1:?usage: package-linux.sh <version> [build-number]}"
BUILD="${2:-1}"
TAG="${TAG:-v${VERSION}}"

APP_ID="gr.polaris.claudealarm"
BINARY="alarmclock"          # leaks into the .desktop Exec= and the archive's
                             # top-level directory name -- chosen, not incidental
FYNE_ARCHIVE="Claude Alarm Clock.tar.xz"   # named from Details.Name, with spaces
TARBALL="claude-alarm-clock-${VERSION}-linux-amd64.tar.xz"

# `fyne package` rewrites FyneApp.toml in place: it re-serialises the file,
# stripping every comment, reordering keys, and incrementing Build. Restore it
# however this script exits, so a failure halfway through does not leave the
# file mutated.
#
# Restore from a byte copy, NOT from `git checkout -- FyneApp.toml`. Those are
# not the same thing: git restores the INDEX state, so if the file carries
# uncommitted edits when the script runs, a git-based restore silently discards
# the user's work along with fyne's damage. This was hit for real during Task 3.
# A copy restores exactly what was there, and needs no .git at all -- which also
# makes the script work from an unpacked source tarball.
TOML_BACKUP="$(mktemp)"
cp FyneApp.toml "${TOML_BACKUP}"
restore_toml() { cp "${TOML_BACKUP}" FyneApp.toml; rm -f "${TOML_BACKUP}"; }
trap restore_toml EXIT

rm -rf dist stage AppDir
mkdir -p dist

echo "==> Building ${BINARY} (${TAG})"
CGO_ENABLED=1 go build \
  -trimpath \
  -ldflags "-s -w -X main.version=${TAG}" \
  -o "${BINARY}" ./cmd/alarmclock

echo "==> Asserting the linker reached main.version"
ACTUAL="$("./${BINARY}" -version)"
echo "    ${ACTUAL}"
case "${ACTUAL}" in
  *"${TAG}"*) ;;
  *) echo "ERROR: -version reported '${ACTUAL}', expected it to contain '${TAG}'." >&2
     echo "       -X silently does nothing unless main.version is an uninitialised string." >&2
     exit 1 ;;
esac

if [ -n "${MAX_GLIBC:-}" ]; then
  echo "==> Asserting the glibc floor is at most ${MAX_GLIBC}"
  FLOOR="$(objdump -T "${BINARY}" | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1)"
  echo "    floor=${FLOOR} ceiling=${MAX_GLIBC}"
  # sort -V puts the higher version last. If that is not the ceiling, the floor
  # exceeded it. Equal values sort to the ceiling, so equality passes.
  HIGHEST="$(printf '%s\n%s\n' "${FLOOR}" "${MAX_GLIBC}" | sort -V | tail -1)"
  if [ "${HIGHEST}" != "${MAX_GLIBC}" ]; then
    echo "ERROR: glibc floor ${FLOOR} exceeds the promised ${MAX_GLIBC}." >&2
    echo "       Users on the distributions the README promises would get a" >&2
    echo "       'version not found' loader error and the app would not start." >&2
    exit 1
  fi
fi

echo "==> Packaging tar.xz"
# Must run from the repo root with --executable: --src ./cmd/alarmclock cannot
# see the root FyneApp.toml, and a bare `fyne package` here fails because main
# lives under ./cmd/alarmclock.
fyne package \
  --target linux \
  --executable "./${BINARY}" \
  --app-version "${VERSION}" \
  --app-build "${BUILD}"

# fyne names the archive from Details.Name verbatim: spaces, no version.
# Uploaded as-is it would give users a %20-encoded download URL.
mv "${FYNE_ARCHIVE}" "dist/${TARBALL}"

echo "==> Verifying the generated .desktop"
mkdir -p stage
tar -xJf "dist/${TARBALL}" -C stage
DESKTOP="stage/${BINARY}/usr/local/share/applications/${APP_ID}.desktop"
if ! grep -q '^Categories=' "${DESKTOP}"; then
  echo "ERROR: ${DESKTOP} has no Categories= key." >&2
  echo "       appimagetool will refuse it. Add [LinuxAndBSD] Categories to FyneApp.toml." >&2
  exit 1
fi

echo "==> dist/"
ls -la dist/
```

- [ ] **Step 3: Make it executable**

```bash
chmod +x scripts/package-linux.sh
```

- [ ] **Step 4: Run it and verify**

```bash
go install fyne.io/tools/cmd/fyne@latest
export PATH="$(go env GOPATH)/bin:$PATH"
scripts/package-linux.sh 0.0.0
```

Expected:
- `-version` line contains `v0.0.0`
- no glibc assertion (MAX_GLIBC unset locally)
- `dist/claude-alarm-clock-0.0.0-linux-amd64.tar.xz` exists

- [ ] **Step 5: Verify the trap restored the TOML**

```bash
git status --porcelain FyneApp.toml
```
Expected: empty. The trap ran even though the script succeeded.

Now the case a git-based restore would have got wrong — uncommitted local edits
must survive the packaging run:

```bash
printf '\n# scratch edit that must survive packaging\n' >> FyneApp.toml
scripts/package-linux.sh 0.0.0
grep -c "scratch edit that must survive packaging" FyneApp.toml
```
Expected: `1`. The edit is still there. A `git checkout --` restore would have
destroyed it. Clean up afterwards:

```bash
git checkout -- FyneApp.toml && git status --porcelain FyneApp.toml
```

Now prove it also fires on failure. `GLIBC_2.0` is an absurdly low ceiling that
any real binary exceeds, so this forces the assertion to trip:

```bash
MAX_GLIBC=GLIBC_2.0 scripts/package-linux.sh 0.0.0 || true
git status --porcelain FyneApp.toml
```
Expected: the script exits non-zero with `exceeds the promised GLIBC_2.0`, and
the TOML is still clean.

Then prove the ceiling passes when it should, including the equality case:

```bash
MAX_GLIBC=GLIBC_2.99 scripts/package-linux.sh 0.0.0 && echo "ceiling OK"
```
Expected: success. A ceiling above the floor must not trip.

- [ ] **Step 6: Verify the archive contents**

```bash
tar -tJf dist/claude-alarm-clock-0.0.0-linux-amd64.tar.xz
```
Expected paths: `alarmclock/Makefile`,
`alarmclock/usr/local/bin/alarmclock`,
`alarmclock/usr/local/share/applications/gr.polaris.claudealarm.desktop`,
`alarmclock/usr/local/share/pixmaps/gr.polaris.claudealarm.png`.

- [ ] **Step 7: Commit**

```bash
git add scripts/package-linux.sh .gitignore
git commit -m "feat: add the Linux packaging script

One script, called by both workflows and runnable locally, so the pipeline
cannot drift between CI and release and can be debugged without pushing.
Restores FyneApp.toml via a trap because fyne package rewrites it in place."
```

---

### Task 5: `scripts/package-linux.sh` — AppImage and checksums

**Files:**
- Modify: `scripts/package-linux.sh` (append before the final `ls -la dist/`)

**Interfaces:**
- Consumes: `./alarmclock` and `stage/` from Task 4
- Produces:
  - `dist/claude-alarm-clock-<version>-x86_64.AppImage`
  - `dist/SHA256SUMS` containing basenames only

- [ ] **Step 1: Append the AppImage and checksum sections**

Insert into `scripts/package-linux.sh`, immediately before the closing
`echo "==> dist/"` block:

```bash
echo "==> Fetching linuxdeploy"
# One download suffices: linuxdeploy's continuous AppImage already bundles
# linuxdeploy-plugin-appimage, appimagetool, mksquashfs and
# desktop-file-validate. No apt install of squashfs-tools or desktop-file-utils.
if [ ! -x linuxdeploy-x86_64.AppImage ]; then
  curl -fsSL -o linuxdeploy-x86_64.AppImage \
    https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage
  chmod +x linuxdeploy-x86_64.AppImage
fi

echo "==> Building AppImage"
# APPIMAGE_EXTRACT_AND_RUN must be an ENVIRONMENT VARIABLE, not the
# --appimage-extract-and-run flag: linuxdeploy spawns the bundled appimagetool
# as a child process, and only the environment propagates to that nested call.
#
# The fyne tree is NOT a valid AppDir -- it uses a usr/local/ prefix, while
# linuxdeploy expects usr/bin and usr/share. Hand it the three pieces instead,
# so both artifacts carry byte-identical metadata.
#
# Never pass -l: it force-deploys and bypasses the excludelist. Let linuxdeploy
# skip the graphics stack; it logs "Skipping deployment of blacklisted library".
P="stage/${BINARY}/usr/local"
APPIMAGE="claude-alarm-clock-${VERSION}-x86_64.AppImage"

APPIMAGE_EXTRACT_AND_RUN=1 \
ARCH=x86_64 \
LINUXDEPLOY_OUTPUT_VERSION="${VERSION}" \
LDAI_OUTPUT="dist/${APPIMAGE}" \
./linuxdeploy-x86_64.AppImage \
  --appdir AppDir \
  --executable   "${P}/bin/${BINARY}" \
  --desktop-file "${P}/share/applications/${APP_ID}.desktop" \
  --icon-file    "${P}/share/pixmaps/${APP_ID}.png" \
  --output appimage

echo "==> Asserting the graphics stack was not bundled"
# The entire point of the exercise. A bundled libGL built against this
# machine's Mesa breaks the app on every NVIDIA machine, and a bundled glibc
# breaks it everywhere. Cheap to check, catches an excludelist regression.
if [ -d AppDir/usr/lib ]; then
  ls -1 AppDir/usr/lib/
  if ls -1 AppDir/usr/lib/ | grep -Ei \
    '^(libGL\.|libEGL\.|libGLX\.|libGLdispatch\.|libOpenGL\.|libX11\.|libxcb\.|libdrm\.|libglapi\.|libgbm\.|libwayland-client\.|libc\.so|ld-linux)'; then
    echo "ERROR: driver or glibc libraries leaked into the AppDir." >&2
    echo "       The AppImage would break on any machine with a different GPU stack." >&2
    exit 1
  fi
fi
echo "    OK: no GL/X11/driver/glibc libraries bundled"

echo "==> Smoke-testing the AppImage"
APPIMAGE_EXTRACT_AND_RUN=1 "dist/${APPIMAGE}" -version

echo "==> Generating SHA256SUMS"
# Generated from inside dist/ with bare globs so the file contains BASENAMES.
# `sha256sum "$PWD"/dist/*` would bake in runner paths, still exit 0 on the
# builder, and fail for every real downloader.
( cd dist && sha256sum claude-alarm-clock-* > SHA256SUMS && sha256sum -c SHA256SUMS )
```

- [ ] **Step 2: Run the full script**

```bash
scripts/package-linux.sh 0.0.0
```

Expected, in order:
- `Skipping deployment of blacklisted library` lines for libGL/libX11 in the linuxdeploy output
- `OK: no GL/X11/driver/glibc libraries bundled`
- the smoke test prints `Claude Alarm Clock v0.0.0 (<7 hex>)`
- `SHA256SUMS: OK` twice (one per artifact)

- [ ] **Step 3: Verify the AppImage independently**

```bash
ls -la dist/
file dist/claude-alarm-clock-0.0.0-x86_64.AppImage
./dist/claude-alarm-clock-0.0.0-x86_64.AppImage --appimage-extract-and-run -version
```
Expected: an ELF executable; the version line matches the tarball's binary.

- [ ] **Step 4: Verify the checksum file is portable**

```bash
mkdir -p /tmp/dlcheck && cp dist/* /tmp/dlcheck/ && (cd /tmp/dlcheck && sha256sum -c SHA256SUMS)
```
Expected: two `OK` lines. This proves the file contains no absolute paths.

```bash
grep -c "$PWD" dist/SHA256SUMS || echo "OK: no absolute paths"
rm -rf /tmp/dlcheck
```

- [ ] **Step 5: Confirm the tree is still clean**

```bash
git status --porcelain
```
Expected: only the modified `scripts/package-linux.sh`. No `FyneApp.toml`, no
stray artifacts (they are gitignored).

- [ ] **Step 6: Commit**

```bash
git add scripts/package-linux.sh
git commit -m "feat: build the AppImage and checksums

Asserts the graphics stack and glibc were not bundled, which is the whole
reason for using linuxdeploy's excludelist rather than deploying libraries by
hand. Checksums are generated from inside dist/ so sha256sum -c works for a
downloader rather than only on the builder."
```

---

### Task 6: `.github/workflows/ci.yml`

Two jobs. `test` is the gate that already exists conceptually in the README.
`package-smoke` exists because a tag-only workflow is exercised so rarely that
it is normally discovered broken at the worst possible moment; running the real
packaging path on every push to `main` makes a tag push the boring case.

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `scripts/package-linux.sh` (Tasks 4, 5); the container preamble proven in Task 1
- Produces: a reusable job shape that Task 7 mirrors

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v7

      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      # Fyne's GLFW/OpenGL backend needs cgo and these headers. None are
      # preinstalled on any runner image.
      - name: Install Fyne build dependencies
        run: |
          sudo apt-get update
          sudo apt-get install -y --no-install-recommends \
            libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev

      # The suite is headless: no display, no network, no API spend. It uses
      # Fyne's software test driver and shell-script stand-ins for the claude
      # CLI, so it needs nothing beyond the build dependencies above.
      - name: Test
        run: go test -race -cover ./... 2>&1 | tee test.log

      # This string means a widget was touched from a goroutine other than the
      # Fyne one without going through fyne.Do. Fyne v2.8 repairs and logs it;
      # v2.9 turns it into a crash. It must never appear.
      - name: Assert no Fyne threading violations
        run: |
          if grep -q "Error in Fyne call thread" test.log; then
            echo "::error::a widget was touched outside the Fyne goroutine"
            exit 1
          fi
          echo "OK: no threading violations"

  package-smoke:
    # Runs the REAL packaging path on every push, so the release workflow is
    # never the first thing to discover a break. Publishes nothing.
    runs-on: ubuntu-24.04
    container:
      image: ubuntu:22.04
    env:
      CGO_ENABLED: "1"
      DEBIAN_FRONTEND: noninteractive
      MAX_GLIBC: GLIBC_2.35
    steps:
      # Before checkout: the base image has no git, curl or CA certificates,
      # and there is no sudo.
      - name: Install toolchain and Fyne build dependencies
        run: |
          apt-get update
          apt-get install -y --no-install-recommends \
            build-essential pkg-config git curl ca-certificates xz-utils file binutils \
            libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev

      - uses: actions/checkout@v7

      - name: Mark the workspace safe
        run: git config --global --add safe.directory "$GITHUB_WORKSPACE"

      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      - name: Install the fyne CLI
        run: |
          go install fyne.io/tools/cmd/fyne@latest
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"

      # 0.0.0 rather than a pre-release string: fyne's --app-version accepts
      # only integers and dots up to x.y.z.
      - name: Package
        run: scripts/package-linux.sh 0.0.0 "$GITHUB_RUN_NUMBER"

      - uses: actions/upload-artifact@v4
        with:
          name: smoke-artifacts
          path: dist/
          retention-days: 7
```

- [ ] **Step 2: Commit and push**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: test on every push, and smoke-test the packaging path

package-smoke runs the same script the release workflow will, so a tag push is
never the first execution of the packaging pipeline."
git push
```

- [ ] **Step 3: Open a draft PR to trigger the run**

Pushing this branch fires nothing: the triggers are `push` on `main` and
`pull_request` targeting `main`, and a feature-branch push matches neither.
Task 1 hit the same class of problem from the other direction — GitHub also
refuses `workflow_dispatch` for a workflow that does not yet exist on the
default branch, so adding a dispatch trigger would not help either.

A pull request is the mechanism that runs `ci.yml` before merge, which is
exactly what it is for. Open it as a draft; Task 10 marks it ready.

```bash
gh pr create --draft --base main --head feat/linux-release-packaging \
  --title "Linux release packaging" \
  --body "Adds AppImage + tar.xz release artifacts. Draft until the packaging path is proven green."
```

- [ ] **Step 4: Watch the run**

```bash
gh run watch "$(gh run list --workflow=ci.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
```

Expected: both jobs green. In `package-smoke`, the log shows
`floor=GLIBC_2.34 ceiling=GLIBC_2.35`,
`OK: no GL/X11/driver/glibc libraries bundled`, and `SHA256SUMS: OK`.

- [ ] **Step 4: Download the smoke artifacts and run them locally**

```bash
gh run download "$(gh run list --workflow=ci.yml --limit 1 --json databaseId --jq '.[0].databaseId')" -n smoke-artifacts -D /tmp/smoke
chmod +x /tmp/smoke/*.AppImage
/tmp/smoke/*.AppImage -version
```
Expected: it runs on Arch. This is the first real proof that a
glibc-2.35-floored AppImage works on a modern system.

- [ ] **Step 5: If the run failed, fix and repeat before continuing**

Do not proceed to Task 7 with a red `package-smoke`. The release workflow is
the same steps with publishing bolted on; debugging it via tags is far slower.

---

### Task 7: `.github/workflows/release.yml`

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: everything above
- Produces: a published GitHub Release with three assets

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/release.yml`:

```yaml
name: release

# Tags only. Defining just `tags:` is what stops this running on branch pushes.
# The pattern deliberately excludes pre-release suffixes: fyne's --app-version
# accepts only integers and dots.
on:
  push:
    tags: ['v[0-9]+.[0-9]+.[0-9]+']

permissions:
  contents: read

jobs:
  release:
    runs-on: ubuntu-24.04
    container:
      image: ubuntu:22.04
    permissions:
      # Minimal and sufficient: creates the release AND uploads the assets.
      # Every unlisted scope is implicitly none.
      contents: write
    env:
      CGO_ENABLED: "1"
      DEBIAN_FRONTEND: noninteractive
      MAX_GLIBC: GLIBC_2.35
    steps:
      - name: Install toolchain and Fyne build dependencies
        run: |
          apt-get update
          apt-get install -y --no-install-recommends \
            build-essential pkg-config git curl ca-certificates xz-utils file binutils \
            libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev

      - uses: actions/checkout@v7

      - name: Mark the workspace safe
        run: git config --global --add safe.directory "$GITHUB_WORKSPACE"

      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      - name: Install the fyne CLI
        run: |
          go install fyne.io/tools/cmd/fyne@latest
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"

      # The v strip is mandatory, not cosmetic: --app-version evaluates
      # semver.IsValid("v" + arg), so "v1.2.3" becomes "vv1.2.3" and fails.
      # The binary still gets the tag verbatim, via TAG.
      - name: Derive the version
        id: v
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"

      - name: Package
        env:
          TAG: ${{ github.ref_name }}
        run: scripts/package-linux.sh "${{ steps.v.outputs.version }}" "$GITHUB_RUN_NUMBER"

      # gh is preinstalled on hosted runners but not in the container image.
      - name: Install gh
        run: |
          curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
            -o /usr/share/keyrings/githubcli-archive-keyring.gpg
          echo "deb [arch=amd64 signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
            > /etc/apt/sources.list.d/github-cli.list
          apt-get update && apt-get install -y gh

      # First-party, so there is no third-party action SHA to pin.
      # --verify-tag refuses to publish if the tag does not exist on the remote.
      - name: Publish the release
        env:
          GH_TOKEN: ${{ github.token }}
          VERSION: ${{ steps.v.outputs.version }}
        run: |
          gh release create "$GITHUB_REF_NAME" \
            --title "$GITHUB_REF_NAME" \
            --generate-notes \
            --verify-tag \
            "dist/claude-alarm-clock-${VERSION}-linux-amd64.tar.xz" \
            "dist/claude-alarm-clock-${VERSION}-x86_64.AppImage" \
            dist/SHA256SUMS
```

- [ ] **Step 2: Commit and push**

```bash
git add .github/workflows/release.yml
git commit -m "ci: publish a GitHub Release on a version tag"
git push
```

- [ ] **Step 3: Confirm ci.yml is still green**

```bash
gh run watch "$(gh run list --workflow=ci.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
```
Expected: green. `release.yml` must NOT have run — it is tag-only.

---

### Task 8: README restructure

The current README opens with *Build*, which is backwards for a public repo,
and buries the `claude` prerequisite inside it.

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: the asset names produced by Tasks 4, 5 and 7
- Produces: nothing consumed by later tasks

- [ ] **Step 1: Insert Requirements and Install above Build**

Immediately after the opening paragraph, before `## Build`:

````markdown
## Requirements

**The `claude` CLI must be on your `PATH`.** This app is a scheduler around
Claude Code, not a replacement for it — at fire time it execs `claude` and
shows you the answer. Without it the app starts and reports the problem at
startup rather than at 07:10 when nobody is watching.

Built for **glibc 2.35 and newer**: Ubuntu 22.04 and newer, Debian 12 and
newer, current Fedora, Arch and every rolling distribution. Older systems —
RHEL/Rocky/Alma 9, Ubuntu 20.04, Debian 11 — are not supported.

(Do not claim the app "will not start" on those. As measured on 2026-08-04 the
binary's actual symbol floor is `GLIBC_2.34`, below the container's 2.35, so
some of them may work by accident. Promising 2.35 is what the build
environment guarantees; promising less would be a claim CI does not enforce,
and the floor can rise at any time.)

x86_64 only.

## Install

### AppImage

```bash
chmod +x claude-alarm-clock-1.0.0-x86_64.AppImage
./claude-alarm-clock-1.0.0-x86_64.AppImage
```

`libfuse2` is **not** required. The bundled AppImage runtime is statically
linked against libfuse3, so nothing needs installing alongside it — only the
kernel FUSE module and the standard setuid `fusermount3` helper, both present
by default on mainstream desktops. Where FUSE is genuinely unavailable
(containers, hardened kernels), run it without mounting:

```bash
./claude-alarm-clock-1.0.0-x86_64.AppImage --appimage-extract-and-run
```

The AppImage deliberately does **not** bundle glibc or the graphics stack
(libGL, libEGL, libX11, libxcb, libdrm, Mesa). Those always come from your
system, so it works on Mesa and NVIDIA alike.

### tar.xz

Installs a desktop entry and icon, so the app appears in your application menu
and can be autostarted with `-hidden`.

```bash
tar -xJf claude-alarm-clock-1.0.0-linux-amd64.tar.xz
cd alarmclock
sudo make install      # /usr/local/{bin,share/applications,share/pixmaps}
```

Or without root:

```bash
make user-install      # ~/.local/{bin,share/applications,share/icons}
```

`make uninstall` and `make user-uninstall` reverse either. The Makefile honours
`PREFIX=` and `DESTDIR=` for packagers.

### Verifying a download

```bash
sha256sum -c SHA256SUMS
```
````

- [ ] **Step 2: Retitle the Build section and add the fyne warning**

Change `## Build` to `## Build from source` and append to it:

```markdown
`scripts/package-linux.sh <version>` builds both release artifacts locally into
`dist/`. Note that `fyne package` **rewrites `FyneApp.toml` in place**, stripping
every comment, reordering keys and incrementing `Build`. The script takes a copy
first and restores it on exit, so running the script is safe even with
uncommitted edits in that file. If you invoke `fyne package` by hand, restore the
file yourself — and be aware that `git checkout -- FyneApp.toml` will also throw
away any uncommitted edits you had, since it restores the index, not the state
the file was in a moment earlier.
```

- [ ] **Step 3: Verify the version numbers are consistent**

```bash
grep -n "1\.0\.0" README.md
```
Expected: every occurrence is in the Install section examples and uses the same
version. They are illustrative, so consistency is all that matters.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: lead with install, not with build

A public repo's first question is how to install it, and the claude CLI
prerequisite was buried inside the build instructions. Also records the glibc
floor and corrects the libfuse2 folklore."
```

---

### Task 9: `MANUAL-TESTS.md`

CI proves the artifacts build. It cannot prove a tray icon renders or that a
real alarm fires.

**Files:**
- Modify: `MANUAL-TESTS.md`

- [ ] **Step 1: Append the release section**

```markdown
## Release artifacts

Run these against the artifacts **CI produced**, downloaded from the release
page — never against a local build. A local build has a different glibc floor
and different bundled libraries, so it cannot answer these questions.

- [ ] Download all three assets and run `sha256sum -c SHA256SUMS` in a fresh
      directory. Expect two `OK` lines and no path errors.
- [ ] `chmod +x` the AppImage and run it. The window appears.
- [ ] The tray icon appears (KDE Plasma 6 is the verified environment).
- [ ] Arm an alarm two minutes out with a short lead-in and let it fire. The
      Claude run completes and the answer is shown.
- [ ] `./claude-alarm-clock-<v>-x86_64.AppImage -version` prints the release
      tag, not `dev`.
- [ ] Extract the tar.xz and `sudo make install`. The app appears in the
      application menu with its icon. `sudo make uninstall` removes it cleanly.
- [ ] `make user-install` works without root and puts the entry in
      `~/.local/share/applications`.
- [ ] `ldd` on the installed binary: `libGL`, `libX11` and `libxkbcommon`
      resolve to system paths under `/usr/lib`, never to a bundled copy.
```

- [ ] **Step 2: Commit**

```bash
git add MANUAL-TESTS.md
git commit -m "docs: manual checks for the release artifacts"
```

---

### Task 10: Cut the first release

**Files:** none

- [ ] **Step 1: Merge to main**

```bash
git push
gh pr create --fill --base main --head feat/linux-release-packaging
```

Wait for `ci.yml` to pass on the PR, then merge.

- [ ] **Step 2: Confirm ci.yml is green on main**

```bash
git checkout main && git pull
gh run watch "$(gh run list --workflow=ci.yml --branch main --limit 1 --json databaseId --jq '.[0].databaseId')"
```
Expected: both jobs green.

- [ ] **Step 3: Tag and push**

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
```

- [ ] **Step 4: Watch the release workflow**

```bash
gh run watch "$(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
gh release view v1.0.0
```
Expected: three assets attached —
`claude-alarm-clock-1.0.0-linux-amd64.tar.xz`,
`claude-alarm-clock-1.0.0-x86_64.AppImage`, `SHA256SUMS`.

- [ ] **Step 5: Work through `MANUAL-TESTS.md` § Release artifacts**

Every box. If any fails, delete the release and the tag, fix, and re-tag:

```bash
gh release delete v1.0.0 --yes
git push --delete origin v1.0.0
git tag -d v1.0.0
```

- [ ] **Step 6: Update `FyneApp.toml` Version to match reality**

The committed `Version` is only a local default — CI overrides it — but leaving
it at `1.0.0` after releasing `v1.0.0` keeps it honest. Bump it in the same
commit as any future release preparation.

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| D1 two artifacts | 4, 5 |
| D2 x86_64 only | Global Constraints; no arm64 anywhere |
| D3 tag is the source of truth | 4 (`--app-version`, `TAG`), 7 |
| D4 container | 1 (proof), 6, 7 |
| `fyne package` rewrites the TOML | 4 (trap), 3 (warning), 8 (README) |
| `--app-version` rejects `v` | Global Constraints, 4, 7 |
| `--src` cannot find the root TOML | 4 (two-step build) |
| No `-ldflags` on `fyne package` | 4 (single build, both artifacts reuse it) |
| Archive name has spaces | 4 (rename) |
| `Categories=` mandatory | 3, 4 (assertion) |
| No `libfuse2` in docs | 8 |
| `APPIMAGE_EXTRACT_AND_RUN` as env | 5 |
| Never `-l` to linuxdeploy | 5 |
| AppDir layout differs from fyne's | 5 |
| `-X main.version` constraints | 2 |
| ReadBuildInfo fallback | 2 |
| apt dependency list | 1, 6, 7 |
| `ci.yml` test + package-smoke | 6 |
| README restructure | 8 |
| MANUAL-TESTS additions | 9 |
| glibc floor assertion | 4 |
| SHA256SUMS portability | 5 |

No gaps.

**Type consistency:** `Info`, `Resolve`, `String`, `DefaultVersion` are used
identically in Tasks 2 and 4. The script's `$BINARY`, `$APP_ID`, `$VERSION`,
`$TAG`, `$P` and `$APPIMAGE` are defined in Task 4 and reused unchanged in
Task 5. Asset filenames are identical in Tasks 4, 5, 7, 8 and 9.

**Known residual risks:**
1. Action major versions (`@v7`) move. Task 1 surfaces a bad ref immediately.
2. `linuxdeploy` continuous is unpinned. The AppDir assertion in Task 5 is the
   net.
3. `ubuntu:22.04` is a moving tag. The `MAX_GLIBC` ceiling catches the drift
   that matters — the tag moving to a base whose glibc exceeds the promise.
   It deliberately does not catch the floor moving *down*, which is harmless.
4. `fyne.io/tools/cmd/fyne@latest` is unpinned. If it breaks, pin the version
   in Tasks 6 and 7 together.
