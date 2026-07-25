package profile

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	AgentCodexID        = "codex"
	AgentCodexName      = "Codex"
	AgentClaudeCodeID   = "claude-code"
	AgentClaudeCodeName = "Claude Code"
	AgentHermesID       = "hermes"
	AgentHermesName     = "Hermes"

	MetadataKeyACP = "acp"

	setupModeAPIKey = "api_key"
	setupModeOAuth  = "oauth"
	setupModeSelf   = "self"
)

type Profile struct {
	ID          string
	DisplayName string
	Description string
	// DynamicCommand, DynamicArgs, and DynamicPackage describe an npm-backed
	// launcher whose version can be refreshed independently of Memoh. The
	// session pool resolves the dist-tag once per bot for the lifetime of the
	// server process, launches the resulting exact package version, and falls
	// back to Command/Args when lookup or startup fails. DynamicArgs are the
	// arguments inserted before the exact package spec.
	DynamicCommand string
	DynamicArgs    []string
	DynamicPackage string
	Command        string
	Args           []string
	// SessionModeID, when set, is the ACP session mode Memoh pins right after
	// session/new so tool permissions flow through ACP regardless of ambient
	// agent-side configuration (e.g. a host ~/.claude/settings.json).
	SessionModeID string
	// SessionConfigValues are ACP session config options pinned after
	// session/new when the agent advertises them. Options the agent does not
	// expose are skipped.
	SessionConfigValues map[string]string
	// ReasoningConfigID maps an agent-specific select to ACP's semantic
	// thought_level category when the agent has not annotated the option yet.
	// Categorized options always take precedence over this compatibility ID.
	ReasoningConfigID string
	// DefaultReasoningEffort is applied only when the session has no explicit
	// user choice. The live option state returned by the agent remains the
	// source of truth for support, available values, and the selected value.
	DefaultReasoningEffort string
	// ToolQuirks override the default title heuristics for this agent; nil
	// means DefaultToolQuirks. See ToolQuirks for why this lives here.
	ToolQuirks *ToolQuirks
	// ForceHTTPMCPServer sends HTTP MCP servers even when the agent omits
	// mcpCapabilities.http. This is for agents that accept session/new
	// mcpServers but do not advertise the capability yet.
	ForceHTTPMCPServer bool
	ManagedFields      []ManagedField
	SupportedBackends  []string
	SetupModes         []string
}

type ManagedField struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
} // @name acpprofile.ManagedField

type PublicProfile struct {
	ID                string         `json:"id"`
	DisplayName       string         `json:"display_name"`
	Description       string         `json:"description,omitempty"`
	ManagedFields     []ManagedField `json:"managed_fields,omitempty"`
	SupportedBackends []string       `json:"supported_backends,omitempty"`
	SetupModes        []string       `json:"setup_modes,omitempty"`
} // @name acpprofile.PublicProfile

type ProfilesResponse struct {
	Items []PublicProfile `json:"items"`
} // @name acpprofile.ProfilesResponse

type AgentSetup struct {
	AgentID string
	Enabled bool
	Mode    string
	// ModeSet is true only when setup_mode was explicitly present in the bot
	// metadata. When false the Mode field carries the package default (api_key)
	// and callers that need to distinguish "explicitly api_key" from "legacy /
	// unset" should check this flag rather than comparing Mode directly.
	ModeSet bool
	Managed map[string]string
}

// MissingRequiredManagedField returns the first profile field required for the
// selected setup mode that has not been configured. It mirrors the frontend
// lightweight validation and intentionally does not inspect OAuth/token files
// inside the workspace; runtime readiness owns those checks.
func MissingRequiredManagedField(profile Profile, setup AgentSetup) (ManagedField, bool) {
	mode := normalizeSetupMode(setup.Mode, setup.Managed)
	if mode == setupModeSelf {
		return ManagedField{}, false
	}
	managed := setup.Managed
	if managed == nil {
		managed = map[string]string{}
	}
	switch NormalizeAgentID(profile.ID) {
	case AgentCodexID:
		if mode == setupModeOAuth {
			return ManagedField{}, false
		}
		if strings.TrimSpace(managed["api_key"]) == "" {
			return managedFieldOrFallback(profile, "api_key", ManagedField{
				ID:        "api_key",
				Label:     "OpenAI API key",
				Type:      "password",
				Required:  true,
				Sensitive: true,
			}), true
		}
		return ManagedField{}, false
	case AgentClaudeCodeID:
		fieldID := "api_key"
		if mode == setupModeOAuth {
			fieldID = "oauth_token"
		}
		if strings.TrimSpace(managed[fieldID]) == "" {
			return managedFieldOrFallback(profile, fieldID, ManagedField{
				ID:        fieldID,
				Label:     fieldID,
				Type:      "password",
				Required:  true,
				Sensitive: true,
			}), true
		}
		return ManagedField{}, false
	}
	for _, field := range profile.ManagedFields {
		id := NormalizeAgentID(field.ID)
		if id == "" || !field.Required {
			continue
		}
		if strings.TrimSpace(managed[id]) == "" {
			return field, true
		}
	}
	return ManagedField{}, false
}

