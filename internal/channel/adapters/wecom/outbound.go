package wecom

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // WeCom stream image payload requires MD5 checksum field.
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
)

func (a *WeComAdapter) ResolveAttachment(ctx context.Context, cfg channel.ChannelConfig, attachment channel.Attachment) (channel.AttachmentPayload, error) {
	_ = cfg
	if a.http == nil {
		return channel.AttachmentPayload{}, errors.New("wecom http client not configured")
	}
	url := strings.TrimSpace(attachment.URL)
	if url == "" {
		return channel.AttachmentPayload{}, errors.New("wecom attachment url is required")
	}
	aesKey := ""
	if attachment.Metadata != nil {
		if value, ok := attachment.Metadata["aeskey"].(string); ok {
			aesKey = strings.TrimSpace(value)
		}
	}
	var file DownloadedFile
	var err error
	if aesKey != "" {
		file, err = a.http.DownloadAndDecryptFile(ctx, url, aesKey)
	} else {
		file, err = a.http.DownloadFile(ctx, url)
	}
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	return channel.AttachmentPayload{
		Reader: io.NopCloser(bytes.NewReader(file.Data)),
		Mime:   strings.TrimSpace(file.ContentType),
		Name:   strings.TrimSpace(file.FileName),
		Size:   int64(len(file.Data)),
	}, nil
}

type markdownPayload struct {
	Content string `json:"content"`
}

func buildSendPayload(msg channel.Message, targetID string) (any, string, string, error) {
	if strings.TrimSpace(targetID) == "" {
		return nil, "", "", errors.New("wecom target id is required")
	}
	reqID := NewReqID(WSCmdSendMessage)
	if card, ok := readTemplateCard(msg.Metadata); ok {
		return SendMessageTemplateCardBody{
			ChatID:       targetID,
			MsgType:      "template_card",
			TemplateCard: card,
		}, WSCmdSendMessage, reqID, nil
	}

	// aibot_send_msg currently supports markdown/template_card in official SDK.
	// Attachments should be sent through callback-reply path (aibot_respond_msg).
	if len(msg.Attachments) > 0 {
		return nil, "", "", errors.New("wecom proactive send does not support attachments; use reply flow")
	}

	text := strings.TrimSpace(msg.PlainText())
	if text == "" {
		return nil, "", "", errors.New("wecom outbound text is required")
	}
	return SendMessageMarkdownBody{
		ChatID:  targetID,
		MsgType: "markdown",
		Markdown: markdownPayload{
			Content: text,
		},
	}, WSCmdSendMessage, reqID, nil
}

func buildRespondPayload(msg channel.Message, replyReqID string) (any, string, string, error) {
	return buildRespondPayloadWithStream(msg, replyReqID, "", true)
}

func buildPreparedRespondPayload(ctx context.Context, msg channel.PreparedMessage, replyReqID string) (any, string, string, error) {
	return buildPreparedRespondPayloadWithStream(ctx, msg, replyReqID, "", true)
}

func buildRespondPayloadWithStream(msg channel.Message, replyReqID string, streamID string, finish bool) (any, string, string, error) {
	reqID := strings.TrimSpace(replyReqID)
	if reqID == "" {
		return nil, "", "", errors.New("reply req_id is required")
	}
	if finish {
		if body, ok := buildWelcomePayload(msg); ok {
			return body, WSCmdRespondWelcome, reqID, nil
		}
		if body, ok := readUpdateTemplateCard(msg.Metadata); ok {
			return body, WSCmdRespondUpdate, reqID, nil
		}
	}
	text := strings.TrimSpace(msg.PlainText())
	if finish && text == "" && len(msg.Attachments) == 0 {
		return nil, "", "", errors.New("wecom reply payload is empty")
	}
	if !finish && text == "" {
		return nil, "", "", errors.New("wecom stream delta content is empty")
	}
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		streamID = NewReqID("stream")
	}
	stream := StreamReplyBlock{
		ID:      streamID,
		Finish:  finish,
		Content: text,
	}
	if feedbackID := readFeedbackID(msg.Metadata); feedbackID != "" {
		stream.Feedback = &StreamReplyFeedback{ID: feedbackID}
	}
	if finish {
		stream.MsgItems = buildLegacyStreamReplyItems(msg)
	}
	if card, ok := readTemplateCard(msg.Metadata); ok {
		return StreamWithTemplateCardReplyBody{
			MsgType:      "stream_with_template_card",
			Stream:       stream,
			TemplateCard: card,
		}, WSCmdRespond, reqID, nil
	}
	return StreamReplyBody{
		MsgType: "stream",
		Stream:  stream,
	}, WSCmdRespond, reqID, nil
}

