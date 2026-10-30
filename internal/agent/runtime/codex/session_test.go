package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
	"github.com/felinics/memoh/internal/workspace/bridgesvc"
)

func TestTurnResultKeepsSessionAnchorOnFailure(t *testing.T) {
	for _, outcome := range []string{"completed", "interrupted", "process_exit", "start_rejected", "goal_activation_failed"} {
		t.Run(outcome, func(t *testing.T) {
			input := external.PromptInput{RuntimeMetadata: map[string]any{
				metadataThreadIDKey: "old-thread", metadataRolloutPathKey: "/data/old.jsonl",
			}}
			turn := newTurnState(t.Context(), input, "new-thread", nil, nil, nil, nil, slog.Default())
			defer turn.close()
			srv := &appServer{threadPaths: map[string]string{"new-thread": "/data/new.jsonl"}}
			var result external.PromptResult
			var err error
			switch outcome {
			case "start_rejected":
				result, err = srv.turnResultAfterError(turn, &protocol.RPCError{Code: -32600, Message: "rejected"})
			case "goal_activation_failed":
				turn.setTurnID("accepted-turn")
				result, err = srv.turnResultAfterError(turn, context.Canceled)
			case "process_exit":
				turn.setTurnID("accepted-turn")
				result, err = srv.turnResult(turn)
			default:
				turn.handleNotification(&protocol.TurnCompletedNotification{
					ThreadID: "new-thread", Turn: protocol.Turn{ID: "accepted-turn", Status: protocol.TurnStatus(outcome)},
				})
				result, err = srv.turnResult(turn)
			}
			if outcome == "completed" || outcome == "interrupted" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failure lost its error")
			}
			if result.RuntimeMetadata[metadataThreadIDKey] != "new-thread" || result.RuntimeMetadata[metadataRolloutPathKey] != "/data/new.jsonl" {
				t.Fatalf("mismatched session anchor: %#v", result.RuntimeMetadata)
			}
			if input.RuntimeMetadata[metadataRolloutPathKey] != "/data/old.jsonl" {
				t.Fatal("result mutated the input metadata")
			}
		})
	}
}

func TestRecordThreadMetadataClearsOnlySupersededPaths(t *testing.T) {
	previous := map[string]any{metadataThreadIDKey: "old-thread", metadataRolloutPathKey: "/data/old.jsonl"}
	var delta map[string]any
	recordThreadMetadata(&delta, previous, "new-thread", "")
	if value, present := delta[metadataRolloutPathKey]; !present || value != nil {
		t.Fatalf("new thread must delete the previous path: %#v", delta)
	}

	delta = nil
	recordThreadMetadata(&delta, previous, "old-thread", "")
	if delta != nil {
		t.Fatalf("unchanged thread must retain its known path: %#v", delta)
	}

	delta = map[string]any{"codex_thread_total_tokens": int64(12)}
	recordThreadMetadata(&delta, previous, "old-thread", "/data/moved.jsonl")
	if delta[metadataRolloutPathKey] != "/data/moved.jsonl" || delta["codex_thread_total_tokens"] != int64(12) {
		t.Fatalf("updated path lost existing metadata: %#v", delta)
	}
}

// The messages are Codex 0.163.0's own, as thread/resume reports them. Only a
// refusal naming the requested thread as held is waited for.
func TestResumeRefusalsNamingTheThreadAsHeld(t *testing.T) {
	for _, tc := range []struct {
		held    bool
		message string
	}{
		{true, "thread 019a already has an active writer"},
		{true, "error resuming thread: Fatal error: Failed to initialize session: invalid thread-store request: thread 019a already has a live local writer"},
		{true, "thread 019a is closing; retry thread/resume after the thread is closed"},
		{true, "thread 019a is closing; retry after the thread is closed"},
		{false, "thread 019b already has an active writer"},
		{false, "no rollout found for thread id 019a"},
		{false, "Server is draining; retry after reconnecting"},
	} {
		if got := threadHeld(&protocol.RPCError{Code: -32603, Message: tc.message}, "019a"); got != tc.held {
			t.Errorf("threadHeld(%q) = %v, want %v", tc.message, got, tc.held)
		}
	}
}

