package approval

import (
	"context"
	"strings"
)

// PolicyMode controls whether one operation class is allowed, reviewed, or
// denied before execution.
type PolicyMode string

const (
	PolicyModeAllow PolicyMode = "allow"
	PolicyModeAsk   PolicyMode = "ask"
	PolicyModeDeny  PolicyMode = "deny"
)

type FilePolicy struct {
	Mode             PolicyMode
	RequireApproval  bool
	BypassGlobs      []string
	ForceReviewGlobs []string
}

type ExecPolicy struct {
	Mode                PolicyMode
	RequireApproval     bool
	BypassCommands      []string
	ForceReviewCommands []string
}

// PolicyConfig is the approval domain's projection of persisted tool approval
// settings. Transport and persistence packages convert their own DTOs at the
// composition boundary.
type PolicyConfig struct {
	Enabled bool
	Read    FilePolicy
	Write   FilePolicy
	Exec    ExecPolicy
}

// PolicyProvider resolves the effective bot-level approval policy without
// coupling the approval domain to the Settings service.
type PolicyProvider interface {
	ToolApprovalPolicy(ctx context.Context, botID string) (PolicyConfig, error)
}

func DefaultPolicyConfig() PolicyConfig {
	fileBypass := []string{"/data/**", "/tmp/**"}
	return PolicyConfig{
		Enabled: false,
		Read: FilePolicy{
			RequireApproval:  false,
			BypassGlobs:      []string{},
			ForceReviewGlobs: []string{},
		},
		Write: FilePolicy{
			RequireApproval:  true,
			BypassGlobs:      append([]string(nil), fileBypass...),
			ForceReviewGlobs: []string{},
		},
		Exec: ExecPolicy{
			RequireApproval:     false,
			BypassCommands:      []string{},
			ForceReviewCommands: []string{},
		},
	}
}

func NormalizePolicyConfig(cfg PolicyConfig) PolicyConfig {
	defaults := DefaultPolicyConfig()
	defaults.Enabled = cfg.Enabled
	defaults.Read = normalizeFilePolicy(cfg.Read, defaults.Read)
	defaults.Write = normalizeFilePolicy(cfg.Write, defaults.Write)
	defaults.Exec = normalizeExecPolicy(cfg.Exec, defaults.Exec)
	return defaults
}

func normalizeFilePolicy(policy, defaults FilePolicy) FilePolicy {
	defaults.Mode = normalizePolicyMode(policy.Mode)
	defaults.RequireApproval = policy.RequireApproval
	if policy.BypassGlobs != nil {
		defaults.BypassGlobs = append([]string{}, policy.BypassGlobs...)
	}
	if policy.ForceReviewGlobs != nil {
		defaults.ForceReviewGlobs = append([]string{}, policy.ForceReviewGlobs...)
	}
	return defaults
}

func normalizeExecPolicy(policy, defaults ExecPolicy) ExecPolicy {
	defaults.Mode = normalizePolicyMode(policy.Mode)
	defaults.RequireApproval = policy.RequireApproval
	if policy.BypassCommands != nil {
		defaults.BypassCommands = append([]string{}, policy.BypassCommands...)
	}
	if policy.ForceReviewCommands != nil {
		defaults.ForceReviewCommands = append([]string{}, policy.ForceReviewCommands...)
	}
	return defaults
}

func normalizePolicyMode(mode PolicyMode) PolicyMode {
	switch PolicyMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case PolicyModeAllow:
		return PolicyModeAllow
	case PolicyModeAsk:
		return PolicyModeAsk
	case PolicyModeDeny:
		return PolicyModeDeny
	default:
		return ""
	}
}
