package botbackup

import (
	"encoding/json"
)

func marshalJSON(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

func unmarshalJSON(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}

func roundTripJSON[T any](value any) (T, error) {
	var out T
	raw, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
