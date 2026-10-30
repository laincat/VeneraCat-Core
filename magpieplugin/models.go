// The models a provider offers, asked for rather than assumed.
//
// Magpie keeps a catalogue of model names per provider, because its gateway
// wants context sizes up front. A client only needs what it can actually pick,
// so this asks the vendor that already knows: an OpenAI-compatible `/models`
// answer. A provider that does not answer gets a plain "it does not say", never
// a list invented here.
package magpieplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Model is one model a client may choose.
type Model struct {
	ID string `json:"id"`
	// Name is what to show; the vendor's own display name when it gave one.
	Name string `json:"name"`
	// Context and Output are token limits, 0 when the vendor did not say.
	Context int64 `json:"context"`
	Output  int64 `json:"output"`
	// Modalities lists what the model accepts, when the vendor said.
	Modalities []string `json:"modalities"`
	// Vendor is the raw object's owner field, kept for diagnostics.
	Vendor string `json:"vendor"`
}

// Models asks the package's vendor what it serves.
func (in *Instance) Models(ctx context.Context) ([]Model, error) {
	// The package's loader prepares the request shape and the sign-in.
	if _, err := in.hook("chatPrepare", "{}"); err != nil {
		return nil, err
	}
	answer, err := in.hook("modelsRun", "{}")
	if err != nil {
		return nil, err
	}
	result, ok := answer.(map[string]any)
	if !ok {
		return nil, errors.New("magpieplugin: the vendor answered no model list")
	}
	if okFlag, isBool := result["ok"].(bool); isBool && !okFlag {
		status, _ := result["status"].(int64)
		body, _ := result["body"].(string)
		return nil, fmt.Errorf("magpieplugin: the vendor does not list models (HTTP %d): %s", status, body)
	}
	return parseModels(result["body"]), nil
}

// parseModels accepts the two shapes OpenAI-compatible vendors use: {"data":
// [...]} and {"models": [...]}.
func parseModels(raw any) []Model {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	items, _ := object["data"].([]any)
	if items == nil {
		items, _ = object["models"].([]any)
	}
	out := make([]Model, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := usageText(entry["id"])
		if id == "" {
			id = usageText(entry["name"])
		}
		if id == "" {
			continue
		}
		name := usageText(entry["display_name"])
		if name == "" {
			name = usageText(entry["name"])
		}
		model := Model{
			ID:         id,
			Name:       name,
			Context:    int64(modelNumber(entry, "context_window", "context_length", "max_context_window_tokens")),
			Output:     int64(modelNumber(entry, "max_output_tokens", "output_tokens")),
			Modalities: modelStrings(entry["modalities"]),
			Vendor:     usageText(entry["owned_by"]),
		}
		if name == "" {
			model.Name = id
		}
		if model.Modalities == nil {
			if capabilities, ok := entry["capabilities"].(map[string]any); ok {
				if accepts, ok := capabilities["type"].(string); ok && accepts != "" {
					model.Modalities = []string{accepts}
				}
			}
		}
		out = append(out, model)
	}
	return out
}

func modelNumber(entry map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := entry[key]; ok {
			if n := numberValue(value); n > 0 {
				return n
			}
		}
	}
	return 0
}

func modelStrings(value any) []string {
	switch v := value.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

// ModelsJSON is Models as JSON, for a client that speaks JSON.
func (in *Instance) ModelsJSON(ctx context.Context) (string, error) {
	models, err := in.Models(ctx)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(models)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
