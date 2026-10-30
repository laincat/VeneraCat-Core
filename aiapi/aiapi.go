// Package aiapi builds the requests for the standard AI protocols that VeneraCat
// Core supports out of the box.
//
// The package only constructs requests. It opens no connection, stores no
// credential and makes no routing decision: the client performs the request
// through its Host, and any policy — which account to use, how to classify a
// request, what to mask — belongs to an extension plugin.
//
// Three protocols are covered:
//
//	chat       OpenAI Chat Completions, POST /v1/chat/completions
//	responses  OpenAI Responses,       POST /v1/responses
//	messages   Anthropic Messages,     POST /v1/messages
//
// A request body is the native JSON object of the protocol it targets. Core
// does not translate between protocols and does not flatten content: tools,
// images, reasoning and vendor extensions are passed through as written.
package aiapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/laincat/veneracat-core/host"
)

// Protocol names a standard AI API.
type Protocol string

// The supported protocols.
const (
	// Chat is OpenAI Chat Completions.
	Chat Protocol = "chat"
	// Responses is OpenAI Responses.
	Responses Protocol = "responses"
	// Messages is Anthropic Messages.
	Messages Protocol = "messages"
)

// Default API roots. A custom root replaces the whole root: the caller who
// writes https://gateway.example/openai means that path.
const (
	defaultOpenAIBase       = "https://api.openai.com/v1"
	defaultAnthropicBase    = "https://api.anthropic.com/v1"
	defaultAnthropicVersion = "2023-06-01"
)

// paths maps a protocol to the endpoint under the API root.
var paths = map[Protocol]string{
	Chat:      "/chat/completions",
	Responses: "/responses",
	Messages:  "/messages",
}

// Config describes where a request goes and how it authenticates.
type Config struct {
	// Protocol selects the API shape. It is required.
	Protocol Protocol
	// BaseURL is the API root. Empty means the vendor default for the
	// protocol. A custom root must be an absolute http or https URL without
	// credentials, query or fragment.
	BaseURL string
	// APIKey is the credential. It is required and never logged.
	APIKey string
	// AnthropicVersion overrides the Anthropic version header. Empty means
	// the default version. It is ignored by the other protocols.
	AnthropicVersion string
	// Partition is an opaque cookie partition name. It never enables
	// cookies: an AI request must not read a comic source's jar.
	Partition string
}

// BuildRequest builds a request for the configured protocol. The body is the
// protocol's own JSON object and is kept as written, so key order, exact
// numbers and unknown fields survive.
func BuildRequest(cfg Config, body json.RawMessage) (*host.Request, error) {
	path, err := pathFor(cfg.Protocol)
	if err != nil {
		return nil, err
	}
	target, err := endpoint(cfg, path)
	if err != nil {
		return nil, err
	}
	header, err := headers(cfg, true)
	if err != nil {
		return nil, err
	}
	if err := validateBody(cfg.Protocol, body); err != nil {
		return nil, err
	}
	return &host.Request{
		Method:    http.MethodPost,
		URL:       target,
		Header:    header,
		Body:      append([]byte(nil), body...),
		Cookies:   false,
		Partition: cfg.Partition,
	}, nil
}

// BuildModelsRequest asks the same root for its model list: GET, no body, the
// same credentials, so a caller that can talk to the API can also list it.
func BuildModelsRequest(cfg Config) (*host.Request, error) {
	if _, err := pathFor(cfg.Protocol); err != nil {
		return nil, err
	}
	target, err := endpoint(cfg, "/models")
	if err != nil {
		return nil, err
	}
	header, err := headers(cfg, false)
	if err != nil {
		return nil, err
	}
	return &host.Request{
		Method:    http.MethodGet,
		URL:       target,
		Header:    header,
		Cookies:   false,
		Partition: cfg.Partition,
	}, nil
}

func pathFor(protocol Protocol) (string, error) {
	switch protocol {
	case Chat, Responses, Messages:
		return paths[protocol], nil
	case "":
		return "", errors.New("aiapi: no protocol: say chat, responses or messages")
	default:
		return "", errors.New("aiapi: unknown protocol: expected chat, responses or messages")
	}
}

func baseFor(protocol Protocol) string {
	if protocol == Messages {
		return defaultAnthropicBase
	}
	return defaultOpenAIBase
}

