package message

import (
	"encoding/json"
	"strings"
)

// ContextUsageMetadataKey holds an External Agent round's context state on
// the round's last assistant message.
const ContextUsageMetadataKey = "context_usage"

// Context observation bases. Native rows report the latest provider request's
// input; External Agent rows report the runtime's own measure.
const (
	ContextBasisProviderInput = "provider_input"
	ContextBasisRuntime       = "runtime"
)

// ContextUsageMetadata is the context_usage value of a round that measured
// its context.
func ContextUsageMetadata(usedTokens, windowTokens int, source string) map[string]any {
	value := map[string]any{"used_tokens": usedTokens}
	if windowTokens > 0 {
		value["context_window"] = windowTokens
	}
	if source = strings.TrimSpace(source); source != "" {
		value["source"] = source
	}
	return value
}

// ContextObservation is the newest context state of a session. WindowTokens
// is zero when the observation carried no window.
type ContextObservation struct {
	Basis        string
	Known        bool
	UsedTokens   int64
	WindowTokens int64
	Source       string
}

// ResolveContextObservation interprets a session's newest context state row
// (rowRuntime is empty without one). The session's current runtime decides
// the basis; a row from another runtime measured another context. A runtime
// row without a current measurement, including one written before rounds
// recorded context_usage, is unknown rather than its turn total.
func ResolveContextObservation(sessionRuntime, rowRuntime string, usage, contextUsage []byte) ContextObservation {
	native := isNativeRuntime(sessionRuntime)
	if native {
		observation := ContextObservation{Basis: ContextBasisProviderInput, Known: true}
		if isNativeRuntime(rowRuntime) && strings.TrimSpace(rowRuntime) != "" {
			var input struct {
				InputTokens int64 `json:"inputTokens"`
			}
			_ = json.Unmarshal(usage, &input)
			observation.UsedTokens = input.InputTokens
		}
		return observation
	}
	observation := ContextObservation{Basis: ContextBasisRuntime}
	if strings.TrimSpace(rowRuntime) != strings.TrimSpace(sessionRuntime) || len(contextUsage) == 0 {
		return observation
	}
	var state struct {
		UsedTokens    *int64 `json:"used_tokens"`
		ContextWindow int64  `json:"context_window"`
		Source        string `json:"source"`
		Stale         string `json:"stale"`
	}
	if json.Unmarshal(contextUsage, &state) != nil || state.UsedTokens == nil || *state.UsedTokens < 0 || state.Stale != "" {
		return observation
	}
	observation.Known = true
	observation.Source = state.Source
	observation.UsedTokens = *state.UsedTokens
	observation.WindowTokens = max(state.ContextWindow, 0)
	return observation
}

func isNativeRuntime(runtimeType string) bool {
	runtimeType = strings.TrimSpace(runtimeType)
	return runtimeType == "" || runtimeType == "model"
}
