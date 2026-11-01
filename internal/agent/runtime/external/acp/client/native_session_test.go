package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
)

func TestRunnerRestoresNativeSession(t *testing.T) {
	for _, tt := range []struct {
		name, nativeID, resume, load, resumeErr, loadErr string
		// replayBurst is how many updates session/load replays in one write.
		replayBurst string
		// lastCall is the last session method the agent saw; wantID is empty
		// when StartSession must fail and keep the stored session.
		lastCall, wantID string
		// restoreFailed marks a failure the user may answer with a new session.
		restoreFailed bool
	}{
		{name: "no stored session", resume: "1", load: "1", lastCall: "new", wantID: "fake-session"},
		{name: "resume preferred", nativeID: "saved", resume: "1", load: "1", lastCall: "resume", wantID: "saved"},
		{name: "load without resume", nativeID: "saved", load: "1", lastCall: "load", wantID: "saved"},
		// Far past acp-go-sdk's 1024-notification queue.
		{name: "long load replay", nativeID: "saved", load: "1", replayBurst: "5000", lastCall: "load", wantID: "saved"},
		{name: "refused resume falls back to load", nativeID: "saved", resume: "1", load: "1", resumeErr: "unsupported", lastCall: "load", wantID: "saved"},
		{name: "missing session", nativeID: "saved", resume: "1", resumeErr: "missing", lastCall: "new", wantID: "fake-session"},
		{name: "missing session reported as invalid params", nativeID: "saved", load: "1", loadErr: "invalid", lastCall: "new", wantID: "fake-session"},
		{name: "missing session reported as internal error", nativeID: "saved", load: "1", loadErr: "internal", lastCall: "new", wantID: "fake-session"},
		{name: "no restore capability", nativeID: "saved", lastCall: "new", wantID: "fake-session"},
		{name: "auth required keeps session", nativeID: "saved", resume: "1", load: "1", resumeErr: "auth", lastCall: "resume"},
		{name: "broken connection keeps session", nativeID: "saved", load: "1", loadErr: "exit", lastCall: "load", restoreFailed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, command := newStartSessionTestRunner(t)
			capture := filepath.Join(t.TempDir(), "lifecycle.json")
			t.Setenv("MEMOH_ACP_FAKE_AGENT_CAPTURE_SESSION_LIFECYCLE_FILE", capture)
			t.Setenv("MEMOH_ACP_FAKE_AGENT_RESUME", tt.resume)
			t.Setenv("MEMOH_ACP_FAKE_AGENT_LOAD", tt.load)
			t.Setenv("MEMOH_ACP_FAKE_AGENT_RESUME_ERROR", tt.resumeErr)
			t.Setenv("MEMOH_ACP_FAKE_AGENT_LOAD_ERROR", tt.loadErr)
			t.Setenv("MEMOH_ACP_FAKE_AGENT_REPLAY_HISTORY", "1")
			t.Setenv("MEMOH_ACP_FAKE_AGENT_REPLAY_BURST", tt.replayBurst)
			t.Setenv("MEMOH_ACP_FAKE_AGENT_USAGE", "{}")
			var (
				mu     sync.Mutex
				events []event.StreamEvent
			)
			sink := EventSinkFunc(func(ev event.StreamEvent) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, ev)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			sess, err := runner.StartSession(ctx, StartRequest{
				BotID: "bot-1", AgentID: acpprofile.AgentACPID, ProjectPath: "/data/project",
				Command: command, NativeSessionID: tt.nativeID,
			}, sink)
			if tt.wantID == "" {
				if err == nil {
					_ = sess.Close()
					t.Fatal("StartSession replaced a session the agent did not refuse")
				}
				if errors.Is(err, ErrSessionRestoreFailed) != tt.restoreFailed {
					t.Fatalf("StartSession() error = %v, want restore failure %v", err, tt.restoreFailed)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = sess.Close() }()
				if sess.ID() != tt.wantID {
					t.Fatalf("session = %q, want %q", sess.ID(), tt.wantID)
				}
				result, err := sess.Prompt(ctx, "continue", sink)
				if err != nil || result.Text != "usage reported" {
					t.Fatalf("live prompt = %q, %v", result.Text, err)
				}
			}

			raw, err := os.ReadFile(capture) //nolint:gosec // test-owned temporary file.
			if err != nil {
				t.Fatal(err)
			}
			var last struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(raw, &last); err != nil {
				t.Fatal(err)
			}
			if last.Method != tt.lastCall {
				t.Fatalf("last session call = %q, want %q", last.Method, tt.lastCall)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, ev := range events {
				if strings.Contains(ev.Delta, "historical") || ev.ToolCallID == "historical-tool" {
					t.Fatalf("replayed history reached the live stream: %#v", ev)
				}
			}
		})
	}
}
