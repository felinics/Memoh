package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/channelpolicy"
	tools "github.com/memohai/memoh/internal/agent/tool"
	"github.com/memohai/memoh/internal/delivery"
)

const telegramStickerSendGuidance = " For Telegram, normally pair every coherent public message with at least one context-appropriate Sticker by setting `sticker_id` in the same send call; omit it only when the catalog has no suitable Sticker. Choose from the full visual descriptions, not from the original emoji alone, and avoid repetitive duplicates."

// mergeTelegramStickerSendTools converts the Sticker MCP into an internal
// backend for the first-party send tool. The model sees one send call whose
// sticker_id enum contains the complete, stable catalog; it never sees a
// search round or a second sticker-send tool.
func mergeTelegramStickerSendTools(session tools.SessionContext, sdkTools []sdk.Tool) []sdk.Tool {
	if !strings.EqualFold(strings.TrimSpace(session.CurrentPlatform), channelpolicy.TelegramPlatform) ||
		strings.TrimSpace(session.ReplyTarget) == "" {
		return sdkTools
	}

	visible := make([]sdk.Tool, 0, len(sdkTools))
	var backend *sdk.Tool
	for i := range sdkTools {
		tool := sdkTools[i]
		if channelpolicy.IsTelegramStickerSendTool(tool.Name) {
			if backend == nil && stickerPropertySchema(tool.Parameters) != nil && tool.Execute != nil {
				copyTool := tool
				backend = &copyTool
			}
			continue
		}
		if channelpolicy.IsTelegramStickerSearchTool(tool.Name) {
			continue
		}
		visible = append(visible, tool)
	}
	if backend == nil {
		return visible
	}

	for i := range visible {
		if strings.TrimSpace(visible[i].Name) != tools.ToolSend().String() || visible[i].Execute == nil {
			continue
		}
		parameters := cloneSchemaMap(visible[i].Parameters)
		properties, _ := parameters["properties"].(map[string]any)
		if properties == nil {
			continue
		}
		stickerSchema := cloneSchemaMap(stickerPropertySchema(backend.Parameters))
		if !normalizeStickerEnum(stickerSchema) {
			continue
		}
		stickerIDs := stickerEnumSet(stickerSchema)
		properties["sticker_id"] = stickerSchema
		visible[i].Parameters = parameters
		visible[i].Description = strings.TrimSpace(visible[i].Description) + telegramStickerSendGuidance

		sendExecute := visible[i].Execute
		stickerExecute := backend.Execute
		visible[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			args := normalizeToolInput(input)
			stickerID, _ := args["sticker_id"].(string)
			stickerID = strings.ToUpper(strings.TrimSpace(stickerID))
			if stickerID == "" {
				return sendExecute(ctx, input)
			}
			if _, ok := stickerIDs[stickerID]; !ok {
				return nil, fmt.Errorf("sticker_id %q is not in the current catalog", stickerID)
			}
			platform, _ := args["platform"].(string)
			target, _ := args["target"].(string)
			if !delivery.IsSameConversation(
				session.CurrentPlatform, session.ReplyTarget, platform, target,
			) {
				return nil, errors.New("sticker_id is available only for the current Telegram conversation")
			}

			sendArgs := make(map[string]any, len(args)-1)
			for key, value := range args {
				if key != "sticker_id" {
					sendArgs[key] = value
				}
			}
			textDelivered := false
			if hasNativeSendContent(sendArgs) {
				output, err := sendExecute(ctx, sendArgs)
				if err != nil {
					return nil, err
				}
				if result, ok := output.(map[string]any); ok {
					if delivered, present := result["ok"].(bool); present && !delivered {
						return output, nil
					}
				}
				textDelivered = true
			}

			output, err := stickerExecute(ctx, map[string]any{
				"chat_id":    strings.TrimSpace(session.ReplyTarget),
				"sticker_id": stickerID,
			})
			if err != nil {
				if textDelivered {
					return partialTelegramStickerDelivery(session), nil
				}
				return nil, err
			}
			if err := telegramStickerBackendError(output); err != nil {
				if textDelivered {
					return partialTelegramStickerDelivery(session), nil
				}
				return nil, err
			}
			return map[string]any{
				"ok":                true,
				"text_delivered":    textDelivered,
				"sticker_delivered": true,
				"platform":          channelpolicy.TelegramPlatform,
				"target":            strings.TrimSpace(session.ReplyTarget),
			}, nil
		}
		break
	}
	return visible
}

func stickerEnumSet(schema map[string]any) map[string]struct{} {
	result := map[string]struct{}{}
	switch values := schema["enum"].(type) {
	case []string:
		for _, value := range values {
			result[strings.ToUpper(strings.TrimSpace(value))] = struct{}{}
		}
	case []any:
		for _, raw := range values {
			value, _ := raw.(string)
			result[strings.ToUpper(strings.TrimSpace(value))] = struct{}{}
		}
	}
	delete(result, "")
	return result
}

func partialTelegramStickerDelivery(session tools.SessionContext) map[string]any {
	return map[string]any{
		"ok":                false,
		"text_delivered":    true,
		"sticker_delivered": false,
		"error_code":        "telegram_sticker_delivery_failed",
		"platform":          channelpolicy.TelegramPlatform,
		"target":            strings.TrimSpace(session.ReplyTarget),
	}
}

func stickerPropertySchema(parameters any) map[string]any {
	root := cloneSchemaMap(parameters)
	properties, _ := root["properties"].(map[string]any)
	property, _ := properties["sticker_id"].(map[string]any)
	return property
}

func cloneSchemaMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	if json.Unmarshal(raw, &cloned) != nil {
		return nil
	}
	return cloned
}

func normalizeStickerEnum(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	raw, ok := schema["enum"].([]any)
	if !ok {
		return false
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		id, _ := item.(string)
		id = strings.ToUpper(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return false
	}
	sort.Strings(ids)
	schema["enum"] = ids
	return true
}

func normalizeToolInput(input any) map[string]any {
	if args, ok := input.(map[string]any); ok {
		return args
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return map[string]any{}
	}
	return args
}

func hasNativeSendContent(args map[string]any) bool {
	if text, _ := args["text"].(string); strings.TrimSpace(text) != "" {
		return true
	}
	if raw, present := args["attachments"]; present && nonEmptyToolValue(raw) {
		return true
	}
	if message, ok := args["message"].(map[string]any); ok {
		for _, key := range []string{"text", "parts", "attachments", "actions"} {
			if nonEmptyToolValue(message[key]) {
				return true
			}
		}
	}
	return false
}

func nonEmptyToolValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func telegramStickerBackendError(output any) error {
	result, ok := output.(map[string]any)
	if !ok {
		return nil
	}
	if isError, _ := result["isError"].(bool); isError {
		return fmt.Errorf("telegram sticker delivery failed: %s", stickerBackendErrorText(result))
	}
	if okValue, present := result["ok"].(bool); present && !okValue {
		return errors.New("telegram sticker delivery failed")
	}
	return nil
}

func stickerBackendErrorText(result map[string]any) string {
	if content, ok := result["content"].([]map[string]any); ok {
		for _, item := range content {
			if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	if content, ok := result["content"].([]any); ok {
		for _, raw := range content {
			if item, ok := raw.(map[string]any); ok {
				if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return "backend returned an error"
}
