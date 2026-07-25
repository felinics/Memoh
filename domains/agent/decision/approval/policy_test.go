package approval

import (
	"testing"
)

func needsApproval(cfg PolicyConfig, toolName string, input any) bool {
	return policyDecision(cfg, toolName, input) == DecisionNeedsApproval
}

func TestPolicyDecisionExplicitModes(t *testing.T) {
	t.Parallel()

	cfg := DefaultPolicyConfig()
	cfg.Enabled = true
	cfg.Read.Mode = PolicyModeAllow
	cfg.Read.ForceReviewGlobs = []string{"/etc/**"}
	cfg.Write.Mode = PolicyModeDeny
	cfg.Write.ForceReviewGlobs = []string{"/data/secret/**"}
	cfg.Exec.Mode = PolicyModeAsk
	cfg.Exec.RequireApproval = false

	if got := policyDecision(cfg, "read", map[string]any{"path": "/etc/passwd"}); got != DecisionBypass {
		t.Fatalf("read allow decision = %q, want bypass", got)
	}
	if got := policyDecision(cfg, "write", map[string]any{"path": "/data/secret/token"}); got != DecisionDeny {
		t.Fatalf("write deny decision = %q, want deny", got)
	}
	if got := policyDecision(cfg, "exec", map[string]any{"command": "go test ./..."}); got != DecisionNeedsApproval {
		t.Fatalf("exec ask decision = %q, want needs approval", got)
	}

	cfg.Exec.BypassCommands = []string{"go test *"}
	if got := policyDecision(cfg, "exec", map[string]any{"command": "go test ./..."}); got != DecisionBypass {
		t.Fatalf("exec ask bypass decision = %q, want bypass", got)
	}
}

func TestPolicyDecisionDisabledTargetBypassesSavedPolicies(t *testing.T) {
	t.Parallel()

	cfg := DefaultPolicyConfig()
	cfg.Enabled = false
	cfg.Read.Mode = PolicyModeAsk
	cfg.Read.ForceReviewGlobs = []string{"/etc/**"}
	cfg.Write.Mode = PolicyModeDeny
	cfg.Exec.Mode = PolicyModeDeny

	for name, tc := range map[string]struct {
		tool  string
		input map[string]any
	}{
		"read":  {tool: "read", input: map[string]any{"path": "/etc/passwd"}},
		"write": {tool: "write", input: map[string]any{"path": "/etc/passwd"}},
		"exec":  {tool: "exec", input: map[string]any{"command": "rm -rf /tmp/example"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := policyDecision(cfg, tc.tool, tc.input); got != DecisionBypass {
				t.Fatalf("decision = %q, want bypass while target approval is disabled", got)
			}
		})
	}

	cfg.Enabled = true
	if got := policyDecision(cfg, "write", map[string]any{"path": "/etc/passwd"}); got != DecisionDeny {
		t.Fatalf("re-enabled write decision = %q, want saved deny mode", got)
	}
}

func TestNeedsApprovalFileBypass(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true

	if needsApproval(cfg, "read", map[string]any{"path": "/etc/passwd"}) {
		t.Fatal("expected read approval to be disabled by default")
	}
	if needsApproval(cfg, "list", map[string]any{"path": "/etc"}) {
		t.Fatal("expected list approval to be disabled by default")
	}
	if needsApproval(cfg, "write", map[string]any{"path": "/data/tmp/output.txt"}) {
		t.Fatal("expected /data path to bypass write approval")
	}
	if needsApproval(cfg, "write", map[string]any{"path": "daily.md"}) {
		t.Fatal("expected relative /data path to bypass write approval")
	}
	if needsApproval(cfg, "edit", map[string]any{"path": "/tmp/output.txt"}) {
		t.Fatal("expected /tmp path to bypass edit approval")
	}
	if !needsApproval(cfg, "edit", map[string]any{"path": "/etc/passwd"}) {
		t.Fatal("expected non-bypassed edit path to require approval")
	}
}

func TestNeedsApprovalForceReviewOverridesBypass(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true
	cfg.Write.ForceReviewGlobs = []string{"/data/secret/**"}

	if !needsApproval(cfg, "write", map[string]any{"path": "/data/secret/token.txt"}) {
		t.Fatal("expected force-review path to require approval even under /data")
	}
}

