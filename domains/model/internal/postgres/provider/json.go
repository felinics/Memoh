package provider

import "encoding/json"

func metadataJSON(metadata map[string]any) []byte {
	if metadata == nil {
		return []byte(`{}`)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return []byte(`{}`)
	}
	return data
}

func decodeMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return map[string]any{}
	}
	return metadata
}
