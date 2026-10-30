package magpieplugin

// FinishReason is why a reply ended, in this core's vocabulary.
//
// The three dialects spell the same endings differently: Chat says "stop",
// Responses says "completed", Anthropic says "end_turn"; the token limit is
// "length", "incomplete" and "max_tokens". A caller that wants to know whether a
// reply was cut short should not have to carry three tables, so the raw string is
// kept next to a normalised one.
type FinishReason string

const (
	// FinishStop means the model finished what it was saying.
	FinishStop FinishReason = "stop"
	// FinishLength means the token limit cut it short.
	FinishLength FinishReason = "length"
	// FinishToolCalls means it stopped to call a tool.
	FinishToolCalls FinishReason = "tool_calls"
	// FinishContentFilter means the vendor suppressed the rest.
	FinishContentFilter FinishReason = "content_filter"
	// FinishCancelled means the caller or the vendor cancelled the run.
	FinishCancelled FinishReason = "cancelled"
	// FinishError means the run failed.
	FinishError FinishReason = "error"
	// FinishInProgress means the vendor reported a state that is not an ending.
	FinishInProgress FinishReason = "in_progress"
)

// NormalizeFinish turns one dialect's ending into this vocabulary.
//
// An ending nobody has seen before becomes `FinishReason(raw)` rather than
// FinishStop: calling an unknown outcome "finished" is the one answer that is
// certainly wrong, and a new vendor word should read as itself in a log.
func NormalizeFinish(dialect Dialect, raw string) FinishReason {
	if raw == "" {
		return ""
	}
	switch dialect {
	case DialectChat:
		switch raw {
		case "stop":
			return FinishStop
		case "length":
			return FinishLength
		case "tool_calls", "function_call":
			return FinishToolCalls
		case "content_filter":
			return FinishContentFilter
		}
	case DialectResponses:
		switch raw {
		case "completed":
			return FinishStop
		// Responses calls a hit token limit "incomplete", with the reason in a
		// separate field this reader does not keep. Both endings are "it was cut
		// short", and that is what a caller acts on.
		case "incomplete":
			return FinishLength
		case "failed":
			return FinishError
		case "cancelled", "canceled":
			return FinishCancelled
		case "in_progress", "queued":
			return FinishInProgress
		}
	case DialectMessages:
		switch raw {
		case "end_turn", "stop_sequence":
			return FinishStop
		case "max_tokens":
			return FinishLength
		case "tool_use":
			return FinishToolCalls
		case "refusal":
			return FinishContentFilter
		// Anthropic's resumable server-side loop is not an ending: the reply is
		// incomplete and the vendor expects to continue it.
		case "pause_turn":
			return FinishLength
		}
	}
	return FinishReason(raw)
}

// Truncated says whether the reply was cut short, which is the question most
// callers actually have.
func (r FinishReason) Truncated() bool { return r == FinishLength }