// MissingRequiredManagedFieldForPreflight applies only the checks that can be
// decided without a workspace backend. Legacy metadata with no explicit
// setup_mode is resolved by the runtime pool because legacy metadata may omit it.
func MissingRequiredManagedFieldForPreflight(profile Profile, setup AgentSetup) (ManagedField, bool) {
	if !setup.ModeSet {
		return ManagedField{}, false
	}
	return MissingRequiredManagedField(profile, setup)
}

func managedFieldOrFallback(profile Profile, fieldID string, fallback ManagedField) ManagedField {
	fieldID = NormalizeAgentID(fieldID)
	for _, field := range profile.ManagedFields {
		if NormalizeAgentID(field.ID) == fieldID {
			return field
		}
	}
	return fallback
}

// registry holds all known ACP agent profiles keyed by NormalizeAgentID.
// It is initialised via init() in this package; downstream code should only
// access it via Lookup / List / Register so we keep the registration logic
// in a single place.
var registry = map[string]Profile{}

func init() {
	Register(codexProfile())
	Register(claudeCodeProfile())
	Register(hermesProfile())
}

// Register adds (or replaces) a profile in the registry. Intended to be
// called from package init() blocks. Profiles with an empty ID are ignored.
func Register(profile Profile) {
	id := NormalizeAgentID(profile.ID)
	if id == "" {
		return
	}
	profile.ID = id
	registry[id] = profile
}

func codexProfile() Profile {
	return Profile{
		ID:             AgentCodexID,
		DisplayName:    AgentCodexName,
		Description:    "OpenAI Codex ACP adapter",
		DynamicCommand: "npx",
		DynamicArgs: []string{
			"-y",
		},
		DynamicPackage:         "@agentclientprotocol/codex-acp",
		Command:                "codex-acp",
		DefaultReasoningEffort: "medium",
		ManagedFields: []ManagedField{
			{
				ID:          "api_key",
				Label:       "OpenAI API key",
				Type:        "password",
				Sensitive:   true,
				Placeholder: "sk-...",
				Help:        "Used by API key setup to authenticate Codex.",
			},
			{
				ID:          "base_url",
				Label:       "OpenAI base URL",
				Type:        "url",
				Placeholder: "https://api.openai.com/v1",
				Help:        "Optional Codex provider base URL.",
			},
		},
		SupportedBackends: []string{"container"},
		SetupModes:        []string{setupModeAPIKey, setupModeOAuth, setupModeSelf},
	}
}

func claudeCodeProfile() Profile {
	return Profile{
		ID:             AgentClaudeCodeID,
		DisplayName:    AgentClaudeCodeName,
		Description:    "Claude Code ACP adapter",
		DynamicCommand: "npx",
		DynamicArgs: []string{
			"-y",
		},
		DynamicPackage: "@agentclientprotocol/claude-agent-acp",
		Command:        "claude-agent-acp",
		// "default" routes every gated tool through session/request_permission;
		// without the pin a host-level Claude settings file (defaultMode auto /
		// acceptEdits) silently bypasses Memoh's approval flow.
		SessionModeID: "default",
		// Newer Claude models gate extended thinking on the effort level, not
		// MAX_THINKING_TOKENS. The ID remains as a compatibility fallback for
		// older/custom adapters that omit ACP's thought_level category.
		ReasoningConfigID:      "effort",
		DefaultReasoningEffort: "high",
		ManagedFields: []ManagedField{
			{
				ID:          "api_key",
				Label:       "Anthropic API key",
				Type:        "password",
				Required:    true,
				Sensitive:   true,
				Placeholder: "sk-ant-...",
				Help:        "Used by API key setup to authenticate Claude Code.",
			},
			{
				ID:          "base_url",
				Label:       "Anthropic base URL",
				Type:        "url",
				Placeholder: "https://api.anthropic.com",
				Help:        "Optional Claude Code API endpoint override.",
			},
			{
				ID:          "oauth_token",
				Label:       "Claude Code OAuth token",
				Type:        "password",
				Required:    true,
				Sensitive:   true,
				Placeholder: "Token from claude setup-token",
				Help:        "Used by OAuth setup to authenticate Claude Code.",
			},
		},
		SupportedBackends: []string{"container"},
		SetupModes:        []string{setupModeAPIKey, setupModeOAuth, setupModeSelf},
	}
}