func TestResumeThreadFallsBackFromStalePath(t *testing.T) {
	for _, scenario := range []string{"matching", "mismatched", "refused", "transport_failure", "fallback_refused", "held", "held_elsewhere"} {
		t.Run(scenario, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = server.Close() }()
			srv := &appServer{logger: slog.Default()}
			srv.conn = newConn(client, srv, slog.Default())
			defer func() { _ = srv.conn.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				scanner := bufio.NewScanner(server)
				calls := 1
				if scenario == "mismatched" || scenario == "refused" || scenario == "fallback_refused" || scenario == "held_elsewhere" {
					calls = 2
				}
				for i := range calls {
					if !scanner.Scan() {
						done <- fmt.Errorf("missing resume request %d: %w", i, scanner.Err())
						return
					}
					var request struct {
						ID     json.RawMessage
						Method string
						Params protocol.ThreadResumeParams
					}
					if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
						done <- err
						return
					}
					if request.Method != protocol.MethodThreadResume || request.Params.ThreadID != "wanted" ||
						(i == 0 && (request.Params.Path == nil || *request.Params.Path != "/data/stored.jsonl")) ||
						(i == 1 && request.Params.Path != nil) {
						done <- fmt.Errorf("unexpected resume request: %+v", request)
						return
					}
					if scenario == "transport_failure" {
						done <- server.Close()
						return
					}
					response := `"result":{"thread":{"id":"wanted"}}`
					switch {
					case scenario == "held":
						response = `"error":{"code":-32600,"message":"thread wanted already has an active writer"}`
					case i == 0 && scenario == "held_elsewhere":
						response = `"error":{"code":-32600,"message":"thread old already has an active writer"}`
					case i == 0 && (scenario == "mismatched" || scenario == "fallback_refused"):
						response = `"result":{"thread":{"id":"old","path":"/data/stored.jsonl"}}`
					case (i == 0 && scenario == "refused") || (i == 1 && scenario == "fallback_refused"):
						response = `"error":{"code":-32600,"message":"no rollout found"}`
					}
					if _, err := fmt.Fprintf(server, "{\"id\":%s,%s}\n", request.ID, response); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			storedPath := "/data/stored.jsonl"
			response, err := srv.resumeThread(ctx, protocol.ThreadResumeParams{ThreadID: "wanted", Path: &storedPath})
			var refused *protocol.RPCError
			switch scenario {
			case "held":
				// No id retry: its "no rollout found" would hide the holder.
				if !errors.As(err, &refused) || !threadHeld(refused, "wanted") {
					t.Fatalf("expected the held refusal, got %v", err)
				}
			case "transport_failure", "fallback_refused":
				if err == nil || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected original failure, got %v", err)
				}
				if response.Thread.ID != "" {
					t.Fatalf("retained the wrong thread after a failed retry: %+v", response.Thread)
				}
			default:
				if err != nil || response.Thread.ID != "wanted" || response.Thread.Path != nil {
					t.Fatalf("resume = %+v, %v", response, err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A held thread is intact and waited for; any other refusal starts over and
// says so.
func TestEnsureThreadWaitsForHeldThreads(t *testing.T) {
	held := `"error":{"code":-32600,"message":"thread wanted already has an active writer"}`
	for _, tc := range []struct {
		name    string
		wait    time.Duration
		resumes []string // successive thread/resume answers; the last repeats
		thread  string
		failure external.FailureKind
		started bool
	}{
		{name: "released while waiting", wait: time.Minute, resumes: []string{held, `"result":{"thread":{"id":"wanted"}}`}, thread: "wanted"},
		{name: "held throughout", wait: 600 * time.Millisecond, resumes: []string{held}, failure: external.FailureRuntimeBusy},
		{name: "refused", wait: time.Minute, resumes: []string{`"error":{"code":-32600,"message":"no rollout found for thread id wanted"}`}, thread: "fresh", started: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answers := tc.resumes
			srv, requests := fakeAppServer(t, func(method string) string {
				switch method {
				case protocol.MethodThreadResume:
					answer := answers[0]
					if len(answers) > 1 {
						answers = answers[1:]
					}
					return answer
				case protocol.MethodThreadStart:
					return `"result":{"thread":{"id":"fresh"}}`
				}
				return `"error":{"code":-32601,"message":"unexpected"}`
			})
			d := &Driver{logger: slog.Default(), heldWait: tc.wait}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			sink := &captureSink{}
			threadID, err := d.ensureThread(ctx, srv, Config{}, external.PromptInput{
				BotID: "bot", BotAgentID: "agent",
				RuntimeMetadata: map[string]any{metadataThreadIDKey: "wanted"},
				Sink:            sink,
			})
			var failure *external.Failure
			if tc.failure != 0 {
				if threadID != "" || !errors.As(err, &failure) || failure.Kind != tc.failure {
					t.Fatalf("ensureThread = %q, %v, want failure kind %d", threadID, err, tc.failure)
				}
			} else if err != nil || threadID != tc.thread {
				t.Fatalf("ensureThread = %q, %v, want %s", threadID, err, tc.thread)
			}
			methods := requests()
			resumes := len(tc.resumes)
			if tc.failure == external.FailureRuntimeBusy {
				resumes = max(len(methods), 2)
			}
			want := slices.Repeat([]string{protocol.MethodThreadResume}, resumes)
			if tc.started {
				want = append(want, protocol.MethodThreadStart)
			}
			if !slices.Equal(methods, want) {
				t.Errorf("requests = %v, want %v", methods, want)
			}
			if notified := len(sink.events) == 1 && sink.events[0].NoticeKind == event.NoticeNativeHistoryLost; notified != tc.started || len(sink.events) > 1 {
				t.Errorf("events = %+v, want a lost-history notice only when starting over", sink.events)
			}
		})
	}
}

// fakeAppServer answers each request with answer(method), a JSON-RPC result
// or error member, and reports the methods asked so far.
func fakeAppServer(t *testing.T, answer func(method string) string) (*appServer, func() []string) {
	t.Helper()
	client, server := net.Pipe()
	srv := &appServer{
		logger:          slog.Default(),
		client:          testBridge(t),
		workspaceInfo:   bridge.WorkspaceInfo{ToolsHTTPURL: "http://127.0.0.1:1/mcp"},
		mountCtx:        t.Context(),
		loadedThreads:   map[string]bool{},
		threadPaths:     map[string]string{},
		threadSettings:  map[string]protocol.Settings{},
		toollessThreads: map[string]bool{},
		toolMounts:      map[string]*toolmount.Mount{},
	}
	srv.conn = newConn(client, srv, slog.Default())
	t.Cleanup(func() {
		_ = srv.conn.Close()
		_ = server.Close()
	})
	var mu sync.Mutex
	var methods []string
	go func() {
		scanner := bufio.NewScanner(server)
		for scanner.Scan() {
			var request struct {
				ID     json.RawMessage
				Method string
			}
			if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
				continue
			}
			mu.Lock()
			methods = append(methods, request.Method)
			mu.Unlock()
			if _, err := fmt.Fprintf(server, "{\"id\":%s,%s}\n", request.ID, answer(request.Method)); err != nil {
				return
			}
		}
	}()
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(methods)
	}
}

// testBridge serves an in-process workspace bridge, which the tool gateway
// mount needs.
func testBridge(t *testing.T) *bridge.Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, bridgesvc.New(bridgesvc.Options{DefaultWorkDir: t.TempDir(), WorkspaceRoot: t.TempDir(), ReverseHTTP: bridgesvc.NewReverseHTTPBroker()}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough://bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}
