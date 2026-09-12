package claudecode

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	sessiontest "github.com/felinics/memoh/internal/testutil/sessionruntime"
)

type closingApprovalFlow struct {
	approval.FlowService
	mu  sync.Mutex
	req approval.Request
}

func (*closingApprovalFlow) EvaluatePolicy(context.Context, approval.CreatePendingInput) (approval.Evaluation, error) {
	return approval.Evaluation{}, nil
}

func (f *closingApprovalFlow) CreatePending(_ context.Context, in approval.CreatePendingInput) (approval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.req = approval.Request{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", BotID: in.BotID, SessionID: in.SessionID, ToolCallID: in.ToolCallID, ToolName: in.ToolName, Status: approval.StatusPending}
	return f.req, nil
}

func (f *closingApprovalFlow) Get(context.Context, string) (approval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req, nil
}

func (f *closingApprovalFlow) Reject(context.Context, string, string, string) (approval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.req.Status = approval.StatusRejected
	return f.req, nil
}

func (*closingApprovalFlow) WaitForDecision(ctx context.Context, _ string) (approval.Request, error) {
	<-ctx.Done()
	return approval.Request{}, ctx.Err()
}

func TestClosedApprovalDoesNotParkFinishedRun(t *testing.T) {
	for _, terminalDelivered := range []bool{false, true} {
		name := "close_drops_terminal_decision"
		if terminalDelivered {
			name = "control_terminal_decision_delivered"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			m := sessiontest.New(sessionruntime.NewMemoryBackend(), sessionruntime.Options{OwnerID: "closing-approval", StateTTL: time.Minute})
			if err := m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.Close() }()
			h, err := sessiontest.Start(ctx, m, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1))
			if err != nil {
				t.Fatal(err)
			}
			m.MarkInlineDecisionRun(h.BotID, h.SessionID, h.RunID)
			seen := make(chan string, 4)
			fail := make(chan error, 4)
			flow := &closingApprovalFlow{}
			st := newTurnRunner(ctx, external.PromptInput{BotID: h.BotID, ThreadID: h.SessionID, RunID: h.RunID, CanRequestUserInput: true, Sink: external.EventSinkFunc(func(ev event.StreamEvent) {
				_, err := m.HandleAgentEvent(context.Background(), h, ev)
				if err != nil {
					fail <- err
				}
				if ev.Type == event.ToolApprovalRequest {
					seen <- ev.Status
				}
			})}, nil, flow, nil, slog.Default())
			defer st.close()
			done := make(chan struct{})
			go func() { defer close(done); st.decide(st.ctx, "pwd", "exec", map[string]any{"command": "pwd"}) }()
			select {
			case s := <-seen:
				if s != approval.StatusPending {
					t.Fatal(s)
				}
			case <-time.After(time.Second):
				t.Fatal("pending decision not observed")
			}
			if terminalDelivered {
				rejected, _ := flow.Reject(ctx, "", "", "")
				st.emitApprovalRequest(rejected)
				select {
				case s := <-seen:
					if s != approval.StatusRejected {
						t.Fatal(s)
					}
				case <-time.After(time.Second):
					t.Fatal("terminal decision not observed")
				}
			}
			// Driver.Prompt defers turn.close on process exit. It marks the event pump
			// closed before cancelling the inline approval, so its rejected event drops.
			st.close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("approval did not stop")
			}
			stored, _ := flow.Get(ctx, "")
			if stored.Status != approval.StatusRejected {
				t.Fatalf("durable decision=%s", stored.Status)
			}
			// The application has no pending rows left to cancel, and reports its
			// persisted failure through the stream while returning nil to finishWSRun.
			for _, ev := range []native.StreamEvent{{Type: native.EventError, Code: "runtime_prompt_failed", Error: "runtime_prompt_failed"}, {Type: native.EventAbort}} {
				if _, err := m.HandleAgentEvent(ctx, h, ev); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.FinishRun(ctx, h, "", ""); err != nil {
				t.Fatal(err)
			}
			snap, err := m.Snapshot(ctx, h.BotID, h.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			parked := snap.CurrentRunView != nil && snap.CurrentRunView.Status == sessionruntime.RunStatusWaitingDecision
			if snap.CurrentRunView == nil || snap.CurrentRunView.Status != sessionruntime.RunStatusErrored || snap.CurrentRunView.ErrorCode != "runtime_prompt_failed" {
				t.Fatalf("terminalDelivered=%v parked=%v snapshot=%+v", terminalDelivered, parked, snap.CurrentRunView)
			}
			if _, err := sessiontest.Start(ctx, m, h.BotID, h.SessionID, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
				t.Fatalf("next run blocked: %v", err)
			}
			select {
			case err := <-fail:
				t.Fatal(err)
			default:
			}
			t.Logf("durable=%s parked=%v terminalDelivered=%v", stored.Status, parked, terminalDelivered)
		})
	}
}
