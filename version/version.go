// Package version answers "which Core is this" for anything that links Core
// directly: the desktop binary, tests, and a bug report.
//
// The value comes from the build, not from a constant someone forgets to bump:
// Go stamps a module's own revision into the binary when it is built from a
// checkout, and `-ldflags -X` can override it for a release build.
package version

import (
	"runtime/debug"
	"strings"
)

// release is what a build without VCS information reports. Release builds set it
// with -ldflags "-X github.com/laincat/veneracat-core/version.release=v1.2.3".
var release = ""

// SetRelease records the version at build time for a program that links Core as a
// local module: such a build has no module version to read, and its own commit
// would be the wrong answer. A program generates a small file that calls this.
func SetRelease(v string) { release = v }

// String returns the Core's version: a release tag when the build set one, else
// the VCS revision, else "unknown". It never returns an empty string, so a
// caller can print it without checking.
func String() string {
	if release != "" {
		return release
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return describe(info)
}

// describe turns build information into the answer, so the branches can be
// tested without a real binary: the values come from the toolchain, not from us.
func describe(info *debug.BuildInfo) string {
	// When Core is the main module, its revision is stamped into the build. When
	// something else links Core — the desktop binary does — the revision belongs
	// to that other program, so it is not Core's to report: the module version is
	// what is known, and a replaced module has none.
	if info.Main.Path != modulePath {
		return dependencyVersion(info)
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			short := setting.Value
			if len(short) > 12 {
				short = short[:12]
			}
			if dirty := dirtySuffix(info.Settings); dirty != "" {
				return short + dirty
			}
			return short
		}
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}
	return "unknown"
}

// modulePath is this package's module, used to tell "Core is the program" from
// "Core is linked into someone else's program".
const modulePath = "github.com/laincat/veneracat-core"

// dependencyVersion reports what a linking program knows: the module version, or
// the replacement's version. A local replace has neither, and "unknown" is the
// honest answer rather than that program's own commit.
func dependencyVersion(info *debug.BuildInfo) string {
	for _, dep := range info.Deps {
		if dep.Path != modulePath {
			continue
		}
		if dep.Replace != nil && dep.Replace.Version != "" {
			return dep.Replace.Version
		}
		if dep.Version != "" && dep.Version != "(devel)" {
			return dep.Version
		}
		return "unknown"
	}
	return "unknown"
}

// dirtySuffix marks a working tree that had uncommitted changes at build time.
// Without it, two different builds can report the same revision.
func dirtySuffix(settings []debug.BuildSetting) string {
	for _, setting := range settings {
		if setting.Key == "vcs.modified" && setting.Value == "true" {
			return "-dirty"
		}
	}
	return ""
}
