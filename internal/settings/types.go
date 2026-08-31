package settings

import (
	"encoding/json"
	"strings"

	"github.com/felinics/memoh/internal/runtimekind"
)

const (
	DefaultLanguage          = "auto"
	DefaultCommandUILanguage = "auto"
	DefaultReasoningEffort   = "medium"
	ChatRuntimeModel         = string(runtimekind.Model)
	ChatRuntimeACPAgent      = string(runtimekind.ACPAgent)
	ChatRuntimeCodex         = string(runtimekind.Codex)
	ChatRuntimeClaudeCode    = string(runtimekind.ClaudeCode)
	DefaultACPProjectPath    = "/data"
	DefaultACPProjectMode    = "project"
)

type Settings struct {
	ChatModelID          string `json:"chat_model_id"`
	DefaultBotAgentID    string `json:"default_bot_agent_id,omitempty"`
	ChatRuntime          string `json:"chat_runtime"`
	ChatACPAgentID       string `json:"chat_acp_agent_id,omitempty"`
	ChatACPProjectPath   string `json:"chat_acp_project_path,omitempty"`
	ChatACPProjectMode   string `json:"chat_acp_project_mode,omitempty"`
	ImageModelID         string `json:"image_model_id"`
	SearchProviderID     string `json:"search_provider_id"`
	FetchProviderID      string `json:"fetch_provider_id"`
	MemoryProviderID     string `json:"memory_provider_id"`
	TtsModelID           string `json:"tts_model_id"`
	TranscriptionModelID string `json:"transcription_model_id"`
	VideoModelID         string `json:"video_model_id"`
	Language             string `json:"language"`
	CommandUILanguage    string `json:"command_ui_language"`
	AclDefaultEffect     string `json:"acl_default_effect"`
	Timezone             string `json:"timezone"`
	// ReasoningEffort is the single on/off source for reasoning:
	// models.ReasoningEffortDisable means no reasoning, any other value is a tier.
	ReasoningEffort         string             `json:"reasoning_effort"`
	CompactionEnabled       bool               `json:"compaction_enabled"`
	CompactionThreshold     int                `json:"compaction_threshold"`
	CompactionTargetPercent *int               `json:"compaction_target_percent" extensions:"x-nullable"`
	CompactionModelID       string             `json:"compaction_model_id,omitempty"`
	DiscussProbeModelID     string             `json:"discuss_probe_model_id,omitempty"`
	PersistFullToolResults  bool               `json:"persist_full_tool_results"`
	ShowToolCallsInIM       bool               `json:"show_tool_calls_in_im"`
	ToolApprovalConfig      ToolApprovalConfig `json:"tool_approval_config"`
	DisplayEnabled          bool               `json:"display_enabled"`
	OverlayEnabled          bool               `json:"overlay_enabled"`
	OverlayProvider         string             `json:"overlay_provider,omitempty"`
	OverlayConfig           map[string]any     `json:"overlay_config,omitempty"`
}

type UpsertRequest struct {
	// Reference fields below use pointer semantics so autosaving clients can
	// clear a selection: nil = keep current, "" = clear, value = set. The
	// service mirrors each into a `<field>_set` SQL flag (same pattern as
	// FetchProviderID / CompactionModelID); plain strings would make ""
	// indistinguishable from "not sent".
	ChatModelID          *string `json:"chat_model_id,omitempty"`
	DefaultBotAgentID    *string `json:"default_bot_agent_id,omitempty"`
	ChatRuntime          *string `json:"chat_runtime,omitempty"`
	ChatACPAgentID       *string `json:"chat_acp_agent_id,omitempty"`
	ChatACPProjectPath   *string `json:"chat_acp_project_path,omitempty"`
	ChatACPProjectMode   *string `json:"chat_acp_project_mode,omitempty"`
	ImageModelID         *string `json:"image_model_id,omitempty"`
	SearchProviderID     *string `json:"search_provider_id,omitempty"`
	FetchProviderID      *string `json:"fetch_provider_id,omitempty"`
	MemoryProviderID     *string `json:"memory_provider_id,omitempty"`
	TtsModelID           *string `json:"tts_model_id,omitempty"`
	TranscriptionModelID *string `json:"transcription_model_id,omitempty"`
	VideoModelID         *string `json:"video_model_id,omitempty"`
	// Language follows the same pointer rule; "" normalizes to DefaultLanguage
	// ("auto") rather than clearing the column.
	Language                *string             `json:"language,omitempty"`
	CommandUILanguage       string              `json:"command_ui_language,omitempty"`
	AclDefaultEffect        string              `json:"acl_default_effect,omitempty"`
	Timezone                *string             `json:"timezone,omitempty"`
	ReasoningEffort         *string             `json:"reasoning_effort,omitempty"`
	CompactionEnabled       *bool               `json:"compaction_enabled,omitempty"`
	CompactionThreshold     *int                `json:"compaction_threshold,omitempty"`
	CompactionTargetPercent *int                `json:"compaction_target_percent,omitempty"`
	CompactionModelID       *string             `json:"compaction_model_id,omitempty"`
	DiscussProbeModelID     string              `json:"discuss_probe_model_id,omitempty"`
	PersistFullToolResults  *bool               `json:"persist_full_tool_results,omitempty"`
	ShowToolCallsInIM       *bool               `json:"show_tool_calls_in_im,omitempty"`
	ToolApprovalConfig      *ToolApprovalConfig `json:"tool_approval_config,omitempty"`
	DisplayEnabled          *bool               `json:"display_enabled,omitempty"`
	OverlayEnabled          *bool               `json:"overlay_enabled,omitempty"`
	OverlayProvider         *string             `json:"overlay_provider,omitempty"`
	OverlayConfig           map[string]any      `json:"overlay_config,omitempty"`
}

