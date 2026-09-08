package trajectory

import "encoding/json"

func JSONBlock(kind, label string, value any) Block {
	data, err := json.Marshal(value)
	if err != nil {
		return Block{Kind: "capture_error", Label: label, Content: "json_encoding_failed"}
	}
	return Block{Kind: kind, Label: label, Format: "json", Content: string(data)}
}
