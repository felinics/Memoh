package acp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestSubagentDriverRetainsIdentityThroughWarmRuntime(t *testing.T) {
	pool := newFakeScriptPool(t)
	driver := NewDriver(pool)
	var original *runtimeHandle
	for _, runID := range []string{"child-first", "child-followup"} {
		fence := runtimefence.Fence{BotID: "bot-1", SessionID: "child", Token: 7}
		ctx := runtimefence.WithContext(t.Context(), fence)
		observed := make(chan mcp.ToolSessionContext, 1)
		result, err := driver.Prompt(ctx, external.PromptInput{
			BotID: "bot-1", ThreadID: "child", RunID: runID, SessionMode: sessionmode.Subagent,
			RuntimeOwnerAccountID: "owner", CanRequestUserInput: true, Prompt: "work",
			ActorUserID:     runID + "-caller",
			RuntimeMetadata: map[string]any{metadataAgentIDKey: acpprofile.AgentACPID, "project_path": "/data/project", "is_subagent": false},
			Sink: external.EventSinkFunc(func(event.StreamEvent) {
				h := pool.sessionHandle("child")
				if h == nil {
					return
				}
				identity, ok := pool.ResolveRuntimeToolContext("bot-1", h.id, h.toolToken)
				if ok {
					select {
					case observed <- identity:
					default:
					}
				}
			}),
		})
		if err != nil || !result.TurnCompleted {
			t.Fatalf("prompt failed: result=%+v err=%v", result, err)
		}
		select {
		case identity := <-observed:
			if identity.UserID != runID+"-caller" {
				t.Fatalf("trusted caller lost or retained from previous turn: %q", identity.UserID)
			}
			if !identity.IsSubagent || identity.SessionType != sessionmode.Subagent || !identity.CanRequestUserInput {
				t.Fatalf("trusted child identity or execution approval lost: %+v", identity)
			}
			if identity.SessionID != "child" || identity.RunID != runID || identity.RuntimeFence != fence {
				t.Fatalf("child run or fence lost: %+v", identity)
			}
		default:
			t.Fatal("no runtime event exposed the active tool identity")
		}
		h := pool.sessionHandle("child")
		if original != nil && original != h {
			t.Fatal("followup replaced the child's warm runtime")
		}
		original = h
		if identity := h.toolContext(); identity.RunID != "" || identity.RuntimeActive || identity.IsSubagent || identity.UserID != "" {
			t.Fatalf("finished child leaked active identity: %+v", identity)
		}
	}
}

func TestSubagentRuntimeProcessCarriesThreadMarker(t *testing.T) {
	pool := newFakeScriptPool(t)
	pool.timeout = time.Hour
	for _, tc := range []struct {
		session string
		mode    string
		marked  bool
	}{{"child-marked", sessionmode.Subagent, true}, {"chat-unmarked", "", false}} {
		_, err := pool.Prompt(t.Context(), PromptInput{
			BotID: "bot-1", SessionID: tc.session, RunID: "run-" + tc.session, SessionType: tc.mode,
			AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project", Prompt: "work", RuntimeOwnerAccountID: "user-1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if found := markedProcessCount(t, external.SubagentThreadEnv+"="+tc.session); (found > 0) != tc.marked {
			t.Fatalf("%s: marked processes=%d, want marked=%v", tc.session, found, tc.marked)
		}
	}
}

func markedProcessCount(t *testing.T, marker string) int {
	t.Helper()
	entries, err := filepath.Glob("/proc/[0-9]*/environ")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		data, err := os.ReadFile(entry) //nolint:gosec // test scans its own process table.
		if err != nil {
			continue
		}
		if slices.Contains(strings.Split(string(data), "\x00"), marker) {
			count++
		}
	}
	return count
}
