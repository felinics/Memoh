package approval

import "testing"

func TestOperationForCallClassifiesAppLaunchAsExec(t *testing.T) {
	t.Parallel()

	if op, ok := OperationForCall("exec", map[string]any{"command": "ls"}); !ok || op != OperationExec {
		t.Fatalf("exec must stay exec, got %q ok=%v", op, ok)
	}
	if _, ok := OperationForCall("computer_context", map[string]any{"action": "list_apps"}); ok {
		t.Fatal("listing applications is not a governed operation")
	}
	if _, ok := OperationForCall("computer_context", map[string]any{"action": "get_app", "app": "xfce4-terminal"}); ok {
		t.Fatal("selecting a running application must not be governed as exec")
	}
	args := map[string]any{"action": "get_app", "app": "xfce4-terminal", "launch": true}
	op, ok := OperationForCall("computer_context", args)
	if !ok || op != OperationExec {
		t.Fatalf("launching an application must be governed as exec, got %q ok=%v", op, ok)
	}
	if args["command"] != "xfce4-terminal" {
		t.Fatalf("launch must expose the application as the exec command, got %#v", args["command"])
	}
}

func TestPolicyDecisionGovernsAppLaunchLikeExec(t *testing.T) {
	t.Parallel()

	cfg := PolicyConfig{Enabled: true, Exec: ExecPolicy{Mode: PolicyModeAsk}}
	if got := policyDecision(cfg, "computer_context", map[string]any{"action": "get_app", "app": "xterm", "launch": true}); got != DecisionNeedsApproval {
		t.Fatalf("launch under ask policy must need approval, got %q", got)
	}
	if got := policyDecision(cfg, "computer_context", map[string]any{"action": "get_app", "app": "xterm"}); got != DecisionBypass {
		t.Fatalf("selecting without launch must bypass, got %q", got)
	}
	deny := PolicyConfig{Enabled: true, Exec: ExecPolicy{Mode: PolicyModeDeny}}
	if got := policyDecision(deny, "computer_context", map[string]any{"action": "get_app", "app": "xterm", "launch": true}); got != DecisionDeny {
		t.Fatalf("launch under deny policy must be denied, got %q", got)
	}
}
