package anibaka

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type pathStep struct {
	key      string
	index    int
	hasIndex bool
	wildcard bool
}

func parseJSONPath(expr string) ([]pathStep, error) {
	s := strings.TrimSpace(expr)
	if s == "" || s == "$" {
		return nil, nil
	}
	if strings.HasPrefix(s, "$") {
		s = s[1:]
	}
	var steps []pathStep
	for len(s) > 0 {
		if s[0] == '.' {
			s = s[1:]
			if s == "" {
				return nil, fmt.Errorf("invalid JSON path %q: trailing dot", expr)
			}
			if s[0] == '*' {
				steps = append(steps, pathStep{wildcard: true})
				s = s[1:]
				continue
			}
			end := strings.IndexAny(s, ".[")
			var key string
			if end < 0 {
				key, s = s, ""
			} else {
				key, s = s[:end], s[end:]
			}
			if key == "" {
				return nil, fmt.Errorf("invalid JSON path %q: empty key", expr)
			}
			steps = append(steps, pathStep{key: key})
			continue
		}
		if s[0] == '[' {
			closeIdx := strings.IndexByte(s, ']')
			if closeIdx < 0 {
				return nil, fmt.Errorf("invalid JSON path %q: unclosed '['", expr)
			}
			inner := strings.TrimSpace(s[1:closeIdx])
			s = s[closeIdx+1:]
			if inner == "*" {
				steps = append(steps, pathStep{wildcard: true})
				continue
			}
			if len(inner) >= 2 && ((inner[0] == '\'' && inner[len(inner)-1] == '\'') || (inner[0] == '"' && inner[len(inner)-1] == '"')) {
				steps = append(steps, pathStep{key: inner[1 : len(inner)-1]})
				continue
			}
			idx, err := strconv.Atoi(inner)
			if err != nil {
				return nil, fmt.Errorf("invalid JSON path %q: bad index %q", expr, inner)
			}
			steps = append(steps, pathStep{index: idx, hasIndex: true})
			continue
		}
		// Allow leading bare field name without leading "$."
		end := strings.IndexAny(s, ".[")
		var key string
		if end < 0 {
			key, s = s, ""
		} else {
			key, s = s[:end], s[end:]
		}
		steps = append(steps, pathStep{key: key})
	}
	return steps, nil
}

func decodeJSONValue(raw any) (any, error) {
	switch v := raw.(type) {
	case string:
		dec := json.NewDecoder(bytes.NewReader([]byte(v)))
		dec.UseNumber()
		var out any
		if err := dec.Decode(&out); err != nil {
			return nil, fmt.Errorf("anibaka: parse JSON: %w", err)
		}
		return out, nil
	case []byte:
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		var out any
		if err := dec.Decode(&out); err != nil {
			return nil, fmt.Errorf("anibaka: parse JSON: %w", err)
		}
		return out, nil
	default:
		return raw, nil
	}
}

func evalJSONPath(root any, expr string) ([]any, error) {
	steps, err := parseJSONPath(expr)
	if err != nil {
		return nil, err
	}
	cur := []any{root}
	for _, st := range steps {
		next := make([]any, 0, len(cur))
		for _, item := range cur {
			switch {
			case st.wildcard:
				switch x := item.(type) {
				case []any:
					next = append(next, x...)
				case map[string]any:
					for _, v := range x {
						next = append(next, v)
					}
				}
			case st.hasIndex:
				if arr, ok := item.([]any); ok {
					idx := st.index
					if idx < 0 {
						idx = len(arr) + idx
					}
					if idx >= 0 && idx < len(arr) {
						next = append(next, arr[idx])
					}
				}
			default:
				if obj, ok := item.(map[string]any); ok {
					if v, exists := obj[st.key]; exists {
						next = append(next, v)
					}
				}
			}
		}
		cur = next
		if len(cur) == 0 {
			break
		}
	}
	return cur, nil
}