// endpoint resolves the root and appends the path.
func endpoint(cfg Config, path string) (string, error) {
	base := cfg.BaseURL
	if base == "" {
		base = baseFor(cfg.Protocol)
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", errors.New("aiapi: the API root is not a URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("aiapi: the API root must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("aiapi: the API root has no host")
	}
	if parsed.User != nil {
		return "", errors.New("aiapi: the API root must not carry credentials")
	}
	if parsed.ForceQuery || strings.Contains(base, "#") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("aiapi: the API root must not carry a query or fragment")
	}
	return strings.TrimRight(base, "/") + path, nil
}

// headers builds the credential headers each vendor expects, and refuses a key
// that could split the request into two.
func headers(cfg Config, withBody bool) (http.Header, error) {
	key := cfg.APIKey
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("aiapi: no API key")
	}
	for i := 0; i < len(key); i++ {
		if c := key[i]; c < 0x20 || c == 0x7f {
			return nil, errors.New("aiapi: the API key contains a control character")
		}
	}
	header := http.Header{}
	if withBody {
		header.Set("Content-Type", "application/json")
	} else {
		header.Set("Accept", "application/json")
	}
	switch cfg.Protocol {
	case Messages:
		header.Set("x-api-key", key)
		version := cfg.AnthropicVersion
		if version == "" {
			version = defaultAnthropicVersion
		}
		if strings.TrimSpace(version) == "" || host.ValidHeader("anthropic-version", version) != nil {
			return nil, errors.New("aiapi: invalid Anthropic version header")
		}
		header.Set("anthropic-version", version)
	default:
		header.Set("Authorization", "Bearer "+key)
	}
	return header, nil
}

// validateBody checks the fields that decide what the provider answers, without
// touching the bytes that will be sent.
func validateBody(protocol Protocol, body json.RawMessage) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return errors.New("aiapi: the request body is empty")
	}
	if trimmed[0] != '{' {
		return errors.New("aiapi: the request body must be a JSON object")
	}
	if err := scanJSON(trimmed); err != nil {
		return err
	}

	// Protocol keys are case-sensitive; struct decoding would accept Model.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return errors.New("aiapi: the request body is not valid JSON")
	}
	for key := range fields {
		for _, reserved := range []string{"model", "messages", "input", "stream", "max_tokens"} {
			if key != reserved && strings.EqualFold(key, reserved) {
				return errors.New("aiapi: protocol fields require exact lowercase spelling")
			}
		}
	}

	var model string
	if json.Unmarshal(fields["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return errors.New("aiapi: the request body needs a model")
	}

	switch protocol {
	case Chat, Messages:
		if err := requireArray(fields["messages"], "messages"); err != nil {
			return err
		}
		if protocol == Messages {
			if err := requirePositiveInteger(fields["max_tokens"], "max_tokens"); err != nil {
				return err
			}
		}
	case Responses:
		if err := requireInput(fields["input"]); err != nil {
			return err
		}
	}

	if stream, present := fields["stream"]; present {
		switch string(bytes.TrimSpace(stream)) {
		case "true", "false":
		default:
			return errors.New("aiapi: stream must be true or false")
		}
	}
	return nil
}

func requireArray(raw json.RawMessage, name string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("aiapi: the request body needs a %s array", name)
	}
	if trimmed[0] != '[' {
		return fmt.Errorf("aiapi: %s must be an array", name)
	}
	return nil
}

// requireInput accepts what the Responses API accepts: a plain string, or the
// array of item objects a client builds for a conversation.
func requireInput(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return errors.New("aiapi: the request body needs input")
	}
	switch trimmed[0] {
	case '"', '[':
		return nil
	default:
		return errors.New("aiapi: input must be a string or an array")
	}
}

// requirePositiveInteger reads the number as text: converting it to float64
// would accept 1.5 and silently round a limit the vendor refuses.
func requirePositiveInteger(raw json.RawMessage, name string) error {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return fmt.Errorf("aiapi: the request body needs %s", name)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("aiapi: %s must be a positive whole number", name)
	}
	return nil
}

// scanJSON walks the body once: it rejects two values in one body, and a key
// repeated in the same object. A repeated key is read differently by different
// decoders, which is exactly the ambiguity a request must not carry.
func scanJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("aiapi: the request body holds more than one JSON value")
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("aiapi: the request body is not valid JSON: %w", err)
	}
	open, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch open {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("aiapi: the request body is not valid JSON: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("aiapi: the request body has a key that is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("aiapi: the request body repeats an object key")
			}
			seen[key] = struct{}{}
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("aiapi: the request body is not valid JSON: %w", err)
		}
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("aiapi: the request body is not valid JSON: %w", err)
		}
	}
	return nil
}
