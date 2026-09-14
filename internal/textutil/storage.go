package textutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// StorageText makes binary bytes visible in text-only transports. PostgreSQL
// text and jsonb cannot represent NUL, even when JSON encodes it as an escape.
// Existing literal backslash sequences are left untouched.
func StorageText(text string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(text, "\uFFFD"), "\x00", `\x00`)
}

// StorageJSON normalizes string values before they reach jsonb. Decode only
// potentially unsafe input, preserving numeric precision and literal escapes.
func StorageJSON(raw []byte) ([]byte, error) {
	if !json.Valid(raw) {
		return nil, errors.New("invalid JSON content")
	}
	if utf8.Valid(raw) && !bytes.Contains(raw, []byte(`\u`)) {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value, err := storageJSONValue(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func storageJSONValue(value any) (any, error) {
	switch v := value.(type) {
	case string:
		return StorageText(v), nil
	case []any:
		for i, item := range v {
			clean, err := storageJSONValue(item)
			if err != nil {
				return nil, err
			}
			v[i] = clean
		}
	case map[string]any:
		clean := make(map[string]any, len(v))
		for key, item := range v {
			key = StorageText(key)
			if _, exists := clean[key]; exists {
				return nil, errors.New("JSON keys collide after text normalization")
			}
			next, err := storageJSONValue(item)
			if err != nil {
				return nil, err
			}
			clean[key] = next
		}
		return clean, nil
	}
	return value, nil
}
