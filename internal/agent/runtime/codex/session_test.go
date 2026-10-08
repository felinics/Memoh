package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
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

func TestResumeThreadFallsBackFromStalePath(t *testing.T) {
	for _, scenario := range []string{"matching", "mismatched", "refused", "transport_failure", "fallback_refused"} {
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
				if scenario == "mismatched" || scenario == "refused" || scenario == "fallback_refused" {
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
					if i == 0 && (scenario == "mismatched" || scenario == "fallback_refused") {
						response = `"result":{"thread":{"id":"old","path":"/data/stored.jsonl"}}`
					} else if (i == 0 && scenario == "refused") || (i == 1 && scenario == "fallback_refused") {
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
			if scenario == "transport_failure" || scenario == "fallback_refused" {
				if err == nil || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected original failure, got %v", err)
				}
				if response.Thread.ID != "" {
					t.Fatalf("retained the wrong thread after a failed retry: %+v", response.Thread)
				}
			} else if err != nil || response.Thread.ID != "wanted" || response.Thread.Path != nil {
				t.Fatalf("resume = %+v, %v", response, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
