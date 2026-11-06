package sessionruntime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

// largeToolOutput is shaped like a decoded tool result: a JSON tree with a long
// text body and some structure, about 32 KB once encoded.
func largeToolOutput(i int) any {
	lines := make([]any, 64)
	for j := range lines {
		lines[j] = map[string]any{"line": float64(j), "text": strings.Repeat(fmt.Sprintf("row %d.%d ", i, j), 40)}
	}
	return map[string]any{"ok": true, "path": fmt.Sprintf("/data/file-%d.txt", i), "lines": lines}
}

// benchmarkRunOutputDelta streams text deltas into a run whose view already
// holds toolMessages large tool results, committing the held output every
// deltasPerWindow deltas as the output window would. One delta per window is
// the worst case: tokens slower than the window.
func benchmarkRunOutputDelta(b *testing.B, backend Backend, toolMessages, deltasPerWindow int) {
	b.Helper()
	ctx := context.Background()
	manager := NewManager(backend, Options{OwnerID: "bench-owner", StateTTL: time.Hour, OwnerLeaseTTL: 30 * time.Second})
	if err := manager.Start(ctx); err != nil {
		b.Fatalf("start manager: %v", err)
	}
	b.Cleanup(func() { _ = manager.Close() })
	sessionID := fmt.Sprintf("session-output-bench-%d", time.Now().UnixNano())
	runID := "run-output-bench"
	if err := manager.startRunWithAdmission(ctx, testBotID, sessionID, runID, RunAdmissionView{}, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		b.Fatalf("start run: %v", err)
	}
	ref, ok, err := manager.RunRef(ctx, testBotID, sessionID, runID)
	if err != nil || !ok {
		b.Fatalf("run ref = ok:%v err:%v", ok, err)
	}
	handle := RunHandle{BotID: ref.BotID, SessionID: ref.SessionID, RunID: ref.RunID, Generation: ref.Generation}
	for i := range toolMessages {
		messages := []chatview.UIMessage{{
			ID: i + 1, Type: chatview.UIMessageTool, Name: "read", ToolCallID: fmt.Sprintf("call-%d", i),
			Input: map[string]any{"path": fmt.Sprintf("/data/file-%d.txt", i)}, Output: largeToolOutput(i),
		}}
		if _, _, err := manager.updateActiveAndPublish(ctx, handle, func(snapshot Snapshot, _ time.Time) (Snapshot, bool, error) {
			snapshot.Seq++
			for _, message := range messages {
				snapshot.CurrentRunView.Messages = upsertUIMessage(snapshot.CurrentRunView.Messages, message)
			}
			return snapshot, true, nil
		}, func(Snapshot) RuntimeDelta { return RuntimeDelta{MessageUpserts: messages} }); err != nil {
			b.Fatalf("seed tool output: %v", err)
		}
	}
	ctrl := manager.localControlForHandle(handle)
	event := native.StreamEvent{Type: native.EventTextDelta, Delta: "token "}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := manager.HandleAgentEvent(ctx, handle, event); err != nil {
			b.Fatalf("handle delta: %v", err)
		}
		if (i+1)%deltasPerWindow == 0 || i == b.N-1 {
			ctrl.output.mu.Lock()
			err := manager.flushRunOutputLocked(ctx, ctrl)
			ctrl.output.mu.Unlock()
			if err != nil {
				b.Fatalf("commit output: %v", err)
			}
		}
	}
}

// BenchmarkRunOutputDelta is meant for a fixed count, e.g. -benchtime 2000x:
// the run's text grows with every delta, so ns/op depends on b.N.
func BenchmarkRunOutputDelta(b *testing.B) {
	url := os.Getenv("MEMOH_TEST_VALKEY_URL")
	if url == "" {
		url = os.Getenv("MEMOH_TEST_REDIS_URL")
	}
	for _, tools := range []int{4, 32} {
		for _, perWindow := range []int{1, 4} {
			name := fmt.Sprintf("tools=%d/deltas_per_window=%d", tools, perWindow)
			b.Run("memory/"+name, func(b *testing.B) {
				benchmarkRunOutputDelta(b, NewMemoryBackend(), tools, perWindow)
			})
			if url == "" {
				continue
			}
			b.Run("redis/"+name, func(b *testing.B) {
				backend, err := NewRedisBackend(context.Background(), RedisOptions{URL: url, KeyPrefix: uniqueRuntimeBackendPrefix("output-bench"), StateTTL: time.Minute})
				if err != nil {
					b.Fatalf("redis backend: %v", err)
				}
				benchmarkRunOutputDelta(b, backend, tools, perWindow)
			})
		}
	}
}
