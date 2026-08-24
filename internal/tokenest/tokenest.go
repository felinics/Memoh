// Package tokenest is the single token-estimation authority shared by every
// subsystem that sizes context before a provider call: timeline composition,
// discuss/pipeline admission, compaction triggers, and fragment budget
// ledgers (docs/design/context-memory-scheduling.md, CM-EST-001). It is the
// one swap point for a real tokenizer; no package may keep a private
// bytes-per-token constant.
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
