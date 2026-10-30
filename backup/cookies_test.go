package backup

import (
	"reflect"
	"testing"
)

func TestCookieTargets(t *testing.T) {
	cases := []struct {
		name       string
		inBackup   []string
		installed  []string
		wantTarget []string
	}{
		// The sources the backup carries are installed by the import, so a fresh
		// device must still receive its cookies. That is the whole migration case.
		{"a backup names its own sources", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b"}},
		{"nothing installed yet", []string{"a"}, nil, []string{"a"}},
		{"backup names nothing falls back to all", nil, []string{"x", "y"}, []string{"x", "y"}},
		{"neither side names anything", nil, nil, nil},
		{"duplicates and empty keys collapse", []string{"a", "a"}, []string{"a", "a", ""}, []string{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CookieTargets(tc.inBackup, tc.installed)
			if !reflect.DeepEqual(got, tc.wantTarget) {
				t.Fatalf("targets = %v, want %v", got, tc.wantTarget)
			}
		})
	}
}

func TestPlanResolveCookieTargets(t *testing.T) {
	// The clients call this instead of repeating the rule, so it has to work from
	// the plan alone: sources the plan installs, and the keys this device has.
	p := &Plan{Sources: []SourceRestore{{Key: "copy"}, {Key: "mangadex"}},
		Cookies: []Cookie{{Name: "sid", Value: "v", Domain: ".wnacg.com"}}}
	// Both sources the plan installs get the cookies, whether or not this device
	// had them before: an import installs them, so "installed already" is not the
	// question. A fresh device is the case this has to work for.
	p.ResolveCookieTargets([]string{"other"})
	if len(p.CookieTargets) != 2 || p.CookieTargets[0] != "copy" || p.CookieTargets[1] != "mangadex" {
		t.Fatalf("targets = %v", p.CookieTargets)
	}

	// No cookies selected: no targets, so a client cannot write any.
	p.Cookies = nil
	p.ResolveCookieTargets([]string{"mangadex"})
	if p.CookieTargets != nil {
		t.Fatalf("targets = %v, want none", p.CookieTargets)
	}
}