func buildPreparedRespondPayloadWithStream(
	ctx context.Context,
	msg channel.PreparedMessage,
	replyReqID string,
	streamID string,
	finish bool,
) (any, string, string, error) {
	logical := msg.LogicalMessage()
	reqID := strings.TrimSpace(replyReqID)
	if reqID == "" {
		return nil, "", "", errors.New("reply req_id is required")
	}
	if finish {
		if body, ok := buildWelcomePayload(logical); ok {
			return body, WSCmdRespondWelcome, reqID, nil
		}
		if body, ok := readUpdateTemplateCard(logical.Metadata); ok {
			return body, WSCmdRespondUpdate, reqID, nil
		}
	}
	text := strings.TrimSpace(logical.PlainText())
	if finish && text == "" && len(logical.Attachments) == 0 && len(msg.Attachments) == 0 {
		return nil, "", "", errors.New("wecom reply payload is empty")
	}
	if !finish && text == "" {
		return nil, "", "", errors.New("wecom stream delta content is empty")
	}
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		streamID = NewReqID("stream")
	}
	stream := StreamReplyBlock{
		ID:      streamID,
		Finish:  finish,
		Content: text,
	}
	if feedbackID := readFeedbackID(logical.Metadata); feedbackID != "" {
		stream.Feedback = &StreamReplyFeedback{ID: feedbackID}
	}
	if finish {
		items, err := buildPreparedStreamReplyItems(ctx, msg)
		if err != nil {
			return nil, "", "", err
		}
		stream.MsgItems = items
	}
	if card, ok := readTemplateCard(logical.Metadata); ok {
		return StreamWithTemplateCardReplyBody{
			MsgType:      "stream_with_template_card",
			Stream:       stream,
			TemplateCard: card,
		}, WSCmdRespond, reqID, nil
	}
	return StreamReplyBody{
		MsgType: "stream",
		Stream:  stream,
	}, WSCmdRespond, reqID, nil
}

func buildPreparedStreamReplyItems(ctx context.Context, msg channel.PreparedMessage) ([]StreamReplyItem, error) {
	if len(msg.Attachments) == 0 {
		return buildLegacyStreamReplyItems(msg.Message), nil
	}
	item, err := buildPreparedStreamReplyItem(ctx, msg.Attachments[0])
	if err != nil {
		return nil, err
	}
	return []StreamReplyItem{item}, nil
}

func buildPreparedStreamReplyItem(ctx context.Context, att channel.PreparedAttachment) (StreamReplyItem, error) {
	if !isWeComReplyImageAttachment(att) {
		return StreamReplyItem{}, errors.New("wecom reply attachments only support images")
	}
	if att.Open == nil {
		return StreamReplyItem{}, errors.New("wecom reply attachment reader is required")
	}
	reader, err := att.Open(ctx)
	if err != nil {
		return StreamReplyItem{}, err
	}
	defer func() { _ = reader.Close() }()

	raw, err := media.ReadAllWithLimit(reader, media.MaxAssetBytes)
	if err != nil {
		return StreamReplyItem{}, err
	}
	if len(raw) == 0 {
		return StreamReplyItem{}, errors.New("wecom reply attachment is empty")
	}
	return StreamReplyItem{
		MsgType: "image",
		Image: &StreamReplyImage{
			Base64: base64.StdEncoding.EncodeToString(raw),
			MD5:    fmt.Sprintf("%x", md5.Sum(raw)), //nolint:gosec // WeCom protocol mandates md5 field for base64 images.
		},
	}, nil
}

