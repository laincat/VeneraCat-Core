package magpieplugin

import "github.com/laincat/veneracat-core/aiapi"

// FinishReason is the normalized outcome shared with standard AI protocols.
type FinishReason = aiapi.FinishReason

const (
	FinishStop          = aiapi.FinishStop
	FinishLength        = aiapi.FinishLength
	FinishToolCalls     = aiapi.FinishToolCalls
	FinishContentFilter = aiapi.FinishContentFilter
	FinishCancelled     = aiapi.FinishCancelled
	FinishError         = aiapi.FinishError
	FinishInProgress    = aiapi.FinishInProgress
)

// NormalizeFinish maps a dialect's raw ending to the shared outcome.
func NormalizeFinish(dialect Dialect, raw string) FinishReason {
	return aiapi.NormalizeFinish(aiapi.Protocol(dialect), raw)
}
