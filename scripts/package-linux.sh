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
#
# Signal-handling boundary: safe under a real terminal Ctrl-C and under CI
# cancellation, because both deliver the signal to the whole foreground
# process group, so the child (go build / fyne package) dies too and this
# trap then restores correctly. NOT safe if a supervisor signals only this
# script's PID: the child is left running, the trap restores now, and the
# orphaned child finishes later and rewrites FyneApp.toml with nothing left
# to undo it. Do not "fix" this by backgrounding the child and forwarding
# signals -- a `cmd &` job ignores SIGINT outright under bash's POSIX
# async-list semantics, trading this narrow hole for a wider one.
TOML_BACKUP="$(mktemp)"
cp FyneApp.toml "${TOML_BACKUP}"
restore_toml() { cp "${TOML_BACKUP}" FyneApp.toml; rm -f "${TOML_BACKUP}"; }
trap restore_toml EXIT

rm -rf dist stage AppDir
mkdir -p dist

echo "==> Building ${BINARY} (${TAG})"
LDFLAGS="-s -w -X main.version=${TAG}"
CGO_ENABLED=1 go build \
  -trimpath \
  -ldflags "${LDFLAGS}" \
  -o "${BINARY}" ./cmd/alarmclock

echo "==> Asserting -X main.version=${TAG} reached the go build invocation"
# `go version -m` cannot answer this: cmd/go only records a binary's -ldflags
# in its build info when -trimpath is unset (https://go.dev/issue/52372 --
# ldflags can carry local paths via -extld/-extar/-extldflags, so the
# toolchain redacts the setting entirely rather than risk leaking one;
# confirmed empirically against this toolchain). This script passes
# -trimpath deliberately so the release binary embeds no local filesystem
# paths, which means `go version -m` on the shipped binary will NEVER show
# -ldflags, whether or not -X actually reached the build. `go build -n` is a
# dry run: it prints the exact commands go build would issue -- including
# the `go tool link` line -- without compiling or linking anything, so it
# works regardless of -trimpath. Assert the -X assignment appears there for
# this exact package and this exact LDFLAGS.
#
# This proves -X main.version=${TAG} reached the linker's command line --
# i.e. it survived this script's quoting and TAG expansion and got as far as
# `go build`. It does NOT prove the linker applied it: -X silently no-ops if
# main.version is not a plain package-level string var, and this check
# cannot see that. Only actually running the binary (below) can.
#
# -o must NOT be ${BINARY} here: that path already exists from the real
# build above with these exact same flags, so go build (even with -n) sees
# a cached, up-to-date target and prints only "touch alarmclock" instead of
# the link line -- silently defeating this whole check. A distinct,
# never-created path forces the full plan to print every time.
#
# Remove any stale ${BINARY}.linkcheck first: if a file already sits at that
# path with a matching build ID, `go build -n` reports IT as up to date too,
# printing "touch alarmclock.linkcheck" instead of the link line -- the exact
# bug this distinct path was chosen to avoid, just one build cycle later.
rm -f "${BINARY}.linkcheck"
LINK_PLAN="$(CGO_ENABLED=1 go build -n -trimpath -ldflags "${LDFLAGS}" -o "${BINARY}.linkcheck" ./cmd/alarmclock 2>&1)"
if ! grep -qF -- "-X main.version=${TAG}" <<<"${LINK_PLAN}"; then
  echo "ERROR: -X main.version=${TAG} never reached a go build/link invocation." >&2
  echo "       LDFLAGS='${LDFLAGS}' -- check quoting and TAG expansion above." >&2
  exit 1
fi