func buildLegacyStreamReplyItems(msg channel.Message) []StreamReplyItem {
	if len(msg.Attachments) == 0 {
		return nil
	}
	first := msg.Attachments[0]
	base64Data := extractBase64Content(first.Base64)
	if base64Data == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return []StreamReplyItem{
		{
			MsgType: "image",
			Image: &StreamReplyImage{
				Base64: base64.StdEncoding.EncodeToString(raw),
				MD5:    fmt.Sprintf("%x", md5.Sum(raw)), //nolint:gosec // WeCom protocol mandates md5 field for base64 images.
			},
		},
	}
}

func isWeComReplyImageAttachment(att channel.PreparedAttachment) bool {
	switch att.Logical.Type {
	case channel.AttachmentImage, channel.AttachmentGIF:
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(att.Mime)), "image/")
}

func buildWelcomePayload(msg channel.Message) (any, bool) {
	if !readBool(msg.Metadata, "wecom_welcome") {
		return nil, false
	}
	if card, ok := readTemplateCard(msg.Metadata); ok {
		return WelcomeTemplateCardReplyBody{
			MsgType:      "template_card",
			TemplateCard: card,
		}, true
	}
	text := strings.TrimSpace(msg.PlainText())
	if text == "" {
		return nil, false
	}
	return WelcomeTextReplyBody{
		MsgType: "text",
		Text: welcomeTextBody{
			Content: text,
		},
	}, true
}

func readTemplateCard(metadata map[string]any) (map[string]any, bool) {
	if metadata == nil {
		return nil, false
	}
	raw, ok := metadata["wecom_template_card"]
	if !ok {
		return nil, false
	}
	card, ok := raw.(map[string]any)
	if !ok || len(card) == 0 {
		return nil, false
	}
	return card, true
}

func readUpdateTemplateCard(metadata map[string]any) (UpdateTemplateCardBody, bool) {
	if metadata == nil {
		return UpdateTemplateCardBody{}, false
	}
	raw, ok := metadata["wecom_update_template_card"]
	if !ok {
		return UpdateTemplateCardBody{}, false
	}
	card, ok := raw.(map[string]any)
	if !ok || len(card) == 0 {
		return UpdateTemplateCardBody{}, false
	}
	body := UpdateTemplateCardBody{
		ResponseType: "update_template_card",
		TemplateCard: card,
	}
	if userIDs := readStringSlice(metadata["wecom_update_userids"]); len(userIDs) > 0 {
		body.UserIDs = userIDs
	}
	return body, true
}

func readStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func readFeedbackID(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata["wecom_feedback_id"]
	if !ok {
		return ""
	}
	if v, ok := raw.(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func readBool(metadata map[string]any, key string) bool {
	if metadata == nil || strings.TrimSpace(key) == "" {
		return false
	}
	raw, ok := metadata[key]
	if !ok {
		return false
	}
	v, ok := raw.(bool)
	return ok && v
}

func extractBase64Content(v string) string {
	value := strings.TrimSpace(v)
	if value == "" {
		return ""
	}
	if idx := strings.Index(value, ","); idx > 0 && strings.Contains(strings.ToLower(value[:idx]), "base64") {
		return strings.TrimSpace(value[idx+1:])
	}
	return value
}

func (a *WeComAdapter) getClient(configID, botID string) *WSClient {
	key := wecomClientCacheKey(configID, botID)
	if key == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.clients[key]
}

func wecomClientCacheKey(configID, botID string) string {
	configID = strings.TrimSpace(configID)
	botID = strings.TrimSpace(botID)
	if configID == "" || botID == "" {
		return ""
	}
	return configID + "\x00" + botID
}

func (a *WeComAdapter) lookupCallbackContext(configID string, reply *channel.ReplyRef) (callbackContext, bool) {
	if a == nil || a.cache == nil || reply == nil {
		return callbackContext{}, false
	}
	messageID := strings.TrimSpace(reply.MessageID)
	if messageID == "" {
		return callbackContext{}, false
	}
	return a.cache.Get(configID, messageID)
}
