package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

// A caller prints this without checking, so it must never be empty.
func TestStringIsNeverEmpty(t *testing.T) {
	if got := String(); strings.TrimSpace(got) == "" {
		t.Fatal("String() returned an empty version")
	}
}

// A release build sets the tag and that wins: it is the number a user was told
// about, and a commit hash would only confuse them.
func TestReleaseTagWins(t *testing.T) {
	previous := release
	release = "v9.9.9"
	defer func() { release = previous }()
	if got := String(); got != "v9.9.9" {
		t.Fatalf("String() = %q", got)
	}
}

// An unclean tree is marked: two builds from one revision but different working
// trees are not the same binary.
func TestDirtyTreeIsMarked(t *testing.T) {
	got := dirtySuffix([]debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef"},
		{Key: "vcs.modified", Value: "true"},
	})
	if got != "-dirty" {
		t.Fatalf("dirtySuffix() = %q", got)
	}
	if got := dirtySuffix(nil); got != "" {
		t.Fatalf("dirtySuffix(nil) = %q", got)
	}
}

// A program that links Core is the case the stamp exists for: none of these
// build infos carry Core's revision, so the module version is all there is, and
// "unknown" is the honest answer when even that is absent.
func TestDependencyVersion(t *testing.T) {
	const core = "github.com/laincat/veneracat-core"
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"a module version", &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/laincat/nekophage", Version: "(devel)"},
			Deps: []*debug.Module{{Path: core, Version: "v1.2.3"}},
		}, "v1.2.3"},
		{"a replaced module's version", &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/laincat/nekophage", Version: "(devel)"},
			Deps: []*debug.Module{{Path: core, Replace: &debug.Module{Path: "../build/core", Version: "v0.9.0"}}},
		}, "v0.9.0"},
		{"a local replace has no version", &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/laincat/nekophage", Version: "(devel)"},
			Deps: []*debug.Module{{Path: core, Version: "(devel)", Replace: &debug.Module{Path: "../build/core"}}},
		}, "unknown"},
		{"Core is not a dependency at all", &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/laincat/nekophage", Version: "(devel)"},
		}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dependencyVersion(c.info); got != c.want {
				t.Fatalf("dependencyVersion() = %q; want %q", got, c.want)
			}
		})
	}
}

// The two ways a build of Core itself can answer, and the fallbacks when the
// toolchain recorded neither. These are the branches that decide what a user
// pastes into a bug report.
func TestDescribe(t *testing.T) {
	const core = "github.com/laincat/veneracat-core"
	revision := func(value string, dirty bool) []debug.BuildSetting {
		settings := []debug.BuildSetting{{Key: "vcs.revision", Value: value}}
		if dirty {
			settings = append(settings, debug.BuildSetting{Key: "vcs.modified", Value: "true"})
		}
		return settings
	}
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"a long revision is shortened", &debug.BuildInfo{
			Main:     debug.Module{Path: core},
			Settings: revision("0123456789abcdef0123456789abcdef", false),
		}, "0123456789ab"},
		{"a short revision is left alone", &debug.BuildInfo{
			Main:     debug.Module{Path: core},
			Settings: revision("abcdef", false),
		}, "abcdef"},
		{"a dirty tree is marked", &debug.BuildInfo{
			Main:     debug.Module{Path: core},
			Settings: revision("0123456789abcdef", true),
		}, "0123456789ab-dirty"},
		{"a tagged build without VCS data", &debug.BuildInfo{
			Main: debug.Module{Path: core, Version: "v1.0.0"},
		}, "v1.0.0"},
		{"a development build with no VCS data", &debug.BuildInfo{
			Main: debug.Module{Path: core, Version: "(devel)"},
		}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := describe(c.info); got != c.want {
				t.Fatalf("describe() = %q; want %q", got, c.want)
			}
		})
	}
}
