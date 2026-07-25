package gateway

// FieldType enumerates the supported configuration field types.
type FieldType string

const (
	FieldString FieldType = "string"
	FieldSecret FieldType = "secret"
	FieldBool   FieldType = "bool"
	FieldNumber FieldType = "number"
	FieldEnum   FieldType = "enum"
)

// FieldSchema describes a single configuration field.
type FieldSchema struct {
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Order       int       `json:"order,omitempty"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	Example     any       `json:"example,omitempty"`
}

// ConfigSchema describes the structure of a channel or user-binding configuration.
type ConfigSchema struct {
	Version int                    `json:"version"`
	Fields  map[string]FieldSchema `json:"fields"`
}
