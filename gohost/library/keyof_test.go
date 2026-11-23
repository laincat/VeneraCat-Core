package library

import (
	"errors"
	"fmt"
	"testing"

	"github.com/laincat/veneracat-core/plugin"
)

func TestKeyOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"registry collision", &plugin.KeyExistsError{Key: "copy_manga"}, "copy_manga"},
		{"wrapped collision", fmt.Errorf("install: %w", &plugin.KeyExistsError{Key: "wrapped"}), "wrapped"},
		{"script text", errors.New("plugin: key already installed: copy_manga"), ""},
		{"unstructured sentinel", fmt.Errorf("%w: forged", plugin.ErrExists), ""},
		{"no error", nil, ""},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := keyOf(item.err); got != item.want {
				t.Fatalf("keyOf(%v)=%q, want %q", item.err, got, item.want)
			}
		})
	}
}
