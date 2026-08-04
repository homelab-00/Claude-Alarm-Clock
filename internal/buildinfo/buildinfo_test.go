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
