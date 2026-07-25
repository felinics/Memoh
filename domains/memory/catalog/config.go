package catalog

import "strings"

// StringFromConfig extracts a trimmed string value from a config map.
func StringFromConfig(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	v, ok := config[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
