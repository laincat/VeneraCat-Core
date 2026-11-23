package magpieplugin

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message is one turn of a conversation. Roles are "user", "assistant" and
// "system"; a vendor that has no system turn gets its text folded into the
// first user message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is one conversation turn to send.
type ChatRequest struct {
	Model    string
	Messages []Message
	// System is separate because Anthropic Messages takes it out of band.
	System string
	// MaxTokens caps the reply; vendors disagree on whether it is required,
	// so zero leaves it out.
	MaxTokens int
	// Extra holds vendor parameters (temperature and the like) copied
	// verbatim into the body.
	Extra map[string]json.RawMessage
}

// Body encodes the request in a dialect's own shape.
func (r *ChatRequest) Body(d Dialect) ([]byte, error) {
	switch d {
	case DialectChat, "":
		return r.bodyChat()
	case DialectResponses:
		return r.bodyResponses()
	case DialectMessages:
		return r.bodyMessages()
	}
	return nil, fmt.Errorf("magpieplugin: unknown dialect %q", d)
}

func (r *ChatRequest) bodyChat() ([]byte, error) {
	body := struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		Stream    bool      `json:"stream"`
		MaxTokens int       `json:"max_tokens,omitempty"`
	}{Model: r.Model, Messages: r.messagesWithSystem(), Stream: true, MaxTokens: r.MaxTokens}
	return r.encode(body)
}

func (r *ChatRequest) bodyResponses() ([]byte, error) {
	// Responses takes instructions out of band and calls the user's turns
	// "input"; a single string is enough for a conversation without tools.
	body := struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions,omitempty"`
		Input        string `json:"input"`
		Stream       bool   `json:"stream"`
	}{Model: r.Model, Instructions: r.System, Input: r.prompt(), Stream: true}
	return r.encode(body)
}

func (r *ChatRequest) bodyMessages() ([]byte, error) {
	body := struct {
		Model     string    `json:"model"`
		System    string    `json:"system,omitempty"`
		Messages  []Message `json:"messages"`
		Stream    bool      `json:"stream"`
		MaxTokens int       `json:"max_tokens"`
	}{Model: r.Model, System: r.System, Messages: r.userAndAssistant(), Stream: true, MaxTokens: r.maxTokens()}
	return r.encode(body)
}

// maxTokens is what Anthropic needs; without a cap the request is refused.
func (r *ChatRequest) maxTokens() int {
	if r.MaxTokens > 0 {
		return r.MaxTokens
	}
	return 4096
}

// messagesWithSystem folds a system turn in, for vendors that have one.
func (r *ChatRequest) messagesWithSystem() []Message {
	if r.System == "" {
		return r.userAndAssistant()
	}
	out := make([]Message, 0, len(r.Messages)+1)
	out = append(out, Message{Role: "system", Content: r.System})
	return append(out, r.userAndAssistant()...)
}

// userAndAssistant drops any system turn: it travels separately.
func (r *ChatRequest) userAndAssistant() []Message {
	out := make([]Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.Role == "system" {
			continue
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		out = append(out, Message{Role: role, Content: m.Content})
	}
	return out
}

// prompt flattens the conversation for an API that takes one input string.
func (r *ChatRequest) prompt() string {
	var b strings.Builder
	for _, m := range r.Messages {
		if m.Role == "system" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if m.Role == "assistant" {
			b.WriteString("Assistant: ")
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

// encode merges Extra into the encoded body without disturbing key order: the
// vendor's own keys stay where the struct put them.
func (r *ChatRequest) encode(body any) ([]byte, error) {
	base, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return base, nil
	}
	extra, err := json.Marshal(r.Extra)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, err
	}
	var add map[string]json.RawMessage
	if err := json.Unmarshal(extra, &add); err != nil {
		return nil, err
	}
	for k, v := range add {
		if _, exists := obj[k]; exists {
			continue
		}
		obj[k] = v
	}
	return json.Marshal(obj)
}
