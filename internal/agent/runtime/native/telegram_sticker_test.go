package native

import (
	"context"
	"errors"
	"reflect"
	"strings"
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
				"text":     map[string]any{"type": "string"},
				"reply_to": map[string]any{"type": "string"},
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
				"sticker_id": map[string]any{
					"type": "string", "enum": []any{"s002", "S001", "S001"},
					"description": "目录说明和状态。\n\nSticker Set：`demo`（2 张）\n- S001：角色微笑挥手（原始 emoji：👋，仅供参考）\n- S002：待识别（原始 emoji：😴，仅供参考）",
				},
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
	compactDescription, _ := stickerSchema["description"].(string)
	for _, want := range []string{"Stable Sticker catalog", "- S001：角色微笑挥手", "- S002：待识别（emoji：😴）"} {
		if !strings.Contains(compactDescription, want) {
			t.Fatalf("compact Sticker description missing %q: %s", want, compactDescription)
		}
	}
	for _, removed := range []string{"Sticker Set", "2 张", "原始 emoji"} {
		if strings.Contains(compactDescription, removed) {
			t.Fatalf("compact Sticker description retained %q: %s", removed, compactDescription)
		}
	}
	output, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"text": "hello", "reply_to": "message-42", "sticker_id": "s002",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isSuccessfulCurrentSendResult(output, RunConfig{Identity: SessionContext{
		CurrentPlatform: "telegram", ReplyTarget: "chat-1",
	}}) {
		t.Fatalf("combined send result = %#v", output)
	}
	if len(nativeInputs) != 1 || nativeInputs[0]["text"] != "hello" || nativeInputs[0]["reply_to"] != "message-42" {
		t.Fatalf("native send inputs = %#v", nativeInputs)
	}
	if len(stickerInputs) != 1 || stickerInputs[0]["chat_id"] != "chat-1" || stickerInputs[0]["sticker_id"] != "S002" {
		t.Fatalf("Sticker backend inputs = %#v", stickerInputs)
	}
}

func TestMergedTelegramSendSupportsStickerOnly(t *testing.T) {
	t.Parallel()

	nativeCalls := 0
	stickerCalls := 0
	tools := mergeTelegramStickerSendTools(agenttools.SessionContext{
		CurrentPlatform: "telegram", ReplyTarget: "chat-1",
	}, []sdk.Tool{
		{
			Name:       "send",
			Parameters: map[string]any{"properties": map[string]any{}},
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
			Execute: func(*sdk.ToolExecContext, any) (any, error) {
				stickerCalls++
				return map[string]any{"ok": true}, nil
			},
		},
	})

	output, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"sticker_id": "S001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if nativeCalls != 0 || stickerCalls != 1 {
		t.Fatalf("sticker-only send nativeCalls=%d stickerCalls=%d", nativeCalls, stickerCalls)
	}
	result, ok := output.(map[string]any)
	if !ok || result["ok"] != true || result["text_delivered"] != false || result["sticker_delivered"] != true {
		t.Fatalf("sticker-only result = %#v", output)
	}
}

func TestMergedTelegramSendDeliveryMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input       map[string]any
		wantNative  int
		wantSticker int
		wantReplyTo string
	}{
		{name: "unquoted text", input: map[string]any{"text": "hello"}, wantNative: 1},
		{name: "quoted text", input: map[string]any{"text": "hello", "reply_to": "m-1"}, wantNative: 1, wantReplyTo: "m-1"},
		{name: "unquoted text and Sticker", input: map[string]any{"text": "hello", "sticker_id": "S001"}, wantNative: 1, wantSticker: 1},
		{name: "quoted text and Sticker", input: map[string]any{"text": "hello", "reply_to": "m-1", "sticker_id": "S001"}, wantNative: 1, wantSticker: 1, wantReplyTo: "m-1"},
		{name: "Sticker only", input: map[string]any{"sticker_id": "S001"}, wantSticker: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var nativeInputs []map[string]any
			stickerCalls := 0
			merged := mergeTelegramStickerSendTools(agenttools.SessionContext{
				CurrentPlatform: "telegram", ReplyTarget: "chat-1",
			}, []sdk.Tool{
				{
					Name: "send", Parameters: map[string]any{"properties": map[string]any{}},
					Execute: func(_ *sdk.ToolExecContext, input any) (any, error) {
						nativeInputs = append(nativeInputs, normalizeToolInput(input))
						return map[string]any{"ok": true}, nil
					},
				},
				{
					Name: "send_telegram_sticker",
					Parameters: map[string]any{"properties": map[string]any{
						"sticker_id": map[string]any{"type": "string", "enum": []any{"S001"}},
					}},
					Execute: func(*sdk.ToolExecContext, any) (any, error) {
						stickerCalls++
						return map[string]any{"ok": true}, nil
					},
				},
			})

			if _, err := merged[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, tc.input); err != nil {
				t.Fatal(err)
			}
			if len(nativeInputs) != tc.wantNative || stickerCalls != tc.wantSticker {
				t.Fatalf("native=%d Sticker=%d, want native=%d Sticker=%d", len(nativeInputs), stickerCalls, tc.wantNative, tc.wantSticker)
			}
			if tc.wantNative == 1 {
				gotReplyTo, _ := nativeInputs[0]["reply_to"].(string)
				if gotReplyTo != tc.wantReplyTo {
					t.Fatalf("reply_to = %q, want %q", gotReplyTo, tc.wantReplyTo)
				}
				if _, leaked := nativeInputs[0]["sticker_id"]; leaked {
					t.Fatalf("sticker_id leaked into native sender: %#v", nativeInputs[0])
				}
			}
		})
	}
}

func TestMergedTelegramSendDoesNotHideInvalidStringMessageBehindSticker(t *testing.T) {
	t.Parallel()
	nativeCalls := 0
	stickerCalls := 0
	tools := mergeTelegramStickerSendTools(agenttools.SessionContext{
		CurrentPlatform: "telegram", ReplyTarget: "chat-1",
	}, []sdk.Tool{
		{
			Name: "send", Parameters: map[string]any{"properties": map[string]any{}},
			Execute: func(*sdk.ToolExecContext, any) (any, error) {
				nativeCalls++
				return map[string]any{
					"ok":         false,
					"error_code": "message_send_failed",
				}, nil
			},
		},
		{
			Name: "send_telegram_sticker",
			Parameters: map[string]any{"properties": map[string]any{
				"sticker_id": map[string]any{"type": "string", "enum": []any{"S001"}},
			}},
			Execute: func(*sdk.ToolExecContext, any) (any, error) {
				stickerCalls++
				return map[string]any{"ok": true}, nil
			},
		},
	})

	output, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"message":    `{"format":"markdown", text:"hello"}`,
		"sticker_id": "S001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if nativeCalls != 1 || stickerCalls != 0 {
		t.Fatalf("native=%d Sticker=%d, want native=1 Sticker=0", nativeCalls, stickerCalls)
	}
	result, ok := output.(map[string]any)
	if !ok || result["ok"] != false {
		t.Fatalf("invalid message result = %#v, want retryable failure", output)
	}
}

func TestMergedStickerSendReportsCommittedTextWithoutRetryingIt(t *testing.T) {
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
	output, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"text": "hello", "sticker_id": "S001",
	})
	if err != nil || nativeCalls != 1 {
		t.Fatalf("partial send err=%v nativeCalls=%d", err, nativeCalls)
	}
	result, ok := output.(map[string]any)
	if !ok || result["ok"] != false || result["text_delivered"] != true || result["sticker_delivered"] != false {
		t.Fatalf("partial delivery envelope = %#v", output)
	}
	if !isSuccessfulCurrentSendResult(output, RunConfig{Identity: SessionContext{
		CurrentPlatform: "telegram", ReplyTarget: "chat-1",
	}}) {
		t.Fatalf("committed text did not terminate the local send loop: %#v", output)
	}
}

func TestMergedStickerSendRejectsUnknownIDBeforeSendingText(t *testing.T) {
	t.Parallel()
	nativeCalls := 0
	stickerCalls := 0
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
			Execute: func(*sdk.ToolExecContext, any) (any, error) {
				stickerCalls++
				return map[string]any{"ok": true}, nil
			},
		},
	})
	_, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{
		"text": "must not send", "sticker_id": "S999",
	})
	if err == nil || nativeCalls != 0 || stickerCalls != 0 {
		t.Fatalf("unknown ID err=%v nativeCalls=%d stickerCalls=%d", err, nativeCalls, stickerCalls)
	}
}
