// Package tokenest is the dependency-free anchor of the token-estimation
// authority defined by internal/agent/context/fragment (contextfrag's
// EstimateBytesPerToken, the shared ledger heuristic from the unified
// context-budget work). It exists because the architecture guards
// (internal/arch: TestTimelineOnlyDependsOnTurnPort,
// TestChannelAgentDependenciesStayOnPorts) forbid chat/timeline and channel
// packages from importing agent/context — yet CM-EST-001
// (docs/design/context-memory-scheduling.md) requires their estimators to
// agree with the fragment ledger. contextfrag aliases its constant to
// BytesPerToken, so both sides swap together when a real tokenizer lands;
// no package may keep a private bytes-per-token constant.
package tokenest

// BytesPerToken is the shared byte-per-token heuristic.
const BytesPerToken = 4

// DefaultAbsoluteCapTokens is the server-wide admission cap applied when no
// explicit `[agent] context_absolute_max_tokens` is configured. It bounds
// context materialization even when a model has no configured context window
// (CM-ADM-001); models with larger windows need the cap raised explicitly.
const DefaultAbsoluteCapTokens = 200_000

// FromBytes converts a byte count to the shared token estimate.
func FromBytes(n int) int {
	if n <= 0 {
		return 0
	}
	return n / BytesPerToken
}

// FromString estimates tokens for a string's UTF-8 bytes.
func FromString(s string) int {
	return FromBytes(len(s))
}