type ToolApprovalConfig struct {
	Enabled bool                   `json:"enabled"`
	Read    ToolApprovalFilePolicy `json:"read"`
	Write   ToolApprovalFilePolicy `json:"write"`
	Exec    ToolApprovalExecPolicy `json:"exec"`
}

type ToolApprovalMode string

const (
	ToolApprovalAllow ToolApprovalMode = "allow"
	ToolApprovalAsk   ToolApprovalMode = "ask"
	ToolApprovalDeny  ToolApprovalMode = "deny"
)

type ToolApprovalFilePolicy struct {
	Mode             ToolApprovalMode `json:"mode,omitempty"`
	RequireApproval  bool             `json:"require_approval"`
	BypassGlobs      []string         `json:"bypass_globs"`
	ForceReviewGlobs []string         `json:"force_review_globs"`
}

type ToolApprovalExecPolicy struct {
	Mode                ToolApprovalMode `json:"mode,omitempty"`
	RequireApproval     bool             `json:"require_approval"`
	BypassCommands      []string         `json:"bypass_commands"`
	ForceReviewCommands []string         `json:"force_review_commands"`
}

func DefaultToolApprovalConfig() ToolApprovalConfig {
	fileBypass := []string{"/data/**", "/tmp/**"}
	return ToolApprovalConfig{
		Enabled: false,
		Read: ToolApprovalFilePolicy{
			RequireApproval:  false,
			BypassGlobs:      []string{},
			ForceReviewGlobs: []string{},
		},
		Write: ToolApprovalFilePolicy{
			RequireApproval:  true,
			BypassGlobs:      append([]string(nil), fileBypass...),
			ForceReviewGlobs: []string{},
		},
		Exec: ToolApprovalExecPolicy{
			RequireApproval:     false,
			BypassCommands:      []string{},
			ForceReviewCommands: []string{},
		},
	}
}

func NormalizeToolApprovalConfig(cfg ToolApprovalConfig) ToolApprovalConfig {
	defaults := DefaultToolApprovalConfig()
	defaults.Enabled = cfg.Enabled
	defaults.Read = normalizeFilePolicy(cfg.Read, defaults.Read)
	defaults.Write = normalizeFilePolicy(cfg.Write, defaults.Write)
	defaults.Exec = normalizeExecPolicy(cfg.Exec, defaults.Exec)
	return defaults
}

