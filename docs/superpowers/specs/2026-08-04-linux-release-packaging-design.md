# Linux Release Packaging — Design

**Date:** 2026-08-04
**Status:** Approved, pending implementation plan
**Repo:** https://github.com/homelab-00/Claude-Alarm-Clock

## Goal

Publish the app to GitHub Releases so a Linux user can install it without a Go
toolchain. On a pushed tag `vX.Y.Z`, CI builds and attaches two artifacts plus
a checksum file, and the same packaging path is exercised on every push to
`main` so it cannot rot unnoticed between releases.

## Audience

Developers who already have the `claude` CLI installed. The app shells out to
`claude` (`internal/runner/claude.go:38`, `exec.LookPath`), so it can never be
a self-contained download for a general user. This is a constraint, not a
defect: the packaging effort goes into declaring the dependency clearly, not
into hiding it.

## Scope

**In:** Linux, x86_64, two artifacts (AppImage + `fyne package` tar.xz),
SHA256SUMS, a `-version` flag, a release workflow, a CI workflow, README
restructuring, manual-test additions.

**Out (deliberately):** arm64, Flatpak, Snap, AUR, .deb/.rpm, Windows, macOS,
code signing, auto-update. None are precluded later.

`Flatpak/Snap` are excluded on merit, not just scope: the app must `exec` a
host binary (`claude`) with the process CWD set to an arbitrary user-chosen
directory. Sandboxing that away and then punching through it with
`flatpak-spawn --host` plus `--filesystem=host` pays the full complexity cost
for no isolation benefit.

---

## Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | Two artifacts: AppImage + tar.xz | AppImage answers "let me try it"; the tar.xz installs a `.desktop` entry and icon, which matters for a tray app the user wants to autostart with `-hidden` |
| D2 | x86_64 only | The only architecture that can be verified on the maintainer's own machine before publishing. Shipping an unverified aarch64 binary would contradict the project's existing "verified vs. not verified" honesty in the README |
| D3 | The git tag is the single source of truth for version | One input, three consumers (binary, `.desktop` metadata, asset filenames) |
| D4 | Build inside `container: ubuntu:22.04` on a current runner | The container sets the glibc floor, decoupling the compatibility contract from GitHub's runner-image retirement schedule |

### D3 addendum — supersedes the original plan

The design originally called for CI to patch `Version` in `FyneApp.toml` from
the tag. **That is unnecessary and has been dropped.** `fyne package` accepts
`--app-version` and `--app-build`, and `appData.mergeMetadata` gives the flags
precedence over the TOML. Verified end to end: with `Version = "1.0.0"` in the
file and `--app-version 9.8.7 --app-build 42` on the command line, the built
binary reported `9.8.7 42` and the TOML value was not consulted.

The tag is passed as a flag. The committed `Version = "1.0.0"` remains a
sensible default for local packaging runs.

### D4 addendum — why not a runner label

