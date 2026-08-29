package application

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
)

func sanitizeMessages(messages []ModelMessage) []ModelMessage {
	cleaned := make([]ModelMessage, 0, len(messages))
	for _, msg := range messages {
		msg = normalizeUserMessageContent(msg)
		if normalized, ok := normalizeImagePartsToDataURL(msg); ok {
			msg = normalized
		}
		if strings.TrimSpace(msg.Role) == "" {
			continue
		}
		if !msg.HasContent() && strings.TrimSpace(msg.ToolCallID) == "" {
			continue
		}
		cleaned = append(cleaned, msg)
	}
	return cleaned
}

func normalizeImagePartsToDataURL(msg ModelMessage) (ModelMessage, bool) {
	if len(msg.Content) == 0 {
		return msg, false
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(msg.Content, &parts); err != nil || len(parts) == 0 {
		return msg, false
	}

	changed := false
	for i := range parts {
		partTypeRaw, ok := parts[i]["type"]
		if !ok {
			continue
		}
		var partType string
		if err := json.Unmarshal(partTypeRaw, &partType); err != nil || !strings.EqualFold(partType, "image") {
			continue
		}

		imageRaw, ok := parts[i]["image"]
		if !ok || len(imageRaw) == 0 {
			continue
		}
		var tmp string
		if json.Unmarshal(imageRaw, &tmp) == nil {
			continue
		}

		var payload []byte
		if b, ok := decodeIndexedByteObject(imageRaw); ok {
			payload = b
		} else if b, ok := decodeByteArray(imageRaw); ok {
			payload = b
		} else {
			continue
		}
		if len(payload) == 0 {
			continue
		}

		// action trigger to image only here.
		mediaType := "application/octet-stream"
		if mediaTypeRaw, ok := parts[i]["mediaType"]; ok {
			var mt string
			if err := json.Unmarshal(mediaTypeRaw, &mt); err == nil && strings.TrimSpace(mt) != "" {
				mediaType = strings.TrimSpace(mt)
			}
		}
		dataURL := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(payload)
		rebuilt, err := json.Marshal(dataURL)
		if err != nil {
			continue
		}
		parts[i]["image"] = rebuilt
		changed = true
	}

	if !changed {
		return msg, false
	}
	rebuiltContent, err := json.Marshal(parts)
	if err != nil {
		return msg, false
	}
	msg.Content = rebuiltContent
	return msg, true
}

func decodeByteArray(raw json.RawMessage) ([]byte, bool) {
	var arr []int
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	if len(arr) == 0 {
		return nil, false
	}
	out := make([]byte, len(arr))
	for i, v := range arr {
		if v < 0 || v > 255 {
			return nil, false
		}
		out[i] = byte(v)
	}
	return out, true
}

func decodeIndexedByteObject(raw json.RawMessage) ([]byte, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return nil, false
	}
	type indexedByte struct {
		idx int
		val byte
	}
	items := make([]indexedByte, 0, len(obj))
	for k, vRaw := range obj {
		idx, err := strconv.Atoi(k)
		if err != nil || idx < 0 {
			return nil, false
		}
		var val int
		if err := json.Unmarshal(vRaw, &val); err != nil || val < 0 || val > 255 {
			return nil, false
		}
		items = append(items, indexedByte{idx: idx, val: byte(val)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].idx < items[j].idx })
	for i := range items {
		if items[i].idx != i {
			return nil, false
		}
	}
	out := make([]byte, len(items))
	for i := range items {
		out[i] = items[i].val
	}
	return out, true
}

func nonNilModelMessages(m []ModelMessage) []ModelMessage {
	if m == nil {
		return []ModelMessage{}
	}
	return m
}

func parseServiceUUID(id string) (pgtype.UUID, error) {
	if strings.TrimSpace(id) == "" {
		return pgtype.UUID{}, errors.New("empty id")
	}
	return db.ParseUUID(id)
}
