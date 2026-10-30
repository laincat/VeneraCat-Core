package backup

import (
	"sort"
	"strings"
)

// CookieTargets decides which partitions receive the backup's cookies.
//
// The sources a backup carries are the answer, not the sources already installed:
// an import installs them, and the case that matters most — a new device restoring
// a backup — has nothing installed yet. Requiring an overlap would hand that device
// no cookies at all, which is the one migration this feature exists for.
//
// When the backup carries no source scripts, every installed partition is used
// instead. A jar only sends a cookie to a matching domain, so a cookie handed to a
// source that never visits that host is inert rather than shared.
func CookieTargets(backupSources, installed []string) []string {
	if keys := dedupe(backupSources); len(keys) > 0 {
		return keys
	}
	return dedupe(installed)
}

// ResolveCookieTargets fills CookieTargets from the sources this device has.
//
// Both clients need the same three lines — the plan's source keys, the installed
// keys, and the rule — so the rule lives here once rather than in each client.
func (p *Plan) ResolveCookieTargets(installed []string) {
	if len(p.Cookies) == 0 {
		p.CookieTargets = nil
		return
	}
	keys := make([]string, 0, len(p.Sources))
	for _, source := range p.Sources {
		keys = append(keys, source.Key)
	}
	p.CookieTargets = CookieTargets(keys, installed)
}

// usable reports whether a cookie could ever be sent to a host.
func (c Cookie) usable() bool {
	if strings.TrimSpace(c.Name) == "" || c.Value == "" {
		return false
	}
	domain := strings.TrimPrefix(strings.TrimSpace(c.Domain), ".")
	if domain == "" || strings.ContainsAny(domain, "/: ") || !strings.Contains(domain, ".") {
		return false
	}
	return true
}

func dedupe(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}