`ubuntu-22.04` gives glibc 2.35 today, but it enters deprecation **2026-09-17**,
hard-fails during brownouts from 2027-03-23, and is fully unsupported
**2027-04-17** (actions/runner-images#14254). A tag-triggered workflow runs
rarely, so a label-based pin would most likely be discovered broken at the
worst possible moment.

`ubuntu-latest` is never acceptable for a distributable binary: it resolves to
`ubuntu-24.04` (glibc 2.39) today, `ubuntu-26.04` (glibc 2.43) is already in
public preview, and the floor rises silently.

Building inside `ubuntu:22.04` on `ubuntu-24.04` keeps the floor at glibc 2.35
while the runner label stays freely upgradable.

---

## Verified constraints

Everything below was confirmed against source code or by execution, not from
documentation. Several official docs are stale and must not be cited.

### The compatibility contract

A binary linked against glibc *N* runs on any host with glibc ≥ *N* and fails
to start on any host with glibc < *N*. (Avoid the phrase "forward compatible" —
it is used in the opposite sense in the GCC/libstdc++ ABI policy.) The failure
surfaces from the dynamic loader as — this being what a Debian 12 user *would*
see if we built on `ubuntu-24.04` instead, which is precisely what D4 prevents:

```
./alarmclock: /lib/x86_64-linux-gnu/libc.so.6: version `GLIBC_2.39' not found (required by ./alarmclock)
```

A glibc 2.35 floor covers Ubuntu 22.04+, Linux Mint 21, Debian 12 bookworm
(2.36), Debian 13 (2.41), all current Fedora, and every rolling distro. It does
**not** cover RHEL/Rocky/Alma 9 (2.34), Ubuntu 20.04 or Debian 11 (2.31). The
README must state the floor rather than imply universality.

An AppImage does not bundle glibc, so the AppImage inherits the same floor.

### `fyne package` behaviour

1. **It rewrites `FyneApp.toml` in place.** It re-serialises the whole file,
   stripping every comment, reorders keys, and increments `Build`. Observed on
   this repo: the 15-line comment block explaining the `fyneDo` migration
   decision was destroyed and `Build` went 1 → 2. Harmless on an ephemeral CI
   checkout, destructive locally. Requires a `git checkout --` guard and a
   documented warning.

2. **`--app-version` rejects a leading `v`.** Internally it evaluates
   `semver.IsValid("v" + ver)`, so `v1.2.3` becomes `vv1.2.3` and fails with
   `invalid --app-version parameter, integer and '.' characters only up to
   x.y.z`. The `${GITHUB_REF_NAME#v}` strip is mandatory. This is the single
   most likely way a first release run fails.

3. **`--src ./cmd/alarmclock` does not find the repo-root `FyneApp.toml`.** It
   looks only inside `--src` and fails with
   `Missing application icon at .../cmd/alarmclock/Icon.png`. A bare
   `fyne package` at the root also fails (`function main is undeclared in the
   main package`), because `main` lives at `./cmd/alarmclock`.
   **Resolution:** two-step — `go build` the binary, then run
   `fyne package --executable ./alarmclock` from the repo root. This keeps
   `FyneApp.toml` where it is.

4. **It has no `-ldflags` flag.** Without the two-step build the AppImage would
   report the tag and the tar.xz would report `dev` — two artifacts from one
   release disagreeing about their own version.

5. **The archive is named from `Details.Name` verbatim, with no version:**
   `Claude Alarm Clock.tar.xz` — with spaces. Uploaded unrenamed it yields a
   `%20`-encoded download URL, and any unquoted shell reference breaks. Must be
   renamed.

6. **Output is `.tar.xz`, not `.tar.gz`.** `package-unix.go` does
   `tar -Jcf ... .tar.xz`; `.tar.gz` is reserved for OpenBSD.
   `docs.fyne.io/started/packaging` still says `myapp.tar.gz` and is wrong.

7. **The archive's top-level directory and the `.desktop` `Exec=` come from the
   executable basename**, not from `Details.Name`. Building to `alarmclock` is
   therefore a deliberate, user-visible choice.

8. **Flag aliases exist** — `--app-version`/`--appVersion`,
   `--os`/`--target` — but passing both spellings of one flag errors with
   `Cannot use two forms of the same flag`.

### AppImage

1. **`Categories=` is mandatory.** `appimagetool` hard-aborts with
   `.desktop file is missing a Categories= key`. The current `FyneApp.toml` has
   no `[LinuxAndBSD]` section, so fyne's generated `.desktop` omits it. This
   blocks the AppImage job outright.

2. **End users do not need `libfuse2`.** Current `appimagetool` embeds the
   type-2 runtime, which is static-pie linked against libfuse **3** plus musl
   (`ldd` reports `not a dynamic executable`). Users need only the kernel FUSE
   module and the standard setuid `fusermount3` helper, present by default on
   mainstream desktops. The `libfuse2` advice applies only to legacy AppImages
   and must not appear in the README.

3. **CI needs `APPIMAGE_EXTRACT_AND_RUN=1` as an environment variable**, not
   the `--appimage-extract-and-run` CLI flag, because `linuxdeploy` spawns the
   bundled `appimagetool` as a child process and only the environment
   propagates to that nested invocation.

4. **One download suffices.** `linuxdeploy`'s continuous AppImage already
   bundles `linuxdeploy-plugin-appimage`, `appimagetool`, `mksquashfs` and
   `desktop-file-validate`. No `apt install squashfs-tools desktop-file-utils`.

5. **The fyne tree is not a valid AppDir.** fyne emits a `usr/local/…` prefix;
   `linuxdeploy` expects `usr/bin`, `usr/share/applications`,
   `usr/share/icons/hicolor/<size>/apps`. Extract the fyne archive and hand
   `linuxdeploy` the three pieces via `--executable` / `--desktop-file` /
   `--icon-file` so both artifacts carry byte-identical metadata.

6. **Never force-deploy graphics libraries.** `-l` bypasses the built-in
   excludelist. Let `linuxdeploy` skip them; it logs
   `Skipping deployment of blacklisted library`. Bundling `libGL`, `libEGL`,
   `libGLX`, `libX11`, `libxcb`, `libdrm`, Mesa or driver libraries breaks the
   app on any machine whose GPU stack differs from the builder's — the classic
   Mesa-built AppImage failing on NVIDIA.

### Go version stamping

1. `-ldflags "-X main.version=…"` targets the **literal** prefix `main`, not
   the module path. Using `claudealarm.version` fails silently.
2. The target must stay a package-level `string` initialised to a constant. It
   silently stops working if it becomes a `const`, a struct field, or is
   initialised by a function call.
3. **`(devel)` is outdated lore.** Since Go 1.24, `debug.ReadBuildInfo()` on a
   tagged checkout reports the tag itself. The original justification for
   `ldflags` ("ReadBuildInfo cannot give you the tag") was wrong.
4. `ldflags` remains the right choice for different reasons: it is immune to
   `-buildvcs=false`, to a shallow checkout without tags, to the `/vN`
   semantic-import-versioning trap (this module is `claudealarm` with no `/v2`
   suffix, so a pure-ReadBuildInfo implementation would silently start
   reporting `v0.0.0-<pseudo>` the day `v2.0.0` is tagged), and it is the only
   mechanism that survives a build from a source tarball with no `.git`.
5. Combining both is strictly better: `ldflags` when present, `ReadBuildInfo`
   as fallback, plus the short revision and a dirty marker.

### Build dependencies

The originally assumed `libgl1-mesa-dev xorg-dev` is incomplete. The official
Fyne quick-start list is:

```
libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev
```

None are preinstalled on any runner image or in the `ubuntu:22.04` base image.
Inside the container there is no `sudo` and no `git`/`curl`/`ca-certificates`,
so the toolchain install must be the **first** step, before `actions/checkout`.

---

## Components

### 1. `FyneApp.toml` — add `[LinuxAndBSD]`

```toml
[LinuxAndBSD]
Categories = ["Utility"]
GenericName = "Alarm Clock"
Comment = "Runs Claude Code at a chosen time"
```

Required for the AppImage to build at all. `Version`/`Build` stay as they are —
CI overrides them via flags and never writes the file.

The existing `[Migrations] fyneDo = true` comment block gains a warning that
`fyne package` will destroy it if run without the guard below.

### 2. `cmd/alarmclock/version.go` — new file

```go
var version = "dev"
```

plus `versionString()`, which prefers the `ldflags` value and falls back to
`debug.ReadBuildInfo()`, appending the 7-char revision and a `(modified)`
marker for a dirty tree.

`main.go` gains a `-version` flag. **It must be handled immediately after
`flag.Parse()` and before `fyneapp.NewWithID`** — otherwise the check would
require a display and could not run in CI. This ordering is what makes
`-version` usable as the release smoke test.

### 3. `.github/workflows/release.yml`

Trigger `on: push: tags: ['v[0-9]+.[0-9]+.[0-9]+']`. Defining only `tags:` is
what keeps it from running on branch pushes. Job-level
`permissions: contents: write` — minimal and sufficient to create the release
and upload assets; every unlisted scope becomes `none`. No secrets: the
automatic `github.token` is passed to `gh` as `GH_TOKEN`.

```
runs-on: ubuntu-24.04
container: { image: ubuntu:22.04 }
env: CGO_ENABLED=1, APPIMAGE_EXTRACT_AND_RUN=1, DEBIAN_FRONTEND=noninteractive
```

Steps, in order:

1. `apt-get install` toolchain + Fyne deps (first, before checkout, no `sudo`)
2. `actions/checkout`, then `git config --global --add safe.directory` to avoid
   the dubious-ownership `-buildvcs` failure inside the container
3. `actions/setup-go` with `go-version-file: go.mod`
4. Record `ldd --version` in the job log — the compatibility contract, visible
5. `VERSION=${GITHUB_REF_NAME#v}` (the mandatory `v` strip)
6. `go build -trimpath -ldflags "-s -w -X main.version=${GITHUB_REF_NAME}" -o alarmclock ./cmd/alarmclock`

   The two forms are used deliberately and must not be unified: the binary is
   stamped with the tag **verbatim** (`v1.0.0`), because that is the string a
   user will quote back in a bug report and paste into the tag list. `$VERSION`
   (`1.0.0`, stripped) is used only where a leading `v` is rejected or unwanted
   — `--app-version` and the asset filenames.

7. `./alarmclock -version` — assert it prints the tag, not `dev`
8. `objdump -T` assertion that the highest required symbol is `GLIBC_2.35`
9. `fyne package --target linux --executable ./alarmclock --app-version "$VERSION" --app-build "$GITHUB_RUN_NUMBER"` from the repo root
10. `git checkout -- FyneApp.toml` — undo fyne's rewrite
11. Rename `"Claude Alarm Clock.tar.xz"` → `claude-alarm-clock-$VERSION-linux-amd64.tar.xz`
12. Extract it, feed `linuxdeploy` the executable, `.desktop` and icon
13. Assert no GL/X11/driver/glibc libraries leaked into `AppDir/usr/lib/`
14. Rename the AppImage → `claude-alarm-clock-$VERSION-x86_64.AppImage`
15. `sha256sum` from within `dist/` using bare globs (basenames only — absolute
    paths would still exit 0 on the runner while failing for every downloader),
    then `sha256sum -c` as a self-check
16. `gh release create "$GITHUB_REF_NAME" --generate-notes --verify-tag` with
    the three assets

`gh` is preinstalled on hosted runners and is preferred over
`softprops/action-gh-release` — it is first-party and removes the third-party
SHA-pinning question entirely. (For the record: that action's current major is
v3; v2 is EOL.)

### 4. `.github/workflows/ci.yml`

Two jobs on push and PR to `main`:

- **`test`** — `ubuntu-24.04`, no container, apt deps, `go test -race -cover ./...`.
  Fast. The suite is headless: no display, no network, no API spend.
  Additionally assert the output never contains `Error in Fyne call thread`,
  per the existing README rule.
- **`package-smoke`** — the *same container and the same packaging steps as the
  release job*, but uploading to workflow artifacts instead of publishing.

`package-smoke` exists specifically to answer the risk the verification raised:
a tag-only workflow is exercised so rarely that it is normally discovered
broken at the worst moment. Running the packaging path on every push to `main`
means a tag push is the boring case.

### 5. `README.md` restructure

Current order opens with *Build from source*, which is backwards for a public
repo. New order:

```
Requirements   ← the `claude` CLI, stated first and plainly; glibc floor
Install        ← AppImage (2 lines) | tar.xz (menu entry + autostart)
Run
What it runs
Behaviour
Build from source   ← moves down
Tray support / Tests / Licence
```

The Install section states the glibc 2.35 floor and which distributions that
covers, says explicitly that `libfuse2` is **not** required, and documents
`--appimage-extract-and-run` for FUSE-less environments. The Build section
gains the `fyne package` file-rewrite warning.

### 6. `MANUAL-TESTS.md` additions

CI proves the artifacts *build*, not that they *work*. Before a release is
announced:

- Download the **CI-produced** AppImage (not a local build), run it on the
  development machine: tray icon appears, a real alarm fires end to end.
- Install the tar.xz via `sudo make install`; confirm the entry appears in the
  application menu with its icon, and that `make user-install` works without root.
- `./…AppImage -version` prints the tag.
- `sha256sum -c SHA256SUMS` passes from a fresh download directory.
- `ldd` on the extracted binary confirms `libGL`/`libX11` resolve to system
  paths, not to bundled copies.

---

## Failure modes and guards

| Failure | Guard |
|---|---|
| `--app-version` rejects `v1.2.3` | `${GITHUB_REF_NAME#v}`, plus a tag-pattern trigger that only matches `v[0-9]+.[0-9]+.[0-9]+` |
| Two artifacts disagree on version | One `go build`, both artifacts reuse that binary; step 7 asserts `-version` |
| glibc floor drifts silently | `objdump -T` assertion pinned to `GLIBC_2.35` |
| Driver libraries bundled → breaks on other GPUs | Explicit `AppDir/usr/lib/` scan; never use `-l` |
| `fyne package` destroys the TOML comments | `git checkout --` in CI; documented warning for local use |
| Asset URL contains `%20` | Explicit rename before upload |
| `SHA256SUMS` contains runner paths | Generated from `working-directory: dist` with bare globs, then self-checked |
| Release workflow rots between tags | `package-smoke` runs the same path on every push to `main` |
| Container base goes EOL | Ubuntu 22.04 standard support ends 2027-04; noted as a scheduled review, not a surprise |

## Open risks

1. **Container + JS actions.** `actions/checkout` and `actions/setup-go` inside
   a `container:` job were not verified in this environment. The runner
   normally injects its own Node externals, and `ubuntu:22.04` is a common base,
   but this must be proven by an actual run. If it fails, the fallback is a
   plain `docker run` step inside a normal job, which avoids the question
   entirely. **This is the single largest unknown and should be the first thing
   the implementation proves.**
2. **`ubuntu:22.04` is a moving tag.** It currently tracks jammy at glibc 2.35.
   Pinning by digest would be stricter; the `objdump` assertion catches drift
   either way.
3. **`linuxdeploy` continuous** is an unpinned rolling release. Accepted for
   now; the AppDir assertion is the safety net.
4. The AppImage build chain end-to-end was verified in principle but not
   executed against this repo. The first CI run is the real test.

## Next step

An implementation plan, sequenced so the riskiest unknown (container + JS
actions) is proven before any of the packaging work depends on it.
