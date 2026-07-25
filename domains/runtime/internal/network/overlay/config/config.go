package config

import "strings"

func String(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	value, ok := config[key]
	if !ok {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func Bool(config map[string]any, key string, defaults ...bool) bool {
	if config != nil {
		if value, ok := config[key]; ok {
			if b, ok := value.(bool); ok {
				return b
			}
		}
	}
	if len(defaults) > 0 {
		return defaults[0]
	}
	return false
}

func Int(config map[string]any, key string, defaultValue int) int {
	if config != nil {
		if value, ok := config[key]; ok {
			switch typed := value.(type) {
			case float64:
				return int(typed)
			case int:
				return typed
			}
		}
	}
	return defaultValue
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