func TestNeedsApprovalApplyPatchUsesFileApproval(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true

	if !needsApproval(cfg, "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** End Patch"}) {
		t.Fatal("expected pathless apply_patch to require approval when file approvals are enabled")
	}
	if needsApproval(cfg, "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Add File: notes.txt\n+hello\n*** End Patch"}) {
		t.Fatal("expected relative apply_patch path to bypass under default /data policy")
	}
	if needsApproval(cfg, "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Update File: /tmp/output.txt\n@@\n-old\n+new\n*** End Patch"}) {
		t.Fatal("expected /tmp apply_patch path to bypass")
	}

	cfg.Write.RequireApproval = false
	if needsApproval(cfg, "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** End Patch"}) {
		t.Fatal("expected apply_patch to skip approval when file approvals are disabled")
	}

	cfg.Write.ForceReviewGlobs = []string{"/data/secret/**"}
	if !needsApproval(cfg, "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Update File: /data/secret/token.txt\n@@\n-old\n+new\n*** End Patch"}) {
		t.Fatal("expected apply_patch force-review path to require approval")
	}
}

func TestNeedsApprovalExecDefaultsToAllowed(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true

	if needsApproval(cfg, "exec", map[string]any{"command": "npm test"}) {
		t.Fatal("expected exec to be allowed by default")
	}
	if needsApproval(cfg, "exec", map[string]any{"command": "npm test && rm -rf /data"}) {
		t.Fatal("expected compound exec to be allowed when approval is disabled")
	}
}

func TestNeedsApprovalExecForceReview(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true
	cfg.Exec.ForceReviewCommands = []string{"rm"}

	if !needsApproval(cfg, "exec", map[string]any{"command": "rm file.txt"}) {
		t.Fatal("expected force-review command to require approval")
	}
}

func TestNeedsApprovalReadPolicy(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true
	cfg.Read.RequireApproval = true
	cfg.Read.BypassGlobs = []string{"/data/public/**"}

	if needsApproval(cfg, "read", map[string]any{"path": "/data/public/note.txt"}) {
		t.Fatal("expected read bypass glob to skip approval")
	}
	if !needsApproval(cfg, "list", map[string]any{"path": "/etc"}) {
		t.Fatal("expected list to use read approval policy")
	}
}

func TestNeedsApprovalFilePathAliases(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true
	cfg.Write.BypassGlobs = []string{"/data/public/**", "/tmp/**"}

	if needsApproval(cfg, "write", map[string]any{"file_path": "/data/public/note.txt"}) {
		t.Fatal("expected file_path alias to use write bypass globs")
	}
	if needsApproval(cfg, "write", map[string]any{"files": []any{"/data/public/a.txt", "/tmp/b.txt"}}) {
		t.Fatal("expected all files entries under bypass globs to skip approval")
	}
	if !needsApproval(cfg, "write", map[string]any{"files": []any{"/data/public/a.txt", "/etc/passwd"}}) {
		t.Fatal("expected non-bypassed file in multi-path input to require approval")
	}
}

func TestNeedsApprovalExecCommandPatterns(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Enabled = true
	cfg.Exec.RequireApproval = true
	cfg.Exec.BypassCommands = []string{"git status", "ls", "npm *"}
	cfg.Exec.ForceReviewCommands = []string{"sudo *", "rm -rf *"}

	if needsApproval(cfg, "exec", map[string]any{"command": "git status"}) {
		t.Fatal("expected exact command bypass to skip approval")
	}
	if needsApproval(cfg, "exec", map[string]any{"command": "npm test"}) {
		t.Fatal("expected wildcard command bypass to skip approval")
	}
	if !needsApproval(cfg, "exec", map[string]any{"command": "git status --short"}) {
		t.Fatal("expected non-matching command to require approval")
	}
	if !needsApproval(cfg, "exec", map[string]any{"command": "sudo apt update"}) {
		t.Fatal("expected wildcard force-review command to require approval")
	}
	if !needsApproval(cfg, "exec", map[string]any{"command": "rm -rf /data/tmp"}) {
		t.Fatal("expected force-review command with slash to require approval")
	}
}

func TestOperationForTool(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"read":        OperationRead,
		"list":        OperationRead,
		"write":       OperationWrite,
		"edit":        OperationWrite,
		"apply_patch": OperationWrite,
		"exec":        OperationExec,
	}
	for tool, want := range cases {
		if got, ok := OperationForTool(tool); !ok || got != want {
			t.Fatalf("OperationForTool(%q) = %q, %v; want %q, true", tool, got, ok, want)
		}
	}
	if got, ok := OperationForTool("web_search"); ok || got != "" {
		t.Fatalf("OperationForTool(web_search) = %q, %v; want unsupported", got, ok)
	}
}
