package version

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// The official venera_core test cases.
		{"1.2.4", "1.2.3", true},
		{"1.2.3", "1.2.3", false},
		{"1.2.3", "1.2.4", false},
		{"1.2.3", "1.2.3-beta", true},
		{"1.2.3-hotfix", "1.2.3", true},
		{"1.2.3-beta", "1.2.3", false},
		// Further behaviour of the same rules.
		{"1.2.3", "1.2.3-hotfix", false},
		{"1.2.3-rc", "1.2.3-beta", true},
		{"1.10.0", "1.9.9", true},
		{"2", "1.9.9", true},
		{"1.0", "1.0.0", false},
		{"v1.2.3", "1.2.3", false}, // non-numeric part counts as 0: "v1" < "1"
		{"", "0.0.1", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
