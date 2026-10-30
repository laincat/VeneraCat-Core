// The daily check-in a plugin may press itself.
//
// Magpie asks each account once a day while the switch is on, and a plugin
// answers with one of a few outcomes. The set is fixed so a client can say what
// happened without parsing a vendor's sentence; an outcome outside it is a
// failure that names what the plugin actually answered.
package magpieplugin

import (
	"encoding/json"
	"errors"
	"fmt"
)

// CheckinOutcome is what a check-in came to.
type CheckinOutcome string

const (
	// CheckinClaimed means the check-in was pressed and succeeded now.
	CheckinClaimed CheckinOutcome = "claimed"
	// CheckinDone means it was already done today.
	CheckinDone CheckinOutcome = "done"
	// CheckinIneligible means the account cannot take part.
	CheckinIneligible CheckinOutcome = "ineligible"
	// CheckinInactive means the vendor runs no check-in right now.
	CheckinInactive CheckinOutcome = "inactive"
	// CheckinCaptcha means a captcha stands in the way. Magpie never solves one
	// and neither does this: the user checks in in the vendor's own app.
	CheckinCaptcha CheckinOutcome = "captcha"
	// CheckinFailed means it did not work.
	CheckinFailed CheckinOutcome = "failed"
)

// CheckinResult is one account's check-in answer.
type CheckinResult struct {
	Outcome CheckinOutcome `json:"outcome"`
	// Credit is what it gave, when it said.
	Credit string `json:"credit"`
	// Streak is consecutive days, when it said.
	Streak float64 `json:"streak"`
	// Message is the vendor's own words, kept for display.
	Message string `json:"message"`
	// Until is when the vendor's own day turns, as RFC 3339.
	Until string `json:"until"`
}

// Checkin presses the account's check-in and normalises the answer.
func (in *Instance) Checkin(provider string) (*CheckinResult, error) {
	arg, err := json.Marshal(map[string]any{"provider": provider})
	if err != nil {
		return nil, err
	}
	answer, err := in.hook("checkin", string(arg))
	if err != nil {
		return nil, err
	}
	raw, ok := answer.(map[string]any)
	if !ok {
		return nil, errors.New("magpieplugin: the check-in answered nothing")
	}
	declared := usageText(raw["outcome"])
	result := &CheckinResult{
		Outcome: CheckinOutcome(declared),
		Credit:  usageText(raw["credit"]),
		Streak:  numberValue(raw["streak"]),
		Message: usageText(raw["message"]),
		Until:   usageTime(raw["until"]),
	}
	switch result.Outcome {
	case CheckinClaimed, CheckinDone, CheckinIneligible, CheckinInactive, CheckinCaptcha, CheckinFailed:
		return result, nil
	}
	// An outcome outside the set is a failure, and the reason says what the
	// plugin answered so its wording is not mistaken for the host's.
	result.Outcome = CheckinFailed
	reason := fmt.Sprintf("the package answered %q", declared)
	if result.Message != "" {
		reason += ":" + result.Message
	}
	result.Message = reason
	return result, nil
}

// CheckinJSON is Checkin as JSON, for a client that speaks JSON.
func (in *Instance) CheckinJSON(provider string) (string, error) {
	result, err := in.Checkin(provider)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
