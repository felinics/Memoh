package message

import "testing"

func TestResolveContextObservation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime string
		usage   string
		context string
		want    ContextObservation
	}{
		{"native", "model", `{"inputTokens":2500}`, "", ContextObservation{Basis: ContextBasisProviderInput, Known: true, UsedTokens: 2500}},
		{"runtime", "codex", `{"inputTokens":9000}`, `{"used_tokens":1810,"context_window":258400,"source":"codex_last_request"}`, ContextObservation{Basis: ContextBasisRuntime, Known: true, UsedTokens: 1810, WindowTokens: 258400, Source: "codex_last_request"}},
		{"runtime zero", "acp_agent", "", `{"used_tokens":0}`, ContextObservation{Basis: ContextBasisRuntime, Known: true}},
		{"runtime without measurement", "codex", `{"inputTokens":9000}`, "", ContextObservation{Basis: ContextBasisRuntime}},
		{"stale", "claude-code", "", `{"used_tokens":1810,"context_window":200000,"source":"claude_code_request","stale":"compact"}`, ContextObservation{Basis: ContextBasisRuntime}},
		{"malformed", "codex", "", `{"used_tokens":"many"}`, ContextObservation{Basis: ContextBasisRuntime}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveContextObservation(tc.runtime, []byte(tc.usage), []byte(tc.context)); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
