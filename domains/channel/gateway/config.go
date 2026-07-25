package gateway

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// DecodeConfigMap unmarshals a JSON byte slice into a string-keyed map.
func DecodeConfigMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

// ReadString looks up the first matching key in a map and returns its string representation.
// It tries each key in order and converts non-string values using type-safe formatting.
func ReadString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch v := value.(type) {
			case string:
				return v
			case float64:
				return strconv.FormatFloat(v, 'f', -1, 64)
			case bool:
				return strconv.FormatBool(v)
			default:
				return fmt.Sprintf("%v", v)
			}
		}
	}
	return ""
}
