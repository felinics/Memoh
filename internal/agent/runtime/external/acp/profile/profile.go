package profile

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/felinics/memoh/internal/errs"
)

const (
	AgentACPID   = "acp"
	AgentACPName = "ACP"

	MetadataKeyACP = "acp"

	setupModeAPIKey = "api_key"
	setupModeOAuth  = "oauth"
	setupModeSelf   = "self"
)

// Profile is how Memoh runs a user-configured ACP agent: the one generic
// profile, whose command and arguments the agent instance supplies.
type Profile struct {
	ID                string
	DisplayName       string
	Description       string
	ManagedFields     []ManagedField
	SupportedBackends []string
	SetupModes        []string
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
	// Mode is the setup mode, api_key when the metadata names none.
	Mode    string
	Managed map[string]string
}

// CheckSetup reports why setup cannot start the generic agent: a setup mode
// it does not support, or a required field left empty.
func CheckSetup(setup AgentSetup) error {
	if !slices.Contains(generic.SetupModes, setup.Mode) {
		return errs.New(fmt.Sprintf("%s does not support setup mode %q", generic.DisplayName, setup.Mode))
	}
	if field, missing := MissingRequiredManagedField(generic, setup); missing {
		return errs.New(field.ID + " required")
	}
	return nil
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

// generic is the only ACP profile: the built-in external agents (Codex,
// Claude Code, Grok) run as their own runtimes, and ACP serves
// user-configured agents.
var generic = genericACPProfile()

func genericACPProfile() Profile {
	return Profile{
		ID:          AgentACPID,
		DisplayName: AgentACPName,
		Description: "Run a custom Agent Client Protocol command",
		ManagedFields: []ManagedField{
			{
				ID:          commandFieldID,
				Label:       "Command",
				Type:        "text",
				Required:    true,
				Placeholder: "my-agent-acp",
				Help:        "Executable name or path for the ACP agent.",
			},
			{
				ID:          argumentsFieldID,
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

// List returns the public profiles.
func List() []PublicProfile {
	return []PublicProfile{generic.Public()}
}

// Lookup returns the profile for id (case-insensitive).
func Lookup(id string) (Profile, bool) {
	return generic, NormalizeAgentID(id) == AgentACPID
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

// InstanceManagedKey holds an agent instance's own managed fields in
// bot_agents.metadata.
const InstanceManagedKey = "managed"

// ParseInstanceSetup resolves the setup one agent instance launches with.
// Setups used to live on the bot keyed by profile, so every instance of the
// generic profile launched the same command. An instance now owns its managed
// fields; only instances that predate that still read the bot's profile slot.
func ParseInstanceSetup(botMetadata, agentMetadata map[string]any, agentID string) AgentSetup {
	managed, owns := metadataRecord(agentMetadata[InstanceManagedKey])
	if !owns {
		return ParseAgentSetup(botMetadata, agentID)
	}
	setup := AgentSetup{
		AgentID: NormalizeAgentID(agentID),
		Enabled: true,
		Mode:    setupModeAPIKey,
		Managed: make(map[string]string, len(managed)),
	}
	for key, value := range managed {
		if s, ok := value.(string); ok {
			setup.Managed[key] = s
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
	for _, fields := range acpManagedRecords(cloned) {
		for key, value := range fields {
			if !looksSensitiveKey(key) {
				continue
			}
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				fields[key] = maskSecret(s)
			}
		}
	}
	return cloned
}

func ScrubMetadataForExport(metadata map[string]any) (map[string]any, bool) {
	cloned := cloneMap(metadata)
	changed := false
	for _, fields := range acpManagedRecords(cloned) {
		for key := range fields {
			if !looksSensitiveKey(key) {
				continue
			}
			delete(fields, key)
			changed = true
		}
	}
	return cloned, changed
}

// acpManagedRecords returns the managed records under
// metadata.acp.agents[*].managed. Legacy bots kept credentials there.
func acpManagedRecords(metadata map[string]any) []map[string]any {
	acpConfig, ok := metadataRecord(metadata[MetadataKeyACP])
	if !ok {
		return nil
	}
	agents, ok := metadataRecord(acpConfig["agents"])
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(agents))
	for _, rawAgent := range agents {
		agentConfig, ok := metadataRecord(rawAgent)
		if !ok {
			continue
		}
		if managed, ok := metadataRecord(agentConfig["managed"]); ok {
			out = append(out, managed)
		}
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
				restoreSensitiveFields(incomingManaged, existingManaged)
			}
		}
	}

	return merged
}

// restoreSensitiveFields carries stored secrets through an update whose
// payload echoes the scrubbed response: a missing, masked, or empty value
// keeps the stored one, and an explicit null clears it.
func restoreSensitiveFields(incoming, existing map[string]any) {
	for key := range existing {
		if !looksSensitiveKey(key) {
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
