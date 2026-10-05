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

// NoContextObservation is the state of a session without any context row.
func NoContextObservation(runtimeType string) ContextObservation {
	if isNativeRuntime(runtimeType) {
		return ContextObservation{Basis: ContextBasisProviderInput, Known: true}
	}
	return ContextObservation{Basis: ContextBasisRuntime}
}

// ResolveContextObservation interprets the newest context state row. A
// runtime row without a current measurement, including one written before
// rounds recorded context_usage, is unknown rather than its turn total.
func ResolveContextObservation(runtimeType string, usage, contextUsage []byte) ContextObservation {
	if isNativeRuntime(runtimeType) {
		var native struct {
			InputTokens int64 `json:"inputTokens"`
		}
		_ = json.Unmarshal(usage, &native)
		return ContextObservation{Basis: ContextBasisProviderInput, Known: true, UsedTokens: native.InputTokens}
	}
	var state struct {
		UsedTokens    *int64 `json:"used_tokens"`
		ContextWindow int64  `json:"context_window"`
		Source        string `json:"source"`
		Stale         string `json:"stale"`
	}
	observation := ContextObservation{Basis: ContextBasisRuntime}
	if len(contextUsage) == 0 || json.Unmarshal(contextUsage, &state) != nil {
		return observation
	}
	if state.UsedTokens == nil || *state.UsedTokens < 0 || state.Stale != "" {
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
