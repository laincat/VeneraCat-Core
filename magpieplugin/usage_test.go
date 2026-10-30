package magpieplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// decode keeps numbers as json.Number, the way a package's answer crosses the
// bridge.
func decodeUsage(t *testing.T, text string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNormalizeUsageKeepsUnsaidApartFromZero(t *testing.T) {
	report := NormalizeUsage(decodeUsage(t, `{
		"plan": "individual",
		"windows": [
			{"name": "Premium requests", "used": 50, "amount": 150, "limit": 300, "unit": "requests"},
			{"name": "Unknown", "display": "No allowance reported"}
		]
	}`))
	if len(report.Windows) != 2 {
		t.Fatalf("windows = %d", len(report.Windows))
	}
	first := report.Windows[0]
	if first.Used == nil || *first.Used != 50 {
		t.Fatalf("used = %v", first.Used)
	}
	if first.Amount != 150 || first.Limit != 300 || first.Unit != "requests" {
		t.Fatalf("first = %+v", first)
	}
	// The vendor said nothing about this window's share; 0 would read as "0%
	// used" on a quota card.
	if report.Windows[1].Used != nil {
		t.Fatalf("unsaid used became %v", *report.Windows[1].Used)
	}
	if report.Windows[1].Display != "No allowance reported" {
		t.Fatalf("display = %q", report.Windows[1].Display)
	}
}

// A vendor that overspends reports past 100, and it must survive: clamping would
// hide that the account is over its limit.
func TestNormalizeUsageKeepsOverspend(t *testing.T) {
	report := NormalizeUsage(decodeUsage(t, `{"windows":[{"name":"x","used":137.5}]}`))
	if got := *report.Windows[0].Used; got != 137.5 {
		t.Fatalf("used = %v", got)
	}
	report = NormalizeUsage(decodeUsage(t, `{"windows":[{"name":"x","used":-5}]}`))
	if got := *report.Windows[0].Used; got != 0 {
		t.Fatalf("negative used = %v", got)
	}
}

// Seconds and milliseconds are both seen in the wild; reading 1.7e9 as
// milliseconds would put the reset in 1970.
func TestNormalizeUsageReadsEpochBothWays(t *testing.T) {
	report := NormalizeUsage(decodeUsage(t, `{
		"until": 1767225600,
		"windows": [{"name":"x","resetsAt": 1767225600000}]
	}`))
	if report.Until != "2026-01-01T00:00:00Z" {
		t.Fatalf("until = %q", report.Until)
	}
	if report.Windows[0].ResetsAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("resetsAt = %q", report.Windows[0].ResetsAt)
	}
	report = NormalizeUsage(decodeUsage(t, `{"until":"2026-01-01T08:00:00+08:00"}`))
	if report.Until != "2026-01-01T00:00:00Z" {
		t.Fatalf("until = %q", report.Until)
	}
	if NormalizeUsage(decodeUsage(t, `{"until":"later"}`)).Until != "" {
		t.Fatal("an unreadable time became a time")
	}
}

// A string where a number belongs is a vendor bug, not a percentage.
func TestNormalizeUsageIgnoresWrongTypes(t *testing.T) {
	report := NormalizeUsage(decodeUsage(t, `{
		"plan": 7, "windows": [{"name": 3, "used": "50", "limit": "300", "models": ["a", 2, "b"]}],
		"renew": "sometimes", "signIn": "maybe",
		"resets": {"count": 2, "each": [{"window":"weekly","until":1767225600}, "nonsense"]}
	}`))
	if report.Plan != "" {
		t.Fatalf("plan = %q", report.Plan)
	}
	if report.Renew != "" || report.SignIn != "" {
		t.Fatalf("renew/signIn = %q/%q", report.Renew, report.SignIn)
	}
	window := report.Windows[0]
	if window.Name != "" || window.Limit != 0 {
		t.Fatalf("window = %+v", window)
	}
	// A vendor that sent a string for a number said something, just not a
	// number: Magpie reads that as 0 rather than as unsaid, and this matches it
	// so the two hosts agree on the same account.
	if window.Used == nil || *window.Used != 0 {
		t.Fatalf("used = %v", window.Used)
	}
	if len(window.Models) != 2 || window.Models[0] != "a" {
		t.Fatalf("models = %v", window.Models)
	}
	if report.Resets == nil || report.Resets.Count != 2 || len(report.Resets.Each) != 1 {
		t.Fatalf("resets = %+v", report.Resets)
	}
	if report.Resets.Each[0].Window != "weekly" || report.Resets.Each[0].Until != "2026-01-01T00:00:00Z" {
		t.Fatalf("each = %+v", report.Resets.Each)
	}
}

// A wallet has a balance and nothing it is a share of.
func TestNormalizeUsageKeepsABalance(t *testing.T) {
	report := NormalizeUsage(decodeUsage(t, `{"windows":[{"name":"积分","balance":6291,"unit":"积分"}]}`))
	window := report.Windows[0]
	if window.Used != nil {
		t.Fatalf("a wallet invented used = %v", *window.Used)
	}
	if window.Balance == nil || *window.Balance != 6291 {
		t.Fatalf("balance = %v", window.Balance)
	}
	if window.Unit != "积分" {
		t.Fatalf("unit = %q", window.Unit)
	}
	// A balance that is not a number is not a balance.
	other := NormalizeUsage(decodeUsage(t, `{"windows":[{"name":"x","balance":"many"}]}`))
	if other.Windows[0].Balance != nil {
		t.Fatalf("balance = %v", *other.Windows[0].Balance)
	}
}

// The JSON a client reads keeps the null that means "not said".
func TestUsageJSONKeepsNull(t *testing.T) {
	body, err := json.Marshal(NormalizeUsage(decodeUsage(t, `{"windows":[{"name":"x"}]}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"used":null`) {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(string(body), `"windows":[`) {
		t.Fatalf("body = %s", body)
	}
}
