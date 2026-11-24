// Package anibaka implements a declarative three-stage (search / detail / play)
// video pipeline engine modeled after AniBaka's anx-rule/2 format.
//
// All HTTP requests flow strictly through host.Transport and all WebView
// sniffing flows through host.Sniffer or returns a video.Sniff signal to the
// client. Core never opens direct network connections.
package anibaka

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/antchfx/xpath"
	"github.com/laincat/veneracat-core/attribution"
	"github.com/laincat/veneracat-core/host"
)

const (
	// SchemaID is the required "$schema" value for AniBaka pipeline rules.
	SchemaID = "anx-rule/2"
	// Kind is the video.Info.Kind reported by AniBaka providers.
	Kind = "anibaka-rule"

	DefaultMaxSteps     = 500
	HardMaxSteps        = 5000
	MaxDepth            = 16
	DefaultMaxLoopItems = 1000
	HardMaxLoopItems    = 5000
	MaxBodyBytes        = 16 << 20
)

var (
	ErrInvalidRule       = errors.New("anibaka: invalid rule")
	ErrStepLimitExceeded = errors.New("anibaka: pipeline step limit exceeded")
	ErrLoopLimitExceeded = errors.New("anibaka: pipeline loop limit exceeded")
	ErrDepthExceeded     = errors.New("anibaka: pipeline nesting depth exceeded")
)

var (
	keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	keyCleanRe = regexp.MustCompile(`[^a-zA-Z0-9_]+`)
)

// Rule is a parsed and validated anx-rule/2 declarative video rule.
type Rule struct {
	Schema      string            `json:"$schema"`
	Key         string            `json:"key,omitempty"`
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	BaseURL     string            `json:"baseUrl"`
	UserAgent   string            `json:"userAgent,omitempty"`
	Referer     string            `json:"referer,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Cookies     *bool             `json:"cookies,omitempty"`
	HLS         *HLSConfig        `json:"hls,omitempty"`
	MaxSteps    int               `json:"maxSteps,omitempty"`
	MaxLoop     int               `json:"maxLoop,omitempty"`
	Search      Stage             `json:"search"`
	Detail      Stage             `json:"detail"`
	Play        Stage             `json:"play"`
	Author      json.RawMessage   `json:"author,omitempty"`
	Homepage    json.RawMessage   `json:"homepage,omitempty"`
	Attribution attribution.Info  `json:"-"`
}

// HLSConfig controls playlist rewriting and ad filtering for HLS streams.
type HLSConfig struct {
	FilterAds   bool   `json:"filterAds,omitempty"`
	StripPrefix bool   `json:"stripPrefix,omitempty"`
	MaxHeight   int    `json:"maxHeight,omitempty"`
	Mode        string `json:"mode,omitempty"` // "highest" | "maxHeight" | "all"
}

// Stage is one pipeline stage (search, detail or play).
type Stage struct {
	TimeoutMs int         `json:"timeoutMs,omitempty"`
	Steps     []Step      `json:"steps"`
	Output    StageOutput `json:"output,omitempty"`
}

// StageOutput names variables or template expressions used to construct the
// stage's typed video result. Omitted fields fall back to well-known variable
// names or the final pipeline value.
type StageOutput struct {
	Items       string            `json:"items,omitempty"`
	Next        string            `json:"next,omitempty"`
	Title       string            `json:"title,omitempty"`
	Cover       string            `json:"cover,omitempty"`
	Description string            `json:"description,omitempty"`
	Tags        string            `json:"tags,omitempty"`
	Lines       string            `json:"lines,omitempty"`
	URL         string            `json:"url,omitempty"`
	Format      string            `json:"format,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Variants    string            `json:"variants,omitempty"`
	FilterAds   *bool             `json:"filterAds,omitempty"`
	Sniff       *SniffConfig      `json:"sniff,omitempty"`
}

// SniffConfig describes a WebView sniffing fallback for the play stage.
type SniffConfig struct {
	Page          string `json:"page,omitempty"`
	Match         string `json:"match,omitempty"`
	FollowIframes bool   `json:"followIframes,omitempty"`
	TimeoutMs     int    `json:"timeoutMs,omitempty"`
	Inline        bool   `json:"inline,omitempty"`
}

// SelectField extracts one named field relative to a matched HTML node.
type SelectField struct {
	Mode     string `json:"mode,omitempty"` // "css" | "xpath" (inherits parent step mode when empty)
	Selector string `json:"selector"`
	Attr     string `json:"attr,omitempty"` // "", "text", "html", "outerHtml", "attr:<name>", or attribute name
	All      bool   `json:"all,omitempty"`
	Default  string `json:"default,omitempty"`
}