echo "==> Asserting the binary's own -version output reflects it"
# This actually runs the binary and reads what it prints -- the one thing
# the check above cannot do. It catches a binary that reports nothing,
# reports "dev", or reports some other version outright -- any of which mean
# a real release shipped an unidentifiable binary. It does NOT by itself
# distinguish "-X succeeded" from "-X silently no-oped and
# buildinfo.Resolve fell back to debug.ReadBuildInfo().Main.Version": Go
# 1.24+ stamps the git tag into Main.Version on a tagged checkout, and a
# real release builds from exactly such a checkout with TAG equal to that
# tag, so a working -X and a silently-ignored -X print byte-identical
# output. That blind spot is exactly what the check above exists to close.
# Neither check alone is the guard; both together are.
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
  # grep exits 1 on zero matches, and under `set -o pipefail` that kills the
  # script right here, before the diagnostic below ever runs. Absorb only
  # grep's own "no match" outcome (an objdump failure still propagates and
  # still fails the script, as it should).
  FLOOR="$(objdump -T "${BINARY}" | { grep -o 'GLIBC_[0-9.]*' || true; } | sort -uV | tail -1)"
  if [ -z "${FLOOR}" ]; then
    echo "ERROR: objdump -T ${BINARY} produced no GLIBC_* versioned symbols;" >&2
    echo "       could not determine the glibc floor to check against MAX_GLIBC=${MAX_GLIBC}." >&2
    exit 1
  fi
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

echo "==> Patching the shipped Makefile's Icon variable"
# Upstream bug, fyne.io/tools@v1.7.2 (cmd/fyne/internal/commands/package-unix.go):
# the pixmap is written to disk as `appIDOrName + filepath.Ext(icon)` -- here
# "${APP_ID}.png" -- but the Makefile template it emits sets Icon to the bare
# appIDOrName, with NO extension. `install` and `user-install` then try to
# `install` a file that does not exist and fail outright; `uninstall` and
# `user-uninstall` only look like they still work because their `rm` lines
# are prefixed with `-`, which tells make to ignore the failure -- they
# silently no-op against the wrong path instead of removing anything.
#
# Do NOT "fix" this by renaming the .png to the extension-less name instead.
# The .desktop's Icon=gr.polaris.claudealarm is correct as written: freedesktop
# icon-theme lookup resolves a bare Icon= name to a file with an extension
# somewhere on the icon path, so installing the .png under its real name is
# the right behaviour. The Makefile's Icon variable is simply wrong, and this
# patches only that.
MAKEFILE="stage/${BINARY}/Makefile"
PIXMAP_DIR="stage/${BINARY}/usr/local/share/pixmaps"
PIXMAP_FILE="$(find "${PIXMAP_DIR}" -maxdepth 1 -type f -name "${APP_ID}.*" -printf '%f\n')"
if [ -z "${PIXMAP_FILE}" ]; then
  echo "ERROR: no file matching ${APP_ID}.* found in ${PIXMAP_DIR}." >&2
  echo "       fyne package's output layout may have changed; nothing to point the Makefile's Icon at." >&2
  exit 1
fi
if [ "$(wc -l <<<"${PIXMAP_FILE}")" -ne 1 ]; then
  echo "ERROR: expected exactly one file matching ${APP_ID}.* in ${PIXMAP_DIR}, found:" >&2
  echo "${PIXMAP_FILE}" >&2
  exit 1
fi
sed -i "s/^Icon := \"${APP_ID}\"\$/Icon := \"${PIXMAP_FILE}\"/" "${MAKEFILE}"
if ! grep -qF "Icon := \"${PIXMAP_FILE}\"" "${MAKEFILE}"; then
  echo "ERROR: failed to patch Icon in ${MAKEFILE} -- fyne's Makefile template may have changed." >&2
  exit 1
fi

echo "==> Repacking the corrected tar.xz"
# Same name, format and internal directory structure fyne produced -- the
# Makefile's bytes are the only thing that differ from what it wrote.
rm -f "dist/${TARBALL}"
( cd stage && tar -Jcf "../dist/${TARBALL}" "${BINARY}" )

echo "==> Asserting the packaged Makefile actually installs (regression guard)"
# This exact check was missing, which is how a Makefile that fails on
# `install` shipped in the first place: everything else here builds and
# smoke-tests the binary and the AppImage, but nothing had ever run the
# Makefile itself. Run it for real, into a scratch DESTDIR, and assert the
# three files it promises actually land. Revert the Icon patch above and
# this must fail -- a check that passes either way is worse than no check.
(
  set -euo pipefail
  D="$(mktemp -d)"
  trap 'rm -rf "${D}"' EXIT
  make -C "stage/${BINARY}" DESTDIR="${D}" install
  test -f "${D}/usr/bin/${BINARY}"
  test -f "${D}/usr/share/applications/${APP_ID}.desktop"
  test -f "${D}/usr/share/pixmaps/${PIXMAP_FILE}"
)
echo "    OK: install lands the binary, the .desktop entry and the icon"

