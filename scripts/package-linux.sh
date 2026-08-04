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
