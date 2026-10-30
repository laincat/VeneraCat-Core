// A provider's usage as Magpie reports it, whatever shape its vendor answered.
//
// A package's `auth.usage` returns its vendor's own numbers; Magpie normalises
// them and so does this, rule for rule: a value the vendor did not give must
// stay unsaid rather than become zero, because "0% used" and "I cannot tell"
// read very differently on a quota card.
package magpieplugin

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// UsageReport is one account's usage, normalised.
type UsageReport struct {
	Plan string `json:"plan"`
	// Until is the vendor's own day or window end, as RFC 3339, when it said.
	Until string `json:"until"`
	// Renew is "auto", "off", or empty when the vendor said neither.
	Renew   string `json:"renew"`
	Balance string `json:"balance"`
	Error   string `json:"error"`
	User    string `json:"user"`
	// SignIn is "expired", "kept", "renewed", or empty.
	SignIn string       `json:"signIn"`
	Resets *UsageResets `json:"resets"`
	// Windows are the quota windows the account has.
	Windows []UsageWindow `json:"windows"`
}

// UsageResets is when the vendor's windows turn over.
type UsageResets struct {
	Count    float64       `json:"count"`
	Until    string        `json:"until"`
	ByWindow bool          `json:"byWindow"`
	FiveHour float64       `json:"fiveHour"`
	Weekly   float64       `json:"weekly"`
	Team     bool          `json:"team"`
	Each     []ResetWindow `json:"each"`
}

// ResetWindow is one window's own reset.
type ResetWindow struct {
	Until  string `json:"until"`
	Window string `json:"window"`
}

// UsageWindow is one quota. A nil field means the vendor did not say it.
type UsageWindow struct {
	Name string `json:"name"`
	// Used may be past 100 when overspent, and is nil when unsaid.
	Used      *float64 `json:"used"`
	ResetsAt  string   `json:"resetsAt"`
	ResetSecs float64  `json:"resetSecs"`
	Display   string   `json:"display"`
	Amount    float64  `json:"amount"`
	Limit     float64  `json:"limit"`
	Unit      string   `json:"unit"`
	Span      float64  `json:"span"`
	Model     string   `json:"model"`
	Models    []string `json:"models"`
	NotModels []string `json:"notModels"`
	// Aside keeps a window out of routing: display only.
	Aside     bool    `json:"aside"`
	RunsOutAt string  `json:"runsOutAt"`
	RunsOut   float64 `json:"runsOut"`
	// Balance is a prepaid wallet's figure, nil when unsaid.
	Balance *float64 `json:"balance"`
}

// NormalizeUsage turns whatever a package answered into the report a client can
// show.
func NormalizeUsage(raw map[string]any) UsageReport {
	report := UsageReport{
		Plan:    usageText(raw["plan"]),
		Until:   usageTime(raw["until"]),
		Renew:   usageOneOf(raw["renew"], "auto", "off"),
		Balance: usageText(raw["balance"]),
		Error:   usageText(raw["error"]),
		User:    usageText(raw["user"]),
		SignIn:  usageOneOf(raw["signIn"], "expired", "kept", "renewed"),
	}
	if resets, ok := raw["resets"].(map[string]any); ok {
		report.Resets = &UsageResets{
			Count:    numberValue(resets["count"]),
			Until:    usageTime(resets["until"]),
			ByWindow: resets["byWindow"] == true,
			FiveHour: numberValue(resets["fiveHour"]),
			Weekly:   numberValue(resets["weekly"]),
			Team:     resets["team"] == true,
		}
		for _, item := range usageList(resets["each"]) {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			report.Resets.Each = append(report.Resets.Each, ResetWindow{
				Until:  usageTime(entry["until"]),
				Window: usageOneOf(entry["window"], "fiveHour", "weekly"),
			})
		}
	}
	for _, item := range usageList(raw["windows"]) {
		window, ok := item.(map[string]any)
		if !ok {
			continue
		}
		report.Windows = append(report.Windows, UsageWindow{
			Name:      usageText(window["name"]),
			Used:      usageUsed(window["used"]),
			ResetsAt:  usageTime(window["resetsAt"]),
			ResetSecs: math.Max(0, math.Round(numberValue(window["resetSecs"]))),
			Display:   usageText(window["display"]),
			Amount:    math.Max(0, numberValue(window["amount"])),
			Limit:     math.Max(0, numberValue(window["limit"])),
			Unit:      usageText(window["unit"]),
			Span:      math.Max(0, numberValue(window["span"])),
			Model:     usageText(window["model"]),
			Models:    usageNames(window["models"]),
			NotModels: usageNames(window["notModels"]),
			Aside:     window["aside"] == true,
			RunsOutAt: usageTime(window["runsOutAt"]),
			RunsOut:   math.Max(0, numberValue(window["runsOut"])),
			Balance:   usageBalance(window["balance"]),
		})
	}
	return report
}

// UsageReport runs the package's usage hook and normalises its answer.
func (in *Instance) UsageReport(provider string) (*UsageReport, error) {
	raw, err := in.Usage(provider)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	report := NormalizeUsage(raw)
	return &report, nil
}

// UsageJSON is UsageReport as JSON, for a client that speaks JSON.
func (in *Instance) UsageJSON(provider string) (string, error) {
	report, err := in.UsageReport(provider)
	if err != nil {
		return "", err
	}
	if report == nil {
		return "", nil
	}
	body, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func usageText(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func usageOneOf(value any, allowed ...string) string {
	s, _ := value.(string)
	for _, candidate := range allowed {
		if s == candidate {
			return s
		}
	}
	return ""
}

// numberOf reads a finite number, keeping json.Number's own text; anything else
// is 0, as Magpie does, so a string never becomes a percentage.
func numberValue(value any) float64 {
	switch v := value.(type) {
	case nil:
		return 0
	case json.Number:
		f, err := strconv.ParseFloat(v.String(), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0
		}
		return f
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case bool:
		return 0
	}
	return 0
}

// usedOf keeps "not said" apart from zero.
func usageUsed(value any) *float64 {
	if value == nil {
		return nil
	}
	f := math.Max(0, numberValue(value))
	return &f
}

// balanceOf is a wallet's figure: only a number counts, and nothing is invented.
func usageBalance(value any) *float64 {
	if value == nil || usageText(value) != "" {
		return nil
	}
	if _, ok := value.(json.Number); !ok {
		if _, ok := value.(float64); !ok {
			return nil
		}
	}
	f := math.Max(0, numberValue(value))
	return &f
}

// timeOf turns an instant into RFC 3339. A number below 1e11 is seconds, as
// Magpie reads it, so a vendor sending seconds is not read as the year 1970.
func usageTime(value any) string {
	text := ""
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		text = v
		if text == "" {
			return ""
		}
	case json.Number:
		text = v.String()
	case float64:
		// A value that came from JavaScript rather than from JSON, as a hook's
		// own number does.
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return ""
		}
		text = strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		text = strconv.FormatInt(v, 10)
	case int:
		text = strconv.Itoa(v)
	default:
		return ""
	}
	if millis, ok := epochMillis(text); ok {
		return millis.UTC().Format(time.RFC3339)
	}
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	return ""
}

func epochMillis(text string) (time.Time, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return time.Time{}, false
	}
	if seconds < 1e11 {
		seconds *= 1000
	}
	return time.UnixMilli(int64(seconds)), true
}

func usageList(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return nil
}

func usageNames(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
