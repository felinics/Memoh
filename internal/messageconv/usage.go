package messageconv

import (
	"encoding/json"

	"github.com/felinics/twilight/sdk"
)

func MarshalUsage(usage *sdk.Usage) json.RawMessage {
	if usage == nil {
		return nil
	}
	data, _ := json.Marshal(struct {
		*sdk.Usage
		InputTokenSemantics string `json:"inputTokenSemantics"`
	}{Usage: usage, InputTokenSemantics: "total"})
	return data
}
