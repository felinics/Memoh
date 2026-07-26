package setting

import (
	"encoding/json"
	"slices"
	"testing"

	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
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

	var cfg settingpersistence.ToolApprovalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal legacy tool approval config: %v", err)
	}
	cfg = settingpersistence.NormalizeToolApprovalConfig(cfg)

	if !cfg.Enabled {
		t.Fatal("enabled was not preserved")
	}
	if cfg.Read.RequireApproval {
		t.Fatal("missing read policy should use the default non-approving posture")
	}
	if !cfg.Write.RequireApproval {
		t.Fatal("legacy edit require_approval was not merged into write")
	}
	if got, want := cfg.Write.BypassGlobs, []string{"/data/**", "/tmp/**"}; !slices.Equal(got, want) {
		t.Fatalf("write bypass globs = %#v, want %#v", got, want)
	}
	if got, want := cfg.Write.ForceReviewGlobs, []string{"/data/secret/**"}; !slices.Equal(got, want) {
		t.Fatalf("write force review globs = %#v, want %#v", got, want)
	}
	if !cfg.Exec.RequireApproval || !slices.Equal(cfg.Exec.BypassCommands, []string{"go"}) || !slices.Equal(cfg.Exec.ForceReviewCommands, []string{"rm"}) {
		t.Fatalf("exec policy was not preserved: %#v", cfg.Exec)
	}
}

func TestToolApprovalConfigUnmarshalDefaultsPartialPolicies(t *testing.T) {
	t.Parallel()

	var cfg settingpersistence.ToolApprovalConfig
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &cfg); err != nil {
		t.Fatalf("unmarshal partial tool approval config: %v", err)
	}
	cfg = settingpersistence.NormalizeToolApprovalConfig(cfg)

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

	var cfg settingpersistence.ToolApprovalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal edit-only tool approval config: %v", err)
	}
	cfg = settingpersistence.NormalizeToolApprovalConfig(cfg)

	if got, want := cfg.Write.BypassGlobs, []string{"/data/**", "/tmp/**", "/workspace/cache/**"}; !slices.Equal(got, want) {
		t.Fatalf("write bypass globs = %#v, want %#v", got, want)
	}
	if got, want := cfg.Write.ForceReviewGlobs, []string{".env*"}; !slices.Equal(got, want) {
		t.Fatalf("write force review globs = %#v, want %#v", got, want)
	}
}

func TestToolApprovalConfigNormalizesExplicitModes(t *testing.T) {
	t.Parallel()

	var cfg settingpersistence.ToolApprovalConfig
	if err := json.Unmarshal([]byte(`{
		"read":{"mode":" ALLOW "},
		"write":{"mode":"ask"},
		"exec":{"mode":"DENY"}
	}`), &cfg); err != nil {
		t.Fatalf("unmarshal tool approval modes: %v", err)
	}
	cfg = settingpersistence.NormalizeToolApprovalConfig(cfg)

	if cfg.Read.Mode != settingpersistence.ToolApprovalAllow || cfg.Write.Mode != settingpersistence.ToolApprovalAsk || cfg.Exec.Mode != settingpersistence.ToolApprovalDeny {
		t.Fatalf("normalized modes = read:%q write:%q exec:%q", cfg.Read.Mode, cfg.Write.Mode, cfg.Exec.Mode)
	}
}
