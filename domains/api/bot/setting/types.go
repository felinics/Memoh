package setting

import (
	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
)

const (
	DefaultLanguage          = "auto"
	DefaultCommandUILanguage = "auto"
	DefaultReasoningEffort   = "medium"
	DefaultHeartbeatInterval = 1440
	ChatRuntimeModel         = "model"
	ChatRuntimeACPAgent      = "acp_agent"
	DefaultACPProjectPath    = "/data"
	DefaultACPProjectMode    = "project"
)

type Settings struct {
	ChatModelID            string                                `json:"chat_model_id"`
	ChatRuntime            string                                `json:"chat_runtime"`
	ChatACPAgentID         string                                `json:"chat_acp_agent_id,omitempty"`
	ChatACPProjectPath     string                                `json:"chat_acp_project_path,omitempty"`
	ChatACPProjectMode     string                                `json:"chat_acp_project_mode,omitempty"`
	ImageModelID           string                                `json:"image_model_id"`
	SearchProviderID       string                                `json:"search_provider_id"`
	FetchProviderID        string                                `json:"fetch_provider_id"`
	MemoryProviderID       string                                `json:"memory_provider_id"`
	TtsModelID             string                                `json:"tts_model_id"`
	TranscriptionModelID   string                                `json:"transcription_model_id"`
	VideoModelID           string                                `json:"video_model_id"`
	Language               string                                `json:"language"`
	CommandUILanguage      string                                `json:"command_ui_language"`
	AclDefaultEffect       string                                `json:"acl_default_effect"`
	Timezone               string                                `json:"timezone"`
	ReasoningEnabled       bool                                  `json:"reasoning_enabled"`
	ReasoningEffort        string                                `json:"reasoning_effort"`
	HeartbeatEnabled       bool                                  `json:"heartbeat_enabled"`
	HeartbeatInterval      int                                   `json:"heartbeat_interval"`
	HeartbeatModelID       string                                `json:"heartbeat_model_id"`
	CompactionEnabled      bool                                  `json:"compaction_enabled"`
	CompactionThreshold    int                                   `json:"compaction_threshold"`
	CompactionRatio        int                                   `json:"compaction_ratio"`
	CompactionModelID      string                                `json:"compaction_model_id,omitempty"`
	DiscussProbeModelID    string                                `json:"discuss_probe_model_id,omitempty"`
	PersistFullToolResults bool                                  `json:"persist_full_tool_results"`
	ShowToolCallsInIM      bool                                  `json:"show_tool_calls_in_im"`
	ToolApprovalConfig     settingpersistence.ToolApprovalConfig `json:"tool_approval_config"`
	DisplayEnabled         bool                                  `json:"display_enabled"`
	OverlayEnabled         bool                                  `json:"overlay_enabled"`
	OverlayProvider        string                                `json:"overlay_provider,omitempty"`
	OverlayConfig          map[string]any                        `json:"overlay_config,omitempty"`
}

type UpsertRequest struct {
	ChatModelID            string                                 `json:"chat_model_id,omitempty"`
	ChatRuntime            *string                                `json:"chat_runtime,omitempty"`
	ChatACPAgentID         *string                                `json:"chat_acp_agent_id,omitempty"`
	ChatACPProjectPath     *string                                `json:"chat_acp_project_path,omitempty"`
	ChatACPProjectMode     *string                                `json:"chat_acp_project_mode,omitempty"`
	ImageModelID           string                                 `json:"image_model_id,omitempty"`
	SearchProviderID       string                                 `json:"search_provider_id,omitempty"`
	FetchProviderID        *string                                `json:"fetch_provider_id,omitempty"`
	MemoryProviderID       string                                 `json:"memory_provider_id,omitempty"`
	TtsModelID             string                                 `json:"tts_model_id,omitempty"`
	TranscriptionModelID   string                                 `json:"transcription_model_id,omitempty"`
	VideoModelID           string                                 `json:"video_model_id,omitempty"`
	Language               string                                 `json:"language,omitempty"`
	CommandUILanguage      string                                 `json:"command_ui_language,omitempty"`
	AclDefaultEffect       string                                 `json:"acl_default_effect,omitempty"`
	Timezone               *string                                `json:"timezone,omitempty"`
	ReasoningEnabled       *bool                                  `json:"reasoning_enabled,omitempty"`
	ReasoningEffort        *string                                `json:"reasoning_effort,omitempty"`
	HeartbeatEnabled       *bool                                  `json:"heartbeat_enabled,omitempty"`
	HeartbeatInterval      *int                                   `json:"heartbeat_interval,omitempty"`
	HeartbeatModelID       string                                 `json:"heartbeat_model_id,omitempty"`
	CompactionEnabled      *bool                                  `json:"compaction_enabled,omitempty"`
	CompactionThreshold    *int                                   `json:"compaction_threshold,omitempty"`
	CompactionRatio        *int                                   `json:"compaction_ratio,omitempty"`
	CompactionModelID      *string                                `json:"compaction_model_id,omitempty"`
	DiscussProbeModelID    string                                 `json:"discuss_probe_model_id,omitempty"`
	PersistFullToolResults *bool                                  `json:"persist_full_tool_results,omitempty"`
	ShowToolCallsInIM      *bool                                  `json:"show_tool_calls_in_im,omitempty"`
	ToolApprovalConfig     *settingpersistence.ToolApprovalConfig `json:"tool_approval_config,omitempty"`
	DisplayEnabled         *bool                                  `json:"display_enabled,omitempty"`
	OverlayEnabled         *bool                                  `json:"overlay_enabled,omitempty"`
	OverlayProvider        *string                                `json:"overlay_provider,omitempty"`
	OverlayConfig          map[string]any                         `json:"overlay_config,omitempty"`
}
