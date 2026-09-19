package sessionruntime

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

func TestNativeInlineApprovalCompletesWithoutReentry(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "with-deferred-sibling"}[sibling], func(t *testing.T) {
			f := newAdmitFixture(t)
			ctx := context.Background()
			admitted, err := f.manager.Admit(ctx, f.input("management-approval", `{"text":"install"}`))
			if err != nil {
				t.Fatal(err)
			}
			emit := func(event native.StreamEvent) {
				t.Helper()
				if _, err := f.manager.HandleAgentEvent(ctx, admitted.Handle, event); err != nil {
					t.Fatal(err)
				}
			}
			emit(native.StreamEvent{Type: native.EventToolApprovalRequest, ApprovalID: "install", ToolCallID: "install", ToolName: "app_manage", Status: "pending", InlineDecision: true})
			if sibling {
				emit(native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: "question", Status: "pending"})
			}
			emit(native.StreamEvent{Type: native.EventToolApprovalRequest, ApprovalID: "install", ToolCallID: "install", ToolName: "app_manage", Status: "approved", InlineDecision: true})
			want := ledger.StateRunning
			if sibling {
				want = ledger.StateWaitingDecision
			}
			if got := f.runs.State(admitted.RunID); got != want {
				t.Fatalf("after inline approval: %s, want %s", got, want)
			}
			if sibling {
				// Answering a deferred sibling still waits for its committed re-entry.
				emit(native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: "question", Status: "submitted"})
				if got := f.runs.State(admitted.RunID); got != ledger.StateWaitingDecision {
					t.Fatalf("deferred sibling resumed early: %s", got)
				}
				emit(native.StreamEvent{Type: native.EventAgentStart})
			}
			emit(native.StreamEvent{Type: native.EventAgentEnd})
			if err := f.manager.FinishRun(ctx, admitted.Handle, "", ""); err != nil {
				t.Fatal(err)
			}
			if got := f.runs.State(admitted.RunID); got != ledger.StateCompleted {
				t.Fatalf("run remains busy after completed operation: %s", got)
			}
		})
	}
}
