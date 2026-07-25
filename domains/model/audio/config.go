package audio

// VoiceConfig is kept for backward compatibility with the legacy Edge adapter tests.
type VoiceConfig struct {
	ID   string `json:"id"`
	Lang string `json:"lang"`
}

// AudioConfig is kept for backward compatibility with the legacy Edge adapter tests.
type AudioConfig struct {
	Format     string      `json:"format"`
	SampleRate int         `json:"sample_rate"`
	Speed      float64     `json:"speed"`
	Pitch      float64     `json:"pitch"`
	Voice      VoiceConfig `json:"voice"`
}

func (AudioConfig) Validate() error { return nil }

// FieldSchema describes a single dynamic speech config field.
type FieldSchema struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Advanced    bool     `json:"advanced,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Example     any      `json:"example,omitempty"`
	Order       int      `json:"order"`
}

type ConfigSchema struct {
	Fields []FieldSchema `json:"fields"`
}

// ParamConstraint describes valid values for a numeric parameter.
// If Options is non-empty, only those discrete values are allowed.
type ParamConstraint struct {
	Options []float64 `json:"options,omitempty"`
	Min     float64   `json:"min,omitempty"`
	Max     float64   `json:"max,omitempty"`
	Default float64   `json:"default"`
}

// ModelCapabilities exposes optional UX hints for speech config forms.
type ModelCapabilities struct {
	ConfigSchema ConfigSchema      `json:"config_schema,omitempty"`
	Voices       []VoiceInfo       `json:"voices,omitempty"`
	Formats      []string          `json:"formats,omitempty"`
	Speed        *ParamConstraint  `json:"speed,omitempty"`
	Pitch        *ParamConstraint  `json:"pitch,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ModelInfo describes a single speech model exposed by a provider definition.
type ModelInfo struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	TemplateOnly bool              `json:"template_only,omitempty"`
	ConfigSchema ConfigSchema      `json:"config_schema,omitempty"`
	Capabilities ModelCapabilities `json:"capabilities"`
}

type VoiceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Lang string `json:"lang"`
}