func (c *ToolApprovalConfig) UnmarshalJSON(data []byte) error {
	defaults := DefaultToolApprovalConfig()
	*c = defaults
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var raw struct {
		Enabled *bool           `json:"enabled"`
		Read    json.RawMessage `json:"read"`
		Write   json.RawMessage `json:"write"`
		Edit    json.RawMessage `json:"edit"`
		Exec    json.RawMessage `json:"exec"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Enabled != nil {
		c.Enabled = *raw.Enabled
	}
	if len(raw.Read) > 0 {
		policy, err := unmarshalFilePolicy(raw.Read, defaults.Read)
		if err != nil {
			return err
		}
		c.Read = policy
	}
	writePolicy := defaults.Write
	if len(raw.Write) > 0 {
		policy, err := unmarshalFilePolicy(raw.Write, defaults.Write)
		if err != nil {
			return err
		}
		writePolicy = policy
	}
	if len(raw.Edit) > 0 {
		policy, err := unmarshalFilePolicy(raw.Edit, defaults.Write)
		if err != nil {
			return err
		}
		writePolicy = mergeFilePolicies(writePolicy, policy)
	}
	c.Write = writePolicy
	if len(raw.Exec) > 0 {
		policy, err := unmarshalExecPolicy(raw.Exec, defaults.Exec)
		if err != nil {
			return err
		}
		c.Exec = policy
	}
	return nil
}

func unmarshalFilePolicy(data []byte, defaults ToolApprovalFilePolicy) (ToolApprovalFilePolicy, error) {
	policy := cloneFilePolicy(defaults)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return policy, err
	}
	if value, ok := raw["mode"]; ok {
		if err := json.Unmarshal(value, &policy.Mode); err != nil {
			return policy, err
		}
	}
	if value, ok := raw["require_approval"]; ok {
		if err := json.Unmarshal(value, &policy.RequireApproval); err != nil {
			return policy, err
		}
	}
	if value, ok := raw["bypass_globs"]; ok {
		if err := json.Unmarshal(value, &policy.BypassGlobs); err != nil {
			return policy, err
		}
	}
	if value, ok := raw["force_review_globs"]; ok {
		if err := json.Unmarshal(value, &policy.ForceReviewGlobs); err != nil {
			return policy, err
		}
	}
	return policy, nil
}

func unmarshalExecPolicy(data []byte, defaults ToolApprovalExecPolicy) (ToolApprovalExecPolicy, error) {
	policy := cloneExecPolicy(defaults)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return policy, err
	}
	if value, ok := raw["mode"]; ok {
		if err := json.Unmarshal(value, &policy.Mode); err != nil {
			return policy, err
		}
	}
	if value, ok := raw["require_approval"]; ok {
		if err := json.Unmarshal(value, &policy.RequireApproval); err != nil {
			return policy, err
		}
	}
	if value, ok := raw["bypass_commands"]; ok {
		if err := json.Unmarshal(value, &policy.BypassCommands); err != nil {
			return policy, err
		}
	}
	if value, ok := raw["force_review_commands"]; ok {
		if err := json.Unmarshal(value, &policy.ForceReviewCommands); err != nil {
			return policy, err
		}
	}
	return policy, nil
}

func cloneFilePolicy(policy ToolApprovalFilePolicy) ToolApprovalFilePolicy {
	return ToolApprovalFilePolicy{
		Mode:             normalizeToolApprovalMode(policy.Mode),
		RequireApproval:  policy.RequireApproval,
		BypassGlobs:      append([]string{}, policy.BypassGlobs...),
		ForceReviewGlobs: append([]string{}, policy.ForceReviewGlobs...),
	}
}

func cloneExecPolicy(policy ToolApprovalExecPolicy) ToolApprovalExecPolicy {
	return ToolApprovalExecPolicy{
		Mode:                normalizeToolApprovalMode(policy.Mode),
		RequireApproval:     policy.RequireApproval,
		BypassCommands:      append([]string{}, policy.BypassCommands...),
		ForceReviewCommands: append([]string{}, policy.ForceReviewCommands...),
	}
}

func mergeFilePolicies(left, right ToolApprovalFilePolicy) ToolApprovalFilePolicy {
	return ToolApprovalFilePolicy{
		Mode:             mergeToolApprovalModes(left.Mode, right.Mode),
		RequireApproval:  left.RequireApproval || right.RequireApproval,
		BypassGlobs:      mergeStringLists(left.BypassGlobs, right.BypassGlobs),
		ForceReviewGlobs: mergeStringLists(left.ForceReviewGlobs, right.ForceReviewGlobs),
	}
}

func mergeStringLists(left, right []string) []string {
	if len(left) == 0 && len(right) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(left)+len(right))
	out := make([]string, 0, len(left)+len(right))
	for _, list := range [][]string{left, right} {
		for _, item := range list {
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func normalizeFilePolicy(policy, defaults ToolApprovalFilePolicy) ToolApprovalFilePolicy {
	defaults.Mode = normalizeToolApprovalMode(policy.Mode)
	defaults.RequireApproval = policy.RequireApproval
	if policy.BypassGlobs != nil {
		defaults.BypassGlobs = append([]string{}, policy.BypassGlobs...)
	}
	if policy.ForceReviewGlobs != nil {
		defaults.ForceReviewGlobs = append([]string{}, policy.ForceReviewGlobs...)
	}
	return defaults
}

func normalizeExecPolicy(policy, defaults ToolApprovalExecPolicy) ToolApprovalExecPolicy {
	defaults.Mode = normalizeToolApprovalMode(policy.Mode)
	defaults.RequireApproval = policy.RequireApproval
	if policy.BypassCommands != nil {
		defaults.BypassCommands = append([]string{}, policy.BypassCommands...)
	}
	if policy.ForceReviewCommands != nil {
		defaults.ForceReviewCommands = append([]string{}, policy.ForceReviewCommands...)
	}
	return defaults
}

func normalizeToolApprovalMode(mode ToolApprovalMode) ToolApprovalMode {
	switch ToolApprovalMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ToolApprovalAllow:
		return ToolApprovalAllow
	case ToolApprovalAsk:
		return ToolApprovalAsk
	case ToolApprovalDeny:
		return ToolApprovalDeny
	default:
		return ""
	}
}

func mergeToolApprovalModes(left, right ToolApprovalMode) ToolApprovalMode {
	left = normalizeToolApprovalMode(left)
	right = normalizeToolApprovalMode(right)
	for _, mode := range []ToolApprovalMode{ToolApprovalDeny, ToolApprovalAsk, ToolApprovalAllow} {
		if left == mode || right == mode {
			return mode
		}
	}
	return ""
}
