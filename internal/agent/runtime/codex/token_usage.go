package codex

import (
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

const contextSourceLastRequest = "codex_last_request"

// tokenUsageKind classifies one thread/tokenUsage/updated notification.
// Codex emits it for each model request (append_last_usage), and also to
// re-report its current state, to replay restored usage after thread/resume or
// thread/fork, and with synthetic counts (a post-compaction estimate, a
// full-window fill) that carry only totalTokens.
type tokenUsageKind int

const (
	tokenUsageRequest tokenUsageKind = iota
	tokenUsageRepeat
	tokenUsageReplay
	tokenUsageSynthetic
)

func syntheticTokenUsage(last protocol.TokenUsageBreakdown) bool {
	return last.InputTokens == 0 && last.CachedInputTokens == 0 && cacheWriteTokens(last) == 0 &&
		last.OutputTokens == 0 && last.ReasoningOutputTokens == 0
}

func cacheWriteTokens(b protocol.TokenUsageBreakdown) int64 {
	if b.CacheWriteInputTokens == nil {
		return 0
	}
	return *b.CacheWriteInputTokens
}

func sameTokenUsage(a, b protocol.TokenUsageBreakdown) bool {
	return a.InputTokens == b.InputTokens && a.CachedInputTokens == b.CachedInputTokens &&
		cacheWriteTokens(a) == cacheWriteTokens(b) && a.OutputTokens == b.OutputTokens &&
		a.ReasoningOutputTokens == b.ReasoningOutputTokens && a.TotalTokens == b.TotalTokens
}

// classifyTokenUsage runs on the read loop, in wire order. The cursor is the
// last thread total seen from a request or a replay, kept per native thread
// and only in this process: a resumed or forked thread is replayed before its
// next turn starts, and a thread nobody replayed has no usage behind it.
func (s *appServer) classifyTokenUsage(n *protocol.ThreadTokenUsageUpdatedNotification) tokenUsageKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	usage := n.TokenUsage
	if syntheticTokenUsage(usage.Last) {
		return tokenUsageSynthetic
	}
	previous, known := s.usageCursors[n.ThreadID]
	if known && sameTokenUsage(previous, usage.Total) {
		return tokenUsageRepeat
	}
	if s.usageCursors == nil {
		s.usageCursors = map[string]protocol.TokenUsageBreakdown{}
	}
	s.usageCursors[n.ThreadID] = usage.Total
	if !known && (n.TurnID == "" || n.TurnID != s.activeTurns[n.ThreadID]) {
		return tokenUsageReplay
	}
	return tokenUsageRequest
}

// requestTotals sums the model requests of one Memoh turn.
type requestTotals struct {
	input, cached, cacheWrite, output, reasoning int
}

func (r *requestTotals) add(last protocol.TokenUsageBreakdown) {
	r.input += int(last.InputTokens)
	r.cached += int(last.CachedInputTokens)
	r.cacheWrite += int(cacheWriteTokens(last))
	r.output += int(last.OutputTokens)
	r.reasoning += int(last.ReasoningOutputTokens)
}

// sdkUsage maps Responses-style counts, where cached and cache-write tokens
// are parts of inputTokens, onto the shared partition.
func (r *requestTotals) sdkUsage() *sdk.Usage {
	input := max(r.input, r.cached+r.cacheWrite)
	return &sdk.Usage{
		InputTokens:       input,
		OutputTokens:      r.output,
		TotalTokens:       input + r.output,
		ReasoningTokens:   r.reasoning,
		CachedInputTokens: r.cached,
		InputTokenDetails: sdk.InputTokenDetail{
			NoCacheTokens:    input - r.cached - r.cacheWrite,
			CacheReadTokens:  r.cached,
			CacheWriteTokens: r.cacheWrite,
		},
		OutputTokenDetails: sdk.OutputTokenDetail{ReasoningTokens: r.reasoning},
	}
}

// observeTokenUsage applies one classified notification to the turn: only a
// request counts and measures the context; a synthetic count means the
// context changed without a request measuring it.
func (t *turnState) observeTokenUsage(n *protocol.ThreadTokenUsageUpdatedNotification, kind tokenUsageKind) {
	if kind == tokenUsageReplay || !t.acceptsTurn(n.TurnID) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch kind {
	case tokenUsageRequest:
		if t.usage == nil {
			t.usage = &requestTotals{}
		}
		last := n.TokenUsage.Last
		t.usage.add(last)
		observed := &external.ContextUsage{UsedTokens: int(last.TotalTokens), Source: contextSourceLastRequest}
		if window := n.TokenUsage.ModelContextWindow; window != nil {
			observed.WindowTokens = int(*window)
		}
		t.context = observed
		totals := n.TokenUsage.Total
		t.threadTotals = &totals
	case tokenUsageRepeat:
		totals := n.TokenUsage.Total
		t.threadTotals = &totals
	case tokenUsageSynthetic:
		t.context = nil
	}
}
