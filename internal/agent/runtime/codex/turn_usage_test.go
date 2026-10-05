package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// codexWire feeds app-server stdout through the read loop's notification
// entry. A turn is registered when its turn/start response arrives unless the
// test registered it earlier; session metadata is merged turn to turn.
type codexWire struct {
	t        *testing.T
	srv      *appServer
	threadID string
	metadata map[string]any
	turn     *turnState
	results  []external.PromptResult
}

func newCodexWire(t *testing.T, metadata map[string]any) *codexWire {
	return &codexWire{t: t, srv: &appServer{logger: slog.Default(), turns: map[string]*turnState{}}, metadata: metadata}
}

func (w *codexWire) register(threadID string) {
	w.threadID = threadID
	input := external.PromptInput{RuntimeMetadata: w.metadata, Sink: external.EventSinkFunc(func(event.StreamEvent) {})}
	w.turn = newTurnState(w.t.Context(), input, threadID, nil, nil, nil, nil, slog.Default())
	w.srv.registerTurn(threadID, w.turn)
}

func (w *codexWire) feed(lines ...string) {
	w.t.Helper()
	for _, line := range lines {
		var response struct {
			Result struct {
				Thread *struct {
					ID string `json:"id"`
				} `json:"thread"`
				Turn *struct {
					ID string `json:"id"`
				} `json:"turn"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			w.t.Fatal(err)
		}
		if response.Result.Thread != nil {
			w.threadID = response.Result.Thread.ID
		}
		if response.Result.Turn != nil {
			if w.turn == nil {
				w.register(w.threadID)
			}
			w.turn.setTurnID(response.Result.Turn.ID)
		}
		inbound, err := protocol.DecodeInbound([]byte(line))
		if err != nil {
			w.t.Fatal(err)
		}
		if inbound.Kind != protocol.InboundNotification {
			continue
		}
		w.srv.HandleNotification(w.t.Context(), inbound)
		if inbound.Method == protocol.MethodTurnCompleted && w.turn != nil && !w.turn.followsGoal {
			w.finish()
		}
	}
}

func (w *codexWire) finish() {
	w.t.Helper()
	result, err := w.srv.turnResult(w.turn)
	w.srv.unregisterTurn(w.threadID, w.turn)
	w.turn.close()
	w.turn = nil
	if err != nil {
		w.t.Fatal(err)
	}
	w.results = append(w.results, result)
	merged := map[string]any{}
	for key, value := range w.metadata {
		merged[key] = value
	}
	for key, value := range result.RuntimeMetadata {
		merged[key] = value
	}
	w.metadata = merged
}

func (w *codexWire) feedFixture(path string) {
	w.t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // fixed testdata fixture path
	if err != nil {
		w.t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		w.feed(scanner.Text())
	}
}

type codexTurnUsage struct {
	usage       sdk.Usage
	context     *external.ContextUsage
	threadTotal int64
}

func (w *codexWire) assert(turns ...codexTurnUsage) {
	w.t.Helper()
	if len(w.results) != len(turns) {
		w.t.Fatalf("turns = %d, want %d", len(w.results), len(turns))
	}
	for i, want := range turns {
		got := w.results[i]
		if got.Usage == nil || *got.Usage != want.usage {
			w.t.Errorf("turn %d usage = %+v, want %+v", i+1, got.Usage, want.usage)
		}
		if (got.Context == nil) != (want.context == nil) || (want.context != nil && *got.Context != *want.context) {
			w.t.Errorf("turn %d context = %+v, want %+v", i+1, got.Context, want.context)
		}
		if want.threadTotal != 0 && got.RuntimeMetadata["codex_thread_total_tokens"] != want.threadTotal {
			w.t.Errorf("turn %d thread total = %v, want %d", i+1, got.RuntimeMetadata["codex_thread_total_tokens"], want.threadTotal)
		}
	}
}

func codexUsage(input, cached, output, reasoning int) sdk.Usage {
	u := sdk.Usage{
		InputTokens: input, OutputTokens: output, TotalTokens: input + output, ReasoningTokens: reasoning, CachedInputTokens: cached,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: input - cached, CacheReadTokens: cached},
	}
	u.OutputTokenDetails.ReasoningTokens = reasoning
	return u
}

func codexContext(used int) *external.ContextUsage {
	return &external.ContextUsage{UsedTokens: used, WindowTokens: 258400, Source: "codex_last_request"}
}

// The fixtures are codex app-server 0.160.0 stdout recorded against a local
// fake Responses endpoint. usage_autocompact.ndjson compacts before its second
// turn: Codex re-reports the previous request, then a post-compaction estimate;
// neither is a model request.
func TestCodexRecordedTurnsUsageAndContext(t *testing.T) {
	first := codexTurnUsage{usage: codexUsage(2700, 2100, 50, 15), context: codexContext(1520), threadTotal: 2750}
	t.Run("two turns", func(t *testing.T) {
		w := newCodexWire(t, nil)
		w.feedFixture("testdata/usage_two_turns.ndjson")
		w.assert(first, codexTurnUsage{usage: codexUsage(1800, 1400, 10, 0), context: codexContext(1810), threadTotal: 4560})
	})
	t.Run("auto compaction", func(t *testing.T) {
		w := newCodexWire(t, nil)
		w.feedFixture("testdata/usage_autocompact.ndjson")
		w.assert(first, codexTurnUsage{usage: codexUsage(3600, 2800, 20, 0), context: codexContext(1810), threadTotal: 6370})
	})
}

func codexBreakdown(input, cached, output, reasoning, total int) string {
	return fmt.Sprintf(`{"totalTokens":%d,"inputTokens":%d,"cachedInputTokens":%d,"cacheWriteInputTokens":0,"outputTokens":%d,"reasoningOutputTokens":%d}`, total, input, cached, output, reasoning)
}

func codexTokenUsage(thread, turn, last, total string) string {
	return fmt.Sprintf(`{"method":"thread/tokenUsage/updated","params":{"threadId":%q,"turnId":%q,"tokenUsage":{"total":%s,"last":%s,"modelContextWindow":258400}}}`, thread, turn, total, last)
}

func codexTurnEvent(method, thread, turn, status string) string {
	return fmt.Sprintf(`{"method":%q,"params":{"threadId":%q,"turn":{"id":%q,"items":[],"status":%q}}}`, method, thread, turn, status)
}

func TestCodexUsageFollowsThreadIdentity(t *testing.T) {
	req1810 := codexBreakdown(1800, 1400, 10, 0, 1810)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		lines    []string
		want     codexTurnUsage
	}{
		{
			// thread/fork and thread/resume replay the restored usage after their
			// response; the turn is already registered when the replay arrives.
			name:     "fork cut replay",
			metadata: map[string]any{metadataThreadIDKey: "source", "codex_thread_total_tokens": int64(6370)},
			lines: []string{
				codexTokenUsage("fork", "historical", codexBreakdown(1500, 1100, 20, 5, 1520), codexBreakdown(2700, 2100, 50, 15, 2750)),
				codexTurnEvent("turn/started", "fork", "t3", "inProgress"),
				codexTokenUsage("fork", "t3", req1810, codexBreakdown(4500, 3500, 60, 15, 4560)),
				codexTurnEvent("turn/completed", "fork", "t3", "completed"),
			},
			want: codexTurnUsage{usage: codexUsage(1800, 1400, 10, 0), context: codexContext(1810), threadTotal: 4560},
		},
		{
			// A fresh thread (ForceFreshRuntime, refused resume) starts from zero
			// whatever total the previous thread left in the session metadata.
			name:     "fresh thread after another thread",
			metadata: map[string]any{metadataThreadIDKey: "old", "codex_thread_total_tokens": int64(6370)},
			lines: []string{
				codexTurnEvent("turn/started", "fresh", "t1", "inProgress"),
				codexTokenUsage("fresh", "t1", codexBreakdown(1200, 1000, 30, 10, 1230), codexBreakdown(1200, 1000, 30, 10, 1230)),
				codexTurnEvent("turn/completed", "fresh", "t1", "completed"),
			},
			want: codexTurnUsage{usage: codexUsage(1200, 1000, 30, 10), context: codexContext(1230), threadTotal: 1230},
		},
		{
			// fill_to_context_window resets the total; neither it nor a re-report
			// is a request, and the next request still counts.
			name: "full window, re-report and recovery",
			lines: []string{
				codexTokenUsage("thread", "historical", codexBreakdown(1500, 1100, 20, 5, 1520), codexBreakdown(2700, 2100, 50, 15, 2750)),
				codexTurnEvent("turn/started", "thread", "t2", "inProgress"),
				codexTokenUsage("thread", "t2", req1810, codexBreakdown(4500, 3500, 60, 15, 4560)),
				codexTokenUsage("thread", "t2", req1810, codexBreakdown(4500, 3500, 60, 15, 4560)),
				codexTokenUsage("thread", "t2", `{"totalTokens":253840,"inputTokens":0,"cachedInputTokens":0,"cacheWriteInputTokens":0,"outputTokens":0,"reasoningOutputTokens":0}`, `{"totalTokens":258400,"inputTokens":0,"cachedInputTokens":0,"cacheWriteInputTokens":0,"outputTokens":0,"reasoningOutputTokens":0}`),
				codexTokenUsage("thread", "t2", codexBreakdown(900, 0, 40, 0, 940), `{"totalTokens":259340,"inputTokens":900,"cachedInputTokens":0,"cacheWriteInputTokens":0,"outputTokens":40,"reasoningOutputTokens":0}`),
				codexTurnEvent("turn/completed", "thread", "t2", "completed"),
			},
			want: codexTurnUsage{usage: codexUsage(2700, 1400, 50, 0), context: codexContext(940)},
		},
		{
			// A turn that ends after compaction has no request measuring the
			// compacted context.
			name: "compaction without a later request",
			lines: []string{
				codexTurnEvent("turn/started", "thread", "t1", "inProgress"),
				codexTokenUsage("thread", "t1", req1810, req1810),
				codexTokenUsage("thread", "t1", `{"totalTokens":5330,"inputTokens":0,"cachedInputTokens":0,"cacheWriteInputTokens":0,"outputTokens":0,"reasoningOutputTokens":0}`, req1810),
				codexTurnEvent("turn/completed", "thread", "t1", "interrupted"),
			},
			want: codexTurnUsage{usage: codexUsage(1800, 1400, 10, 0)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCodexWire(t, tc.metadata)
			thread := strings.Split(strings.Split(tc.lines[0], `"threadId":"`)[1], `"`)[0]
			w.register(thread)
			w.feed(tc.lines...)
			w.assert(tc.want)
		})
	}
}

func TestCodexCacheWriteStaysInsideInput(t *testing.T) {
	w := newCodexWire(t, nil)
	w.register("thread")
	breakdown := `{"totalTokens":1020,"inputTokens":1000,"cachedInputTokens":600,"cacheWriteInputTokens":300,"outputTokens":20,"reasoningOutputTokens":0}`
	w.feed(
		codexTurnEvent("turn/started", "thread", "turn", "inProgress"),
		codexTokenUsage("thread", "turn", breakdown, breakdown),
		codexTurnEvent("turn/completed", "thread", "turn", "completed"),
	)
	usage := w.results[0].Usage
	if usage.InputTokens != 1000 || usage.InputTokenDetails != (sdk.InputTokenDetail{NoCacheTokens: 100, CacheReadTokens: 600, CacheWriteTokens: 300}) || usage.CachedInputTokens != 600 {
		t.Fatalf("usage = %+v", usage)
	}
}

// The status view names the thread's cumulative count for what it is; the
// context measurement comes from the session's observation, not from here.
func TestCodexStatusNamesThreadTotal(t *testing.T) {
	status := (&Driver{}).cachedStatus(external.PromptInput{RuntimeMetadata: map[string]any{
		metadataThreadIDKey: "thread", "codex_thread_total_tokens": int64(4560), "codex_context_window": int64(258400),
	}})
	if status["thread_total_tokens"] != int64(4560) {
		t.Fatalf("status = %#v", status)
	}
	for _, key := range []string{"tokens", "context_window", "context_tokens"} {
		if _, ok := status[key]; ok {
			t.Fatalf("status reports %q: %#v", key, status)
		}
	}
}
