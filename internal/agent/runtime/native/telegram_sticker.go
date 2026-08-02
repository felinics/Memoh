package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	tools "github.com/memohai/memoh/internal/agent/tool"
)

const telegramStickerSendToolSuffix = "send_telegram_sticker"

const telegramStickerSendGuidance = " For Telegram, normally pair every coherent public message with at least one context-appropriate Sticker by setting `sticker_id` in the same send call; omit it only when the catalog has no suitable Sticker. Choose from the full visual descriptions, not from the original emoji alone, and avoid repetitive duplicates."

func isTelegramStickerBackendTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == telegramStickerSendToolSuffix || strings.HasSuffix(name, "_"+telegramStickerSendToolSuffix)
}

func isLegacyTelegramStickerSearchTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == "search_telegram_stickers" || strings.HasSuffix(name, "_search_telegram_stickers")
}

// mergeTelegramStickerSendTools converts the Sticker MCP into an internal
// backend for the first-party send tool. The model sees one send call whose
// sticker_id enum contains the complete, stable catalog; it never sees a
// search round or a second sticker-send tool.
func mergeTelegramStickerSendTools(session tools.SessionContext, sdkTools []sdk.Tool) []sdk.Tool {
	visible := make([]sdk.Tool, 0, len(sdkTools))
	var backend *sdk.Tool
	for i := range sdkTools {
		tool := sdkTools[i]
		if isTelegramStickerBackendTool(tool.Name) {
			if backend == nil && stickerPropertySchema(tool.Parameters) != nil && tool.Execute != nil {
				copyTool := tool
				backend = &copyTool
			}
			continue
		}
		if isLegacyTelegramStickerSearchTool(tool.Name) {
			continue
		}
		visible = append(visible, tool)
	}
	if backend == nil || !strings.EqualFold(strings.TrimSpace(session.CurrentPlatform), "telegram") ||
		strings.TrimSpace(session.ReplyTarget) == "" {
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
			platform, _ := args["platform"].(string)
			target, _ := args["target"].(string)
			if !session.IsSameConversation(strings.TrimSpace(platform), strings.TrimSpace(target)) {
				return nil, errors.New("sticker_id is available only for the current Telegram conversation")
			}

			sendArgs := make(map[string]any, len(args)-1)
			for key, value := range args {
				if key != "sticker_id" {
					sendArgs[key] = value
				}
			}
			if hasNativeSendContent(sendArgs) {
				if _, err := sendExecute(ctx, sendArgs); err != nil {
					return nil, err
				}
			}

			output, err := stickerExecute(ctx, map[string]any{
				"chat_id":    strings.TrimSpace(session.ReplyTarget),
				"sticker_id": stickerID,
			})
			if err != nil {
				return nil, err
			}
			if err := telegramStickerBackendError(output); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		}
		break
	}
	return visible
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
