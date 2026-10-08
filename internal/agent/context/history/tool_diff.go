package historyfrag

import (
	"bytes"
	"encoding/json"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/turn"
)

// toolCallDiffProviderKey is the providerMetadata entry the runtime uses to
// hand a tool call's UI-only unified diff to the harness.
const toolCallDiffProviderKey = "diff"

var toolCallDiffContentNeedle = []byte(`"diff"`)

// ExtractToolCallDiffs lifts UI-only diff payloads out of a stored assistant
// message's tool-call parts. The runtime annotates ToolCallPart
// ProviderMetadata with the diff so in-memory conversion can see it, but the
// same JSON is what lands in the history row's content column — and
// ListActiveSinceBySessionWithinBytes budgets by octet_length(content). The
// diff is display sugar, not model context, so persist-time callers move it
// onto the row's metadata column (message.ToolCallDiffsMetadataKey) instead.
//
// Returns the message unchanged and nil diffs for non-assistant rows, rows
// without extractable diffs, or content the codec does not recognize — the
// original payload is then persisted untouched, preserving old behavior.
func ExtractToolCallDiffs(message turn.ModelMessage) (turn.ModelMessage, map[string]string) {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") || len(message.Content) == 0 {
		return message, nil
	}
	if !bytes.Contains(message.Content, toolCallDiffContentNeedle) {
		return message, nil
	}
	var parts []map[string]any
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		return message, nil
	}
	var diffs map[string]string
	changed := false
	for _, part := range parts {
		partType, _ := part["type"].(string)
		if partType != "tool-call" {
			continue
		}
		metadata, ok := part["providerMetadata"].(map[string]any)
		if !ok {
			continue
		}
		diff, ok := metadata[toolCallDiffProviderKey].(string)
		if !ok || diff == "" {
			continue
		}
		callID, _ := part["toolCallId"].(string)
		if callID = strings.TrimSpace(callID); callID == "" {
			continue
		}
		delete(metadata, toolCallDiffProviderKey)
		if len(metadata) == 0 {
			delete(part, "providerMetadata")
		}
		if diffs == nil {
			diffs = make(map[string]string)
		}
		diffs[callID] = diff
		changed = true
	}
	if !changed {
		return message, nil
	}
	content, err := json.Marshal(parts)
	if err != nil {
		return message, nil
	}
	message.Content = content
	return message, diffs
}

// ExtractToolCallDiffsFromSDK is the sdk.Message variant of
// ExtractToolCallDiffs for call sites that hold typed messages (subagent
// step commits) rather than the stored model shape.
func ExtractToolCallDiffsFromSDK(message sdk.Message) (sdk.Message, map[string]string) {
	if message.Role != sdk.MessageRoleAssistant || len(message.Content) == 0 {
		return message, nil
	}
	var diffs map[string]string
	parts := message.Content
	copied := false
	for i, part := range parts {
		call, ok := part.(sdk.ToolCallPart)
		if !ok || len(call.ProviderMetadata) == 0 {
			continue
		}
		raw, ok := partmeta.Value(call.ProviderMetadata, toolCallDiffProviderKey)
		diff, _ := raw.(string)
		if !ok || diff == "" {
			continue
		}
		callID := strings.TrimSpace(call.ToolCallID)
		if callID == "" {
			continue
		}
		if !copied {
			parts = append([]sdk.MessagePart(nil), parts...)
			copied = true
		}
		call.ProviderMetadata = partmeta.Delete(call.ProviderMetadata, toolCallDiffProviderKey)
		parts[i] = call
		if diffs == nil {
			diffs = make(map[string]string)
		}
		diffs[callID] = diff
	}
	if len(diffs) == 0 {
		return message, nil
	}
	message.Content = parts
	return message, diffs
}