func hermesProfile() Profile {
	return Profile{
		ID:          AgentHermesID,
		DisplayName: AgentHermesName,
		Description: "Hermes Agent ACP adapter",
		Command:     "hermes-acp",
		ToolQuirks: &ToolQuirks{
			WriteTitleKeywords: []string{"write", "write file", "create", "create file", "new file"},
			GenericExecTitles: []string{
				"shell", "shell command", "command", "run", "run command",
				"execute", "exec", "bash", "terminal", "terminal command",
				"execute_code", "execute code", "python", "python code",
			},
		},
		ForceHTTPMCPServer: true,
		ManagedFields: []ManagedField{
			{
				ID:          "provider",
				Label:       "Provider",
				Type:        "text",
				Required:    true,
				Placeholder: "gemini",
				Help:        "Select Gemini, OpenRouter, OpenAI API, or a custom OpenAI-compatible endpoint.",
			},
			{
				ID:          "model",
				Label:       "Model",
				Type:        "text",
				Required:    true,
				Placeholder: "gemini-3.5-flash",
				Help:        "Hermes model name for managed sessions.",
			},
			{
				ID:          "base_url",
				Label:       "Base URL",
				Type:        "url",
				Placeholder: "https://api.example.com/v1",
				Help:        "Only required when Provider is Custom endpoint.",
			},
			{
				ID:          "api_key",
				Label:       "API key",
				Type:        "password",
				Required:    true,
				Sensitive:   true,
				Placeholder: "sk-...",
				Help:        "Written to the bot-scoped Hermes .env file.",
			},
		},
		SupportedBackends: []string{"container"},
		SetupModes:        []string{setupModeSelf, setupModeAPIKey},
	}
}

