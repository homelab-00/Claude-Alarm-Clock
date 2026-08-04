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
