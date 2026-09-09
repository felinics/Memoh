package profile

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	AgentACPID   = "acp"
	AgentACPName = "ACP"

	MetadataKeyACP = "acp"

	setupModeAPIKey = "api_key"
	setupModeOAuth  = "oauth"
	setupModeSelf   = "self"
)

type Profile struct {
	ID          string
	DisplayName string
	Description string
	Launch      LaunchPolicy
	// SessionModeID, when set, is the ACP session mode Memoh pins right after
	// session/new so tool permissions flow through ACP regardless of ambient
	// agent-side configuration (e.g. a host ~/.claude/settings.json).
	SessionModeID string
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
	// RuntimeStorage is the internal allowlist and environment contract that
	// separates durable configuration/credentials from process-local state.
	RuntimeStorage    RuntimeStoragePolicy
	ManagedFields     []ManagedField
	SupportedBackends []string
	SetupModes        []string
}

// LaunchPolicy declares how an ACP profile resolves its process command. A
// pinned Command is used by built-in adapters. ManagedCommandField and
// ManagedArgumentsField let a profile opt into bot-metadata-driven launch
// configuration without teaching the runtime about that profile's ID.
type LaunchPolicy struct {
	Command               string
	ManagedCommandField   string
	ManagedArgumentsField string
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
// decided without a workspace backend. (The legacy no-setup-mode exemption
// died with the built-in profiles; the generic profile's fields are always
// explicit.)
func MissingRequiredManagedFieldForPreflight(profile Profile, setup AgentSetup) (ManagedField, bool) {
	return MissingRequiredManagedField(profile, setup)
}

// registry holds all known ACP agent profiles keyed by NormalizeAgentID.
// It is initialised via init() in this package; downstream code should only
// access it via Lookup / List / Register so we keep the registration logic
// in a single place.
var registry = map[string]Profile{}

func init() {
	// The built-in external agents (codex, claude-code) run as direct runtimes;
	// ACP serves user-configured custom agents through the generic profile.
	Register(genericACPProfile())
}

// Register adds (or replaces) a profile in the registry. Intended to be
// called from package init() blocks. Profiles with an empty ID are ignored.
func Register(profile Profile) {
	id := NormalizeAgentID(profile.ID)
	if id == "" {
		return
	}
	profile.ID = id
	if err := validateRuntimeStorage(profile); err != nil {
		panic(err)
	}
	registry[id] = profile
}

func genericACPProfile() Profile {
	return Profile{
		// Custom ACP adapters rarely advertise mcpCapabilities.http; forcing
		// the HTTP MCP server injection keeps Memoh tools reachable either way.
		ForceHTTPMCPServer: true,
		ID:                 AgentACPID,
		DisplayName:        AgentACPName,
		Description:        "Run a custom Agent Client Protocol command",
		Launch: LaunchPolicy{
			ManagedCommandField:   genericACPCommandFieldID,
			ManagedArgumentsField: genericACPArgumentsFieldID,
		},
		RuntimeStorage: genericACPRuntimeStorage(),
		ManagedFields: []ManagedField{
			{
				ID:          "command",
				Label:       "Command",
				Type:        "text",
				Required:    true,
				Placeholder: "my-agent-acp",
				Help:        "Executable name or path for the ACP agent.",
			},
			{
				ID:          "arguments",
				Label:       "Arguments",
				Type:        "textarea",
				Placeholder: "--stdio",
				Help:        "Optional process arguments, one argument per line.",
			},
		},
		SupportedBackends: []string{"container"},
		// api_key is an internal managed-mode marker here; generic ACP has no
		// authentication UI of its own and only needs Memoh-managed launch data.
		SetupModes: []string{setupModeAPIKey},
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
	for _, entry := range acpManagedRecords(cloned) {
		for key, value := range entry.fields {
			if !entry.sensitive[key] && !looksSensitiveKey(key) {
				continue
			}
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				entry.fields[key] = maskSecret(s)
			}
		}
	}
	return cloned
}

func ScrubMetadataForExport(metadata map[string]any) (map[string]any, bool) {
	cloned := cloneMap(metadata)
	changed := false
	for _, entry := range acpManagedRecords(cloned) {
		for key := range entry.fields {
			if !entry.sensitive[key] && !looksSensitiveKey(key) {
				continue
			}
			delete(entry.fields, key)
			changed = true
		}
	}
	return cloned, changed
}

// acpManagedRecords walks metadata.acp.agents[*].managed, pairing each
// managed record with its profile-declared sensitive field set.
type acpManagedRecord struct {
	fields    map[string]any
	sensitive map[string]bool
}

func acpManagedRecords(metadata map[string]any) []acpManagedRecord {
	acpConfig, ok := metadataRecord(metadata[MetadataKeyACP])
	if !ok {
		return nil
	}
	agents, ok := metadataRecord(acpConfig["agents"])
	if !ok {
		return nil
	}
	out := make([]acpManagedRecord, 0, len(agents))
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
		out = append(out, acpManagedRecord{fields: managed, sensitive: sensitiveFieldSet(profile)})
	}
	return out
}

func MergeSensitiveFieldsForUpdate(existing, incoming map[string]any) map[string]any {
	merged := cloneMap(incoming)
	existingACP, okExistingACP := metadataRecord(existing[MetadataKeyACP])
	incomingACP, okIncomingACP := metadataRecord(merged[MetadataKeyACP])
	if okExistingACP && okIncomingACP {
		existingAgents, okExistingAgents := metadataRecord(existingACP["agents"])
		incomingAgents, okIncomingAgents := metadataRecord(incomingACP["agents"])
		if okExistingAgents && okIncomingAgents {
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
				restoreSensitiveFields(incomingManaged, existingManaged, func(key string) bool {
					return sensitive[key] || looksSensitiveKey(key)
				})
			}
		}
	}

	return merged
}

// restoreSensitiveFields carries stored secrets through an update whose
// payload echoes the scrubbed response: a missing, masked, or empty value
// keeps the stored one, and an explicit null clears it.
func restoreSensitiveFields(incoming, existing map[string]any, isSensitive func(string) bool) {
	for key := range existing {
		if !isSensitive(key) {
			continue
		}
		value, exists := incoming[key]
		switch {
		case !exists:
			incoming[key] = existing[key]
		case value == nil:
			delete(incoming, key)
		case isMaskedSecretValue(value):
			incoming[key] = existing[key]
		case isEmptyString(value):
			incoming[key] = existing[key]
		}
	}
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