// List returns all registered public profiles, sorted by ID for stable
// API responses.
func List() []PublicProfile {
	out := make([]PublicProfile, 0, len(registry))
	for _, profile := range registry {
		out = append(out, profile.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup returns the registered profile for id (case-insensitive).
func Lookup(id string) (Profile, bool) {
	id = NormalizeAgentID(id)
	profile, ok := registry[id]
	return profile, ok
}

func ShouldForceHTTPMCPServer(agentID string) bool {
	profile, ok := Lookup(agentID)
	return ok && profile.ForceHTTPMCPServer
}

func (p Profile) Public() PublicProfile {
	return PublicProfile{
		ID:                p.ID,
		DisplayName:       p.DisplayName,
		Description:       p.Description,
		ManagedFields:     append([]ManagedField(nil), p.ManagedFields...),
		SupportedBackends: append([]string(nil), p.SupportedBackends...),
		SetupModes:        append([]string(nil), p.SetupModes...),
	}
}

func MetadataAgentEnabled(metadata map[string]any, agentID string) bool {
	setup := ParseAgentSetup(metadata, agentID)
	return setup.Enabled
}

func MetadataAgentEnabledRaw(raw []byte, agentID string) bool {
	if len(raw) == 0 {
		return false
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return false
	}
	return MetadataAgentEnabled(metadata, agentID)
}

func ParseAgentSetup(metadata map[string]any, agentID string) AgentSetup {
	agentID = NormalizeAgentID(agentID)
	setup := AgentSetup{
		AgentID: agentID,
		Mode:    setupModeAPIKey,
		Managed: map[string]string{},
	}
	if agentID == "" {
		return setup
	}
	acpConfig, ok := metadataRecord(metadata[MetadataKeyACP])
	if !ok {
		return setup
	}

	if agents, ok := metadataRecord(acpConfig["agents"]); ok {
		if agentConfig, ok := metadataRecord(agents[agentID]); ok {
			if enabled, ok := metadataBool(agentConfig["enabled"]); ok {
				setup.Enabled = enabled
			}
			if mode, ok := agentConfig["setup_mode"].(string); ok && strings.TrimSpace(mode) != "" {
				setup.Mode = strings.TrimSpace(strings.ToLower(mode))
				setup.ModeSet = true
			}
			if managed, ok := metadataRecord(agentConfig["managed"]); ok {
				for key, value := range managed {
					if s, ok := value.(string); ok {
						setup.Managed[key] = s
					}
				}
			}
			setup.Mode = normalizeSetupMode(setup.Mode, setup.Managed)
			return setup
		}
	}

	return setup
}

func normalizeSetupMode(mode string, managed map[string]string) string {
	mode = NormalizeAgentID(mode)
	switch mode {
	case setupModeOAuth, setupModeSelf:
		return mode
	case "managed":
		authType := NormalizeAgentID(managed["auth_type"])
		if authType == "provider_oauth" || authType == setupModeOAuth {
			return setupModeOAuth
		}
		return setupModeAPIKey
	case setupModeAPIKey, "":
		return setupModeAPIKey
	default:
		return mode
	}
}

func NormalizeAgentID(agentID string) string {
	return strings.ToLower(strings.TrimSpace(agentID))
}

func ScrubMetadataForResponse(metadata map[string]any) map[string]any {
	cloned := cloneMap(metadata)
	acpConfig, ok := metadataRecord(cloned[MetadataKeyACP])
	if !ok {
		return cloned
	}
	agents, ok := metadataRecord(acpConfig["agents"])
	if !ok {
		return cloned
	}
	for rawAgentID, rawAgent := range agents {
		agentConfig, ok := metadataRecord(rawAgent)
		if !ok {
			continue
		}
		managed, ok := metadataRecord(agentConfig["managed"])
		if !ok {
			continue
		}
		profile, _ := Lookup(rawAgentID)
		sensitive := sensitiveFieldSet(profile)
		for key, value := range managed {
			if !sensitive[key] && !looksSensitiveKey(key) {
				continue
			}
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				managed[key] = maskSecret(s)
			}
		}
	}
	return cloned
}

func ScrubMetadataForExport(metadata map[string]any) (map[string]any, bool) {
	cloned := cloneMap(metadata)
	acpConfig, ok := metadataRecord(cloned[MetadataKeyACP])
	if !ok {
		return cloned, false
	}
	agents, ok := metadataRecord(acpConfig["agents"])
	if !ok {
		return cloned, false
	}
	changed := false
	for rawAgentID, rawAgent := range agents {
		agentConfig, ok := metadataRecord(rawAgent)
		if !ok {
			continue
		}
		managed, ok := metadataRecord(agentConfig["managed"])
		if !ok {
			continue
		}
		profile, _ := Lookup(rawAgentID)
		sensitive := sensitiveFieldSet(profile)
		for key := range managed {
			if !sensitive[key] && !looksSensitiveKey(key) {
				continue
			}
			delete(managed, key)
			changed = true
		}
	}
	return cloned, changed
}

func MergeSensitiveFieldsForUpdate(existing, incoming map[string]any) map[string]any {
	merged := cloneMap(incoming)
	existingACP, okExistingACP := metadataRecord(existing[MetadataKeyACP])
	incomingACP, okIncomingACP := metadataRecord(merged[MetadataKeyACP])
	if !okExistingACP || !okIncomingACP {
		return merged
	}
	existingAgents, okExistingAgents := metadataRecord(existingACP["agents"])
	incomingAgents, okIncomingAgents := metadataRecord(incomingACP["agents"])
	if !okExistingAgents || !okIncomingAgents {
		return merged
	}

	for rawAgentID, rawIncomingAgent := range incomingAgents {
		incomingAgent, ok := metadataRecord(rawIncomingAgent)
		if !ok {
			continue
		}
		incomingManaged, ok := metadataRecord(incomingAgent["managed"])
		if !ok {
			continue
		}
		existingAgent, ok := metadataRecord(existingAgents[rawAgentID])
		if !ok {
			continue
		}
		existingManaged, ok := metadataRecord(existingAgent["managed"])
		if !ok {
			continue
		}
		profile, _ := Lookup(rawAgentID)
		sensitive := sensitiveFieldSet(profile)
		for key := range existingManaged {
			if !sensitive[key] && !looksSensitiveKey(key) {
				continue
			}
			value, exists := incomingManaged[key]
			switch {
			case !exists:
				incomingManaged[key] = existingManaged[key]
			case value == nil:
				delete(incomingManaged, key)
			case isMaskedSecretValue(value):
				incomingManaged[key] = existingManaged[key]
			case isEmptyString(value):
				incomingManaged[key] = existingManaged[key]
			}
		}
	}
	return merged
}

func sensitiveFieldSet(profile Profile) map[string]bool {
	out := map[string]bool{}
	for _, field := range profile.ManagedFields {
		if field.Sensitive || field.Type == "password" {
			out[field.ID] = true
		}
	}
	return out
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "sk-") && len(value) > 7 {
		return "sk-..." + value[len(value)-4:]
	}
	if len(value) > 4 {
		return "***" + value[len(value)-4:]
	}
	return "***"
}

func isMaskedSecretValue(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	s = strings.TrimSpace(s)
	if s == "***" {
		return true
	}
	if strings.HasPrefix(s, "sk-...") {
		return len([]rune(strings.TrimPrefix(s, "sk-..."))) == 4
	}
	if strings.HasPrefix(s, "***") {
		return len([]rune(strings.TrimPrefix(s, "***"))) == 4
	}
	return false
}

func isEmptyString(value any) bool {
	s, ok := value.(string)
	return ok && strings.TrimSpace(s) == ""
}

func looksSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "key") ||
		strings.Contains(key, "token") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "password")
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil || out == nil {
		out = map[string]any{}
	}
	return out
}

func metadataRecord(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	default:
		return nil, false
	}
}

func metadataBool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on", "enabled":
			return true, true
		case "false", "0", "no", "off", "disabled":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}