echo "==> Asserting 'make user-install' too (fake HOME, never the real one)"
# HOME is set as a make command-line override, which the Makefile's own
# $(HOME) references take verbatim -- it is never exported to this shell's
# environment, so nothing outside the scratch directory below is touched,
# including the sed rewrite of the installed .desktop's Exec= line.
(
  set -euo pipefail
  D="$(mktemp -d)"
  trap 'rm -rf "${D}"' EXIT
  make -C "stage/${BINARY}" HOME="${D}" user-install
  test -f "${D}/.local/bin/${BINARY}"
  test -f "${D}/.local/share/applications/${APP_ID}.desktop"
  test -f "${D}/.local/share/icons/${PIXMAP_FILE}"
)
echo "    OK: user-install lands the binary, the .desktop entry and the icon"

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
#
# A missing AppDir/usr/lib means this check could not run at all -- that is
# NOT the same thing as the check passing. Treat it as a hard failure instead
# of silently falling through to the "OK" message below (this script has a
# documented history of assertions that pass against broken implementations;
# do not add another).
if [ ! -d AppDir/usr/lib ]; then
  echo "ERROR: AppDir/usr/lib does not exist -- the excludelist could not be" >&2
  echo "       verified. linuxdeploy may have changed its output layout, or" >&2
  echo "       the build above failed silently. A missing directory is not a" >&2
  echo "       passing check." >&2
  exit 1
fi

# Recurse, and match on each entry's basename rather than the raw find
# output: a forbidden library commonly lands in a subdirectory (e.g. a
# multiarch-style AppDir/usr/lib/x86_64-linux-gnu/), and a bundled library is
# frequently a symlink rather than a regular file, so both -type f and
# -type l must be included. The type tests are grouped in \( \): find's -o
# binds looser than juxtaposition, so an ungrouped
# `-type f -o -type l -name ...` would apply any trailing test only to the
# -type l branch, silently exempting every regular file from it.
BUNDLED="$(find AppDir/usr/lib \( -type f -o -type l \) -printf '%f\n' | sort -u)"
echo "${BUNDLED}"
if grep -Ei \
  '^(libGL\.|libEGL\.|libGLX\.|libGLdispatch\.|libOpenGL\.|libX11\.|libxcb\.|libdrm\.|libglapi\.|libgbm\.|libwayland-client\.|libc\.so|ld-linux|libm\.so|libresolv\.|libpthread\.|libdl\.so|librt\.so|libnss_)' \
  <<<"${BUNDLED}"; then
  echo "ERROR: driver or glibc libraries leaked into the AppDir." >&2
  echo "       The AppImage would break on any machine with a different GPU stack." >&2
  exit 1
fi
echo "    OK: no GL/X11/driver/glibc libraries bundled"

echo "==> Smoke-testing the AppImage"
# Mirror the native-binary -version assertion above: an exit status alone
# does not prove the AppImage reports the right version, only that it ran.
# A stand-in that runs fine and prints the wrong (or no) version must fail
# this check exactly as it would fail the binary check above.
APPIMAGE_ACTUAL="$(APPIMAGE_EXTRACT_AND_RUN=1 "dist/${APPIMAGE}" -version)"
echo "    ${APPIMAGE_ACTUAL}"
case "${APPIMAGE_ACTUAL}" in
  *"${TAG}"*) ;;
  *) echo "ERROR: AppImage -version reported '${APPIMAGE_ACTUAL}', expected it to contain '${TAG}'." >&2
     echo "       An AppImage that runs but reports the wrong version is a broken release." >&2
     exit 1 ;;
esac

echo "==> Generating SHA256SUMS"
# Generated from inside dist/ with bare globs so the file contains BASENAMES.
# `sha256sum "$PWD"/dist/*` would bake in runner paths, still exit 0 on the
# builder, and fail for every real downloader.
( cd dist && sha256sum claude-alarm-clock-* > SHA256SUMS && sha256sum -c SHA256SUMS )

echo "==> dist/"
ls -la dist/
