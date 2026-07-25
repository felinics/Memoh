package network

import (
	"errors"
	"fmt"
	"strings"
)

type FieldType string

const (
	FieldTypeString   FieldType = "string"
	FieldTypeSecret   FieldType = "secret"
	FieldTypeNumber   FieldType = "number"
	FieldTypeBool     FieldType = "bool"
	FieldTypeEnum     FieldType = "enum"
	FieldTypeTextarea FieldType = "textarea"
)

type ActionType string

type StatusState string

const (
	ActionTypeTestConnection ActionType = "test_connection"

	StatusStateUnknown      StatusState = "unknown"
	StatusStateReady        StatusState = "ready"
	StatusStateNeedsConfig  StatusState = "needs_config"
	StatusStateUnauthorized StatusState = "unauthorized"
)

type ConfigConstraint struct {
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Step *float64 `json:"step,omitempty"`
}

type ConfigField struct {
	Key         string            `json:"key"`
	Type        FieldType         `json:"type"`
	Required    bool              `json:"required,omitempty"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Placeholder string            `json:"placeholder,omitempty"`
	Default     any               `json:"default,omitempty"`
	Example     any               `json:"example,omitempty"`
	Order       int               `json:"order,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
	Multiline   bool              `json:"multiline,omitempty"`
	Readonly    bool              `json:"readonly,omitempty"`
	Secret      bool              `json:"secret,omitempty"`
	Collapsed   bool              `json:"collapsed,omitempty"`
	Constraint  *ConfigConstraint `json:"constraint,omitempty"`
}

func (f ConfigField) Validate() error {
	if strings.TrimSpace(f.Key) == "" {
		return errors.New("config field key is required")
	}
	switch f.Type {
	case FieldTypeString, FieldTypeSecret, FieldTypeNumber, FieldTypeBool, FieldTypeEnum, FieldTypeTextarea:
	default:
		return fmt.Errorf("unsupported config field type %q", f.Type)
	}
	return nil
}

type ConfigSchema struct {
	Version int           `json:"version"`
	Title   string        `json:"title,omitempty"`
	Fields  []ConfigField `json:"fields"`
}

func (s ConfigSchema) Validate() error {
	if s.Version <= 0 {
		return errors.New("schema version must be greater than 0")
	}
	keys := make(map[string]struct{}, len(s.Fields))
	for _, field := range s.Fields {
		if err := field.Validate(); err != nil {
			return err
		}
		if _, exists := keys[field.Key]; exists {
			return fmt.Errorf("duplicate field key %q", field.Key)
		}
		keys[field.Key] = struct{}{}
	}
	return nil
}

type ProviderActionStatus struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

type ProviderAction struct {
	ID          string                `json:"id"`
	Type        ActionType            `json:"type"`
	Label       string                `json:"label"`
	Description string                `json:"description,omitempty"`
	Primary     bool                  `json:"primary,omitempty"`
	Status      *ProviderActionStatus `json:"status,omitempty"`
}

type ProviderStatus struct {
	State       StatusState    `json:"state"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

type ProviderActionExecution struct {
	ActionID string         `json:"action_id"`
	Status   ProviderStatus `json:"status"`
	Output   map[string]any `json:"output,omitempty"`
}

func NormalizeConfigBySchema(schema ConfigSchema, raw map[string]any) map[string]any {
	out := cloneMap(raw)
	for _, field := range schema.Fields {
		value, exists := out[field.Key]
		if !exists {
			if field.Default != nil {
				out[field.Key] = field.Default
			}
			continue
		}
		switch field.Type {
		case FieldTypeString, FieldTypeSecret, FieldTypeTextarea, FieldTypeEnum:
			if s, ok := value.(string); ok {
				out[field.Key] = strings.TrimSpace(s)
			}
		case FieldTypeBool:
			if b, ok := value.(bool); ok {
				out[field.Key] = b
			}
		case FieldTypeNumber:
			if num, ok := toFloat64(value); ok {
				out[field.Key] = num
			}
		}
	}
	return out
}

func ValidateConfigBySchema(schema ConfigSchema, raw map[string]any) error {
	if err := schema.Validate(); err != nil {
		return err
	}
	for _, field := range schema.Fields {
		value, exists := raw[field.Key]
		if field.Required && (!exists || isEmptyConfigValue(value)) {
			return fmt.Errorf("%s is required", field.Key)
		}
		if !exists || value == nil {
			continue
		}
		switch field.Type {
		case FieldTypeString, FieldTypeSecret, FieldTypeTextarea, FieldTypeEnum:
			s, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s must be a string", field.Key)
			}
			s = strings.TrimSpace(s)
			if field.Type == FieldTypeEnum && len(field.Enum) > 0 && s != "" && !containsString(field.Enum, s) {
				return fmt.Errorf("%s must be one of %s", field.Key, strings.Join(field.Enum, ", "))
			}
		case FieldTypeBool:
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a bool", field.Key)
			}
		case FieldTypeNumber:
			num, ok := toFloat64(value)
			if !ok {
				return fmt.Errorf("%s must be a number", field.Key)
			}
			if field.Constraint != nil {
				if field.Constraint.Min != nil && num < *field.Constraint.Min {
					return fmt.Errorf("%s must be >= %v", field.Key, *field.Constraint.Min)
				}
				if field.Constraint.Max != nil && num > *field.Constraint.Max {
					return fmt.Errorf("%s must be <= %v", field.Key, *field.Constraint.Max)
				}
			}
		}
	}
	return nil
}

func cloneMap(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func isEmptyConfigValue(v any) bool {
	switch val := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(val) == ""
	}
	return false
}

func toFloat64(v any) (float64, bool) {
	switch num := v.(type) {
	case float64:
		return num, true
	case float32:
		return float64(num), true
	case int:
		return float64(num), true
	case int32:
		return float64(num), true
	case int64:
		return float64(num), true
	case uint:
		return float64(num), true
	case uint32:
		return float64(num), true
	case uint64:
		return float64(num), true
	default:
		return 0, false
	}
}
