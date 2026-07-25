package plugins

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	StatusReady         = "ready"
	StatusNeedsConfig   = "needs_config"
	StatusNeedsAuth     = "needs_auth"
	StatusAdminRequired = "admin_required"
	StatusDisabled      = "disabled"
	StatusUninstalled   = "uninstalled"
)

type Icon struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

type Author struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type ConfigVar struct {
	Key          string `json:"key"`
	Description  string `json:"description,omitempty"`
	DefaultValue string `json:"defaultValue,omitempty"`
	Required     bool   `json:"required,omitempty"`
	Secret       bool   `json:"secret,omitempty"`
}

type AuthRequirement struct {
	Key       string   `json:"key"`
	Type      string   `json:"type"`
	ClientRef string   `json:"client_ref,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	Variables []string `json:"variables,omitempty"`
}

type MCPResource struct {
	Key          string      `json:"key"`
	Name         string      `json:"name,omitempty"`
	DisplayName  string      `json:"display_name,omitempty"`
	Description  string      `json:"description,omitempty"`
	Transport    string      `json:"transport,omitempty"`
	Command      string      `json:"command,omitempty"`
	Args         []string    `json:"args,omitempty"`
	Env          []ConfigVar `json:"env,omitempty"`
	Cwd          string      `json:"cwd,omitempty"`
	URL          string      `json:"url,omitempty"`
	Headers      []ConfigVar `json:"headers,omitempty"`
	AuthRef      string      `json:"auth_ref,omitempty"`
	Visibility   string      `json:"visibility,omitempty"`
	Capabilities []string    `json:"capabilities,omitempty"`
}

type SkillResource struct {
	Key  string `json:"key"`
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

type SkillEntry struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Content     string         `json:"content,omitempty"`
	Files       []string       `json:"files,omitempty"`
}

type InstallCommands []string

func (c *InstallCommands) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*c = nil
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var items []string
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		*c = normalizeInstallCommands(items)
		return nil
	}
	if strings.HasPrefix(raw, "\"") {
		var item string
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		*c = normalizeInstallCommands([]string{item})
		return nil
	}
	return errors.New("install must be a string or string array")
}

func (c InstallCommands) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string(c))
}

type Manifest struct {
	SchemaVersion    string            `json:"schema_version,omitempty"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Version          string            `json:"version,omitempty"`
	Description      string            `json:"description,omitempty"`
	Author           Author            `json:"author"`
	Icon             *Icon             `json:"icon,omitempty"`
	Homepage         string            `json:"homepage,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
	Capabilities     []string          `json:"capabilities,omitempty"`
	Install          InstallCommands   `json:"install,omitempty"`
	Variables        []ConfigVar       `json:"variables,omitempty"`
	AuthRequirements []AuthRequirement `json:"auth_requirements,omitempty"`
	MCPs             []MCPResource     `json:"mcps,omitempty"`
	Skills           []SkillResource   `json:"skills,omitempty"`
	BundledSkills    []SkillEntry      `json:"bundled_skills,omitempty"`
}

type InstallRequest struct {
	Manifest  Manifest          `json:"manifest"`
	Variables map[string]string `json:"variables,omitempty"`
}

type OAuthAuthorizeRequest struct {
	CallbackURL string `json:"callback_url,omitempty"`
}

type Resource struct {
	ID         string         `json:"id"`
	Type       string         `json:"resource_type"`
	Key        string         `json:"resource_key"`
	ResourceID string         `json:"resource_id"`
	Status     string         `json:"status"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type Installation struct {
	ID          string         `json:"id"`
	BotID       string         `json:"bot_id"`
	PluginID    string         `json:"plugin_id"`
	PluginName  string         `json:"plugin_name"`
	Version     string         `json:"version"`
	Status      string         `json:"status"`
	Enabled     bool           `json:"enabled"`
	Config      map[string]any `json:"config,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Manifest    Manifest       `json:"manifest"`
	Resources   []Resource     `json:"resources,omitempty"`
	InstalledAt time.Time      `json:"installed_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type ListResponse struct {
	Items []Installation `json:"items"`
}