// Step is one declarative operation in a stage pipeline.
type Step struct {
	Op       string `json:"op"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	KeepPipe bool   `json:"keepPipe,omitempty"`

	// Value / template / string ops.
	Value     any    `json:"value,omitempty"`
	Template  string `json:"template,omitempty"`
	Old       string `json:"old,omitempty"`
	New       string `json:"new,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Suffix    string `json:"suffix,omitempty"`
	Separator string `json:"separator,omitempty"`
	Base      string `json:"base,omitempty"`
	Start     int    `json:"start,omitempty"`
	End       int    `json:"end,omitempty"`
	HasEnd    bool   `json:"hasEnd,omitempty"`
	Index     int    `json:"index,omitempty"`

	// Fetch op.
	Method       string            `json:"method,omitempty"`
	URL          string            `json:"url,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Query        map[string]string `json:"query,omitempty"`
	Body         string            `json:"body,omitempty"`
	BodyType     string            `json:"bodyType,omitempty"` // "none" | "raw" | "json" | "form"
	Cookies      *bool             `json:"cookies,omitempty"`
	Charset      string            `json:"charset,omitempty"`
	SaveStatus   string            `json:"saveStatus,omitempty"`
	SaveHeaders  string            `json:"saveHeaders,omitempty"`
	SaveFinalURL string            `json:"saveFinalUrl,omitempty"`

	// Select (CSS / XPath) op.
	Mode     string                 `json:"mode,omitempty"` // "css" | "xpath"
	Selector string                 `json:"selector,omitempty"`
	Attr     string                 `json:"attr,omitempty"`
	All      bool                   `json:"all,omitempty"`
	Fields   map[string]SelectField `json:"fields,omitempty"`

	// Regex op.
	Pattern string  `json:"pattern,omitempty"`
	Group   int     `json:"group,omitempty"`
	Replace *string `json:"replace,omitempty"`

	// JSON op.
	Path       string            `json:"path,omitempty"`
	JSONFields map[string]string `json:"jsonFields,omitempty"`

	// Crypto op.
	Algorithm      string `json:"algorithm,omitempty"`
	Key            string `json:"key,omitempty"`
	IV             string `json:"iv,omitempty"`
	KeyEncoding    string `json:"keyEncoding,omitempty"`
	IVEncoding     string `json:"ivEncoding,omitempty"`
	InputEncoding  string `json:"inputEncoding,omitempty"`
	OutputEncoding string `json:"outputEncoding,omitempty"`
	Padding        string `json:"padding,omitempty"`

	// Control flow ops (branch / foreach / filter).
	Condition string `json:"condition,omitempty"` // "empty" | "notEmpty" | "contains" | "equals" | "regex"
	Target    string `json:"target,omitempty"`
	Then      []Step `json:"then,omitempty"`
	Else      []Step `json:"else,omitempty"`
	Steps     []Step `json:"steps,omitempty"`
	ItemVar   string `json:"itemVar,omitempty"`
	IndexVar  string `json:"indexVar,omitempty"`

	// Sniff op.
	Page          string `json:"page,omitempty"`
	Match         string `json:"match,omitempty"`
	FollowIframes bool   `json:"followIframes,omitempty"`
	TimeoutMs     int    `json:"timeoutMs,omitempty"`
	Inline        bool   `json:"inline,omitempty"`
	WhenEmpty     bool   `json:"whenEmpty,omitempty"`

	// HLS op.
	BaseURL       string `json:"baseUrl,omitempty"`
	DropAds       bool   `json:"dropAds,omitempty"`
	StripPrefix   bool   `json:"stripPrefix,omitempty"`
	MaxHeight     int    `json:"maxHeight,omitempty"`
	SelectVariant string `json:"selectVariant,omitempty"` // "highest" | "maxHeight" | "all"
	OutputMode    string `json:"outputMode,omitempty"`    // "playlist" | "url"
}

// Parse decodes and validates an anx-rule/2 JSON document.
func Parse(data []byte) (*Rule, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: rule is empty", ErrInvalidRule)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var r Rule
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %w", ErrInvalidRule, err)
	}
	r.Attribution, _ = attribution.ParseJSON(r.Author, r.Homepage)
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// ResolvedKey returns the provider key matching ^[a-zA-Z0-9_]+$.
func (r *Rule) ResolvedKey() string {
	if r.Key != "" {
		return r.Key
	}
	clean := strings.Trim(keyCleanRe.ReplaceAllString(r.Name, "_"), "_")
	if clean == "" {
		clean = "rule"
	}
	return "anx_" + clean
}

// Validate checks schema, metadata, URLs, headers, selectors, regexes, crypto
// settings and step nesting bounds so broken rules fail at install time.
func (r *Rule) Validate() error {
	if r.Schema != SchemaID {
		return fmt.Errorf("%w: $schema must be %q, got %q", ErrInvalidRule, SchemaID, r.Schema)
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidRule)
	}
	if strings.TrimSpace(r.Version) == "" {
		return fmt.Errorf("%w: version is required", ErrInvalidRule)
	}
	if r.Key != "" && !keyPattern.MatchString(r.Key) {
		return fmt.Errorf("%w: key %q must match %s", ErrInvalidRule, r.Key, keyPattern)
	}
	if err := host.ValidateURL(r.BaseURL); err != nil {
		return fmt.Errorf("%w: baseUrl: %w", ErrInvalidRule, err)
	}
	if r.Referer != "" {
		if err := host.ValidateURL(r.Referer); err != nil {
			return fmt.Errorf("%w: referer: %w", ErrInvalidRule, err)
		}
	}
	if r.UserAgent != "" {
		if err := host.ValidHeader("User-Agent", r.UserAgent); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRule, err)
		}
	}
	for k, v := range r.Headers {
		if err := host.ValidHeader(k, v); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRule, err)
		}
	}
	if r.MaxSteps < 0 || r.MaxSteps > HardMaxSteps {
		return fmt.Errorf("%w: maxSteps must be between 0 and %d", ErrInvalidRule, HardMaxSteps)
	}
	if r.MaxLoop < 0 || r.MaxLoop > HardMaxLoopItems {
		return fmt.Errorf("%w: maxLoop must be between 0 and %d", ErrInvalidRule, HardMaxLoopItems)
	}
	if err := validateStage("search", &r.Search); err != nil {
		return err
	}
	if err := validateStage("detail", &r.Detail); err != nil {
		return err
	}
	if err := validateStage("play", &r.Play); err != nil {
		return err
	}
	return nil
}

func validateStage(name string, s *Stage) error {
	if len(s.Steps) == 0 && (name != "play" || s.Output.Sniff == nil) {
		return fmt.Errorf("%w: stage %q must declare at least one step", ErrInvalidRule, name)
	}
	if s.TimeoutMs < 0 {
		return fmt.Errorf("%w: stage %q timeoutMs must not be negative", ErrInvalidRule, name)
	}
	for k, v := range s.Output.Headers {
		if err := host.ValidHeader(k, v); err != nil {
			return fmt.Errorf("%w: stage %q output header: %w", ErrInvalidRule, name, err)
		}
	}
	if s.Output.Sniff != nil && s.Output.Sniff.Match != "" {
		if _, err := regexp.Compile(s.Output.Sniff.Match); err != nil {
			return fmt.Errorf("%w: stage %q sniff match: %w", ErrInvalidRule, name, err)
		}
	}
	return validateSteps(name, s.Steps, 1)
}

func validateSteps(stage string, steps []Step, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("%w: stage %q exceeds max depth %d", ErrDepthExceeded, stage, MaxDepth)
	}
	for i := range steps {
		st := &steps[i]
		where := fmt.Sprintf("stage %q step %d (%s)", stage, i, st.Op)
		op := strings.ToLower(strings.TrimSpace(st.Op))
		switch op {
		case "const", "set", "get", "default", "pick", "template",
			"trim", "lower", "upper", "replace", "replaceall",
			"prefix", "suffix", "substring", "split", "join",
			"first", "last", "index", "slice", "flatten", "dedupe", "reverse",
			"urlresolve", "urlencode", "urldecode", "htmlunescape",
			"jsonstringify":
		case "fetch":
			m := strings.ToUpper(strings.TrimSpace(st.Method))
			if m != "" && m != "GET" && m != "POST" && m != "PUT" && m != "HEAD" {
				return fmt.Errorf("%w: %s: unsupported HTTP method %q", ErrInvalidRule, where, st.Method)
			}
			for k, v := range st.Headers {
				if err := host.ValidHeader(k, v); err != nil {
					return fmt.Errorf("%w: %s: %w", ErrInvalidRule, where, err)
				}
			}
			switch strings.ToLower(strings.TrimSpace(st.BodyType)) {
			case "", "none", "raw", "json", "form":
			default:
				return fmt.Errorf("%w: %s: unsupported bodyType %q", ErrInvalidRule, where, st.BodyType)
			}
		case "select", "css", "xpath":
			mode := strings.ToLower(strings.TrimSpace(st.Mode))
			if op == "css" {
				mode = "css"
			} else if op == "xpath" {
				mode = "xpath"
			}
			if mode == "" {
				mode = "css"
			}
			if mode != "css" && mode != "xpath" {
				return fmt.Errorf("%w: %s: unsupported select mode %q", ErrInvalidRule, where, st.Mode)
			}
			if strings.TrimSpace(st.Selector) == "" {
				return fmt.Errorf("%w: %s: selector is required", ErrInvalidRule, where)
			}
			if err := validateSelector(mode, st.Selector); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrInvalidRule, where, err)
			}
			for fname, sf := range st.Fields {
				fmode := strings.ToLower(strings.TrimSpace(sf.Mode))
				if fmode == "" {
					fmode = mode
				}
				if fmode != "css" && fmode != "xpath" {
					return fmt.Errorf("%w: %s: field %q unsupported mode %q", ErrInvalidRule, where, fname, sf.Mode)
				}
				if strings.TrimSpace(sf.Selector) != "" {
					if err := validateSelector(fmode, sf.Selector); err != nil {
						return fmt.Errorf("%w: %s: field %q: %w", ErrInvalidRule, where, fname, err)
					}
				}
			}
		case "regex", "regexall", "regexreplace":
			if st.Pattern == "" {
				return fmt.Errorf("%w: %s: pattern is required", ErrInvalidRule, where)
			}
			if _, err := regexp.Compile(st.Pattern); err != nil {
				return fmt.Errorf("%w: %s: invalid regex: %w", ErrInvalidRule, where, err)
			}
			if st.Group < 0 {
				return fmt.Errorf("%w: %s: group must not be negative", ErrInvalidRule, where)
			}
		case "json", "jsonpath":
			if st.Path != "" {
				if _, err := parseJSONPath(st.Path); err != nil {
					return fmt.Errorf("%w: %s: %w", ErrInvalidRule, where, err)
				}
			}
			for fname, fpath := range st.JSONFields {
				if fpath == "" {
					continue
				}
				if _, err := parseJSONPath(fpath); err != nil {
					return fmt.Errorf("%w: %s: jsonFields[%q]: %w", ErrInvalidRule, where, fname, err)
				}
			}
		case "crypto":
			if !validCryptoAlgorithm(st.Algorithm) {
				return fmt.Errorf("%w: %s: unsupported crypto algorithm %q", ErrInvalidRule, where, st.Algorithm)
			}
		case "branch", "if":
			if err := validateCondition(st.Condition, st.Target); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrInvalidRule, where, err)
			}
			if err := validateSteps(stage, st.Then, depth+1); err != nil {
				return err
			}
			if err := validateSteps(stage, st.Else, depth+1); err != nil {
				return err
			}
		case "map", "foreach":
			if len(st.Steps) == 0 {
				return fmt.Errorf("%w: %s: nested steps are required", ErrInvalidRule, where)
			}
			if err := validateSteps(stage, st.Steps, depth+1); err != nil {
				return err
			}
		case "filter":
			if err := validateCondition(st.Condition, st.Target); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrInvalidRule, where, err)
			}
		case "sniff":
			if st.Match != "" {
				if _, err := regexp.Compile(st.Match); err != nil {
					return fmt.Errorf("%w: %s: invalid sniff match regex: %w", ErrInvalidRule, where, err)
				}
			}
		case "hls", "hlsrewrite":
			switch strings.ToLower(strings.TrimSpace(st.SelectVariant)) {
			case "", "highest", "maxheight", "all":
			default:
				return fmt.Errorf("%w: %s: unsupported selectVariant %q", ErrInvalidRule, where, st.SelectVariant)
			}
			switch strings.ToLower(strings.TrimSpace(st.OutputMode)) {
			case "", "playlist", "url":
			default:
				return fmt.Errorf("%w: %s: unsupported outputMode %q", ErrInvalidRule, where, st.OutputMode)
			}
		default:
			return fmt.Errorf("%w: %s: unknown op %q", ErrInvalidRule, where, st.Op)
		}
	}
	return nil
}

func validateSelector(mode, sel string) error {
	if mode == "css" {
		if _, err := cascadia.Compile(sel); err != nil {
			return fmt.Errorf("invalid CSS selector %q: %w", sel, err)
		}
		return nil
	}
	if _, err := xpath.Compile(sel); err != nil {
		return fmt.Errorf("invalid XPath %q: %w", sel, err)
	}
	return nil
}

func validateCondition(cond, target string) error {
	switch strings.ToLower(strings.TrimSpace(cond)) {
	case "empty", "notempty", "contains", "notcontains", "equals", "notequals", "startswith", "endswith":
		return nil
	case "regex":
		if target == "" {
			return errors.New("regex condition requires target")
		}
		if _, err := regexp.Compile(target); err != nil {
			return fmt.Errorf("invalid condition regex: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported condition %q", cond)
	}
}
