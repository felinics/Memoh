package native

import (
	"context"
	"errors"
	"reflect"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	agenttools "github.com/memohai/memoh/internal/agent/tool"
)

func TestMergeTelegramStickerIntoSingleFirstPartySend(t *testing.T) {
	t.Parallel()
	var nativeInputs []map[string]any
	var stickerInputs []map[string]any
	tools := mergeTelegramStickerSendTools(agenttools.SessionContext{
		CurrentPlatform: "telegram", ReplyTarget: "chat-1",
	}, []sdk.Tool{
		{
			Name: "send",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"text": map[string]any{"type": "string"},
			}},
			Execute: func(_ *sdk.ToolExecContext, input any) (any, error) {
				nativeInputs = append(nativeInputs, normalizeToolInput(input))
				return map[string]any{"ok": true}, nil
			},
		},
		{Name: "search_telegram_stickers", Execute: func(*sdk.ToolExecContext, any) (any, error) { return nil, nil }},
		{
			Name: "sticker_send_telegram_sticker",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"sticker_id": map[string]any{"type": "string", "enum": []any{"s002", "S001", "S001"}},
			}},
			Execute: func(_ *sdk.ToolExecContext, input any) (any, error) {
				stickerInputs = append(stickerInputs, normalizeToolInput(input))
				return map[string]any{"ok": true}, nil
			},
		},
	})
	if len(tools) != 1 || tools[0].Name != "send" {
		t.Fatalf("model-visible tools = %#v", tools)
	}
	stickerSchema := stickerPropertySchema(tools[0].Parameters)
	if got := stickerSchema["enum"]; !reflect.DeepEqual(got, []any{"S001", "S002"}) {
		t.Fatalf("stable Sticker enum = %#v", got)
	}
	output, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"text": "hello", "sticker_id": "s002",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isSuccessfulCurrentSendResult(output, RunConfig{}) {
		t.Fatalf("combined send result = %#v", output)
	}
	if len(nativeInputs) != 1 || nativeInputs[0]["text"] != "hello" {
		t.Fatalf("native send inputs = %#v", nativeInputs)
	}
	if len(stickerInputs) != 1 || stickerInputs[0]["chat_id"] != "chat-1" || stickerInputs[0]["sticker_id"] != "S002" {
		t.Fatalf("Sticker backend inputs = %#v", stickerInputs)
	}
}

func TestMergedStickerSendDoesNotReportPartialDeliveryAsSuccess(t *testing.T) {
	t.Parallel()
	nativeCalls := 0
	tools := mergeTelegramStickerSendTools(agenttools.SessionContext{
		CurrentPlatform: "telegram", ReplyTarget: "chat-1",
	}, []sdk.Tool{
		{
			Name: "send", Parameters: map[string]any{"properties": map[string]any{"text": map[string]any{"type": "string"}}},
			Execute: func(*sdk.ToolExecContext, any) (any, error) {
				nativeCalls++
				return map[string]any{"ok": true}, nil
			},
		},
		{
			Name: "send_telegram_sticker",
			Parameters: map[string]any{"properties": map[string]any{
				"sticker_id": map[string]any{"type": "string", "enum": []any{"S001"}},
			}},
			Execute: func(*sdk.ToolExecContext, any) (any, error) { return nil, errors.New("Sticker failed") },
		},
	})
	_, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"text": "hello", "sticker_id": "S001",
	})
	if err == nil || nativeCalls != 1 {
		t.Fatalf("partial send err=%v nativeCalls=%d", err, nativeCalls)
	}
}
