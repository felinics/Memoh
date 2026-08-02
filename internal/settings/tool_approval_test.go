package settings

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestToolApprovalConfigUnmarshalMergesLegacyEditIntoWrite(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"enabled": true,
		"write": {
			"require_approval": false,
			"bypass_globs": ["/data/**"],
			"force_review_globs": []
		},
		"edit": {
			"require_approval": true,
			"bypass_globs": ["/tmp/**"],
			"force_review_globs": ["/data/secret/**"]
		},
		"exec": {
			"require_approval": true,
			"bypass_commands": ["go"],
			"force_review_commands": ["rm"]
		}
	}`)

	var cfg ToolApprovalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal legacy tool approval config: %v", err)
	}
	cfg = NormalizeToolApprovalConfig(cfg)

	if !cfg.Enabled {
		t.Fatal("enabled was not preserved")
	}
	if cfg.Read.RequireApproval {
		t.Fatal("missing read policy should use the default non-approving posture")
	}
	if !cfg.Write.RequireApproval {
		t.Fatal("legacy edit require_approval was not merged into write")
	}
	if got, want := cfg.Write.BypassGlobs, []string{"/data/**", "/tmp/**"}; !sameStrings(got, want) {
		t.Fatalf("write bypass globs = %#v, want %#v", got, want)
	}
	if got, want := cfg.Write.ForceReviewGlobs, []string{"/data/secret/**"}; !sameStrings(got, want) {
		t.Fatalf("write force review globs = %#v, want %#v", got, want)
	}
	if !cfg.Exec.RequireApproval || !sameStrings(cfg.Exec.BypassCommands, []string{"go"}) || !sameStrings(cfg.Exec.ForceReviewCommands, []string{"rm"}) {
		t.Fatalf("exec policy was not preserved: %#v", cfg.Exec)
	}
}

func TestToolApprovalConfigUnmarshalDefaultsPartialPolicies(t *testing.T) {
	t.Parallel()

	var cfg ToolApprovalConfig
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &cfg); err != nil {
		t.Fatalf("unmarshal partial tool approval config: %v", err)
	}
	cfg = NormalizeToolApprovalConfig(cfg)

	if !cfg.Enabled {
		t.Fatal("enabled was not preserved")
	}
	if cfg.Read.RequireApproval {
		t.Fatal("read should default to not requiring approval")
	}
	if !cfg.Write.RequireApproval {
		t.Fatal("write should default to requiring approval")
	}
	if cfg.Exec.RequireApproval {
		t.Fatal("exec should default to not requiring approval")
	}
}

func TestToolApprovalConfigUnmarshalMergesEditOnlyWithDefaultWrite(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"edit": {
			"require_approval": true,
			"bypass_globs": ["/workspace/cache/**"],
			"force_review_globs": [".env*"]
		}
	}`)

	var cfg ToolApprovalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal edit-only tool approval config: %v", err)
	}
	cfg = NormalizeToolApprovalConfig(cfg)

	if got, want := cfg.Write.BypassGlobs, []string{"/data/**", "/tmp/**", "/workspace/cache/**"}; !sameStrings(got, want) {
		t.Fatalf("write bypass globs = %#v, want %#v", got, want)
	}
	if got, want := cfg.Write.ForceReviewGlobs, []string{".env*"}; !sameStrings(got, want) {
		t.Fatalf("write force review globs = %#v, want %#v", got, want)
	}
}

func TestToolApprovalConfigNormalizesExplicitModes(t *testing.T) {
	t.Parallel()

	var cfg ToolApprovalConfig
	if err := json.Unmarshal([]byte(`{
		"read":{"mode":" ALLOW "},
		"write":{"mode":"ask"},
		"exec":{"mode":"DENY"}
	}`), &cfg); err != nil {
		t.Fatalf("unmarshal tool approval modes: %v", err)
	}
	cfg = NormalizeToolApprovalConfig(cfg)

	if cfg.Read.Mode != ToolApprovalAllow || cfg.Write.Mode != ToolApprovalAsk || cfg.Exec.Mode != ToolApprovalDeny {
		t.Fatalf("normalized modes = read:%q write:%q exec:%q", cfg.Read.Mode, cfg.Write.Mode, cfg.Exec.Mode)
	}
}

func sameStrings(left, right []string) bool {
	return slices.Equal(left, right)
}
