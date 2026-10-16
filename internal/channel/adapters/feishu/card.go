package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
)

// Cards use the JSON 2.0 schema because only its markdown component renders
// headings, tables and fenced code blocks; the 1.0 lark_md text flattened
// them into plain paragraphs. JSON 2.0 needs Feishu client 7.20+, older
// clients show an upgrade prompt instead of the body.
const (
	feishuCardSchema = "2.0"
	// feishuCardBodyElementID addresses the markdown element of a CardKit
	// card entity when its text is streamed.
	feishuCardBodyElementID = "body"
	// CardKit types out streamed text one print_step per print_frequency_ms.
	// The client defaults (1 rune / 70ms ≈ 14 runes/s) lag far behind model
	// output; under the "fast" strategy an update flushes whatever is still
	// queued, so the visible text would jump instead of typing.
	feishuCardPrintFrequencyMs = 50
	feishuCardPrintStep        = 3
	// A card entity whose creation failed (typically because the app has
	// not been granted cardkit:card:write) is not retried for every reply;
	// streams fall back to patching the message until the cool-down ends.
	feishuCardKitRetryAfter = 10 * time.Minute
)

func buildFeishuCardContent(text string) (string, error) {
	return marshalFeishuCard(feishuMarkdownElement(text), false)
}

func buildFeishuPlainCardContent(text string) (string, error) {
	return marshalFeishuCard(map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":     "plain_text",
			"content": strings.TrimSpace(text),
		},
	}, false)
}

func buildFeishuStreamCardContent(text string) (string, error) {
	return buildFeishuCardContent(feishuStreamCardText(text))
}

// buildFeishuStreamingCardEntity builds the card entity created through
// CardKit; its markdown element is then updated by element ID.
func buildFeishuStreamingCardEntity(text string) (string, error) {
	return marshalFeishuCard(feishuMarkdownElement(feishuStreamCardText(text)), true)
}

func feishuStreamCardText(text string) string {
	return normalizeFeishuStreamText(extractReadableFromJSON(text))
}

func feishuMarkdownElement(text string) map[string]any {
	return map[string]any{
		"tag":        "markdown",
		"element_id": feishuCardBodyElementID,
		"content":    strings.TrimSpace(text),
	}
}

func marshalFeishuCard(element map[string]any, streaming bool) (string, error) {
	config := map[string]any{
		// Patching a message requires update_multi before and after.
		"update_multi":   true,
		"enable_forward": true,
	}
	if streaming {
		config["streaming_mode"] = true
		config["streaming_config"] = map[string]any{
			"print_frequency_ms": map[string]any{"default": feishuCardPrintFrequencyMs},
			"print_step":         map[string]any{"default": feishuCardPrintStep},
			"print_strategy":     "fast",
		}
	}
	card := map[string]any{
		"schema": feishuCardSchema,
		"config": config,
		"body": map[string]any{
			"elements": []map[string]any{element},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// buildFeishuCardEntityMessageContent is the interactive message content that
// sends an existing card entity.
func buildFeishuCardEntityMessageContent(cardID string) (string, error) {
	data, err := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]any{"card_id": cardID},
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// feishuCardKitAvailability remembers, per channel config, that creating a
// card entity recently failed.
type feishuCardKitAvailability struct {
	failedAt sync.Map // config ID -> time.Time
}

func (a *feishuCardKitAvailability) available(configID string) bool {
	v, ok := a.failedAt.Load(configID)
	if !ok {
		return true
	}
	if time.Since(v.(time.Time)) >= feishuCardKitRetryAfter {
		a.failedAt.Delete(configID)
		return true
	}
	return false
}

func (a *feishuCardKitAvailability) markFailed(configID string) {
	a.failedAt.Store(configID, time.Now())
}

func createFeishuCardEntity(ctx context.Context, client *lark.Client, text string) (string, error) {
	data, err := buildFeishuStreamingCardEntity(text)
	if err != nil {
		return "", err
	}
	resp, err := client.Cardkit.V1.Card.Create(ctx, larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(data).
			Build()).
		Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu card entity create failed: %s (code: %d)", resp.Msg, resp.Code)
	}
	if resp.Data == nil || resp.Data.CardId == nil || strings.TrimSpace(*resp.Data.CardId) == "" {
		return "", errors.New("feishu card entity create failed: empty card id")
	}
	return strings.TrimSpace(*resp.Data.CardId), nil
}

// streamFeishuCardEntityText replaces the markdown element's full text.
// CardKit types out the suffix when the previous text is a prefix of the new
// one, and redraws the element otherwise.
func streamFeishuCardEntityText(ctx context.Context, client *lark.Client, cardID string, sequence int, text string) error {
	resp, err := client.Cardkit.V1.CardElement.Content(ctx, larkcardkit.NewContentCardElementReqBuilder().
		CardId(cardID).
		ElementId(feishuCardBodyElementID).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().
			Uuid(uuid.NewString()).
			Content(text).
			Sequence(sequence).
			Build()).
		Build())
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu card entity stream failed: %s (code: %d)", resp.Msg, resp.Code)
	}
	return nil
}

// setFeishuCardEntityStreaming switches streaming mode. While it is on the
// chat list previews the card as "[生成中...]" and the card cannot be
// forwarded, so it is closed once the text is final. Feishu also closes it ten
// minutes after it was opened, after which text updates are rejected until it
// is reopened.
func setFeishuCardEntityStreaming(ctx context.Context, client *lark.Client, cardID string, sequence int, on bool) error {
	settings := `{"config":{"streaming_mode":false}}`
	if on {
		settings = `{"config":{"streaming_mode":true}}`
	}
	resp, err := client.Cardkit.V1.Card.Settings(ctx, larkcardkit.NewSettingsCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().
			Settings(settings).
			Uuid(uuid.NewString()).
			Sequence(sequence).
			Build()).
		Build())
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu card entity streaming settings failed: %s (code: %d)", resp.Msg, resp.Code)
	}
	return nil
}
