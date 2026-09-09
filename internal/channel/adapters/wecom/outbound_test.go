package wecom

import (
	"context"
	"crypto/md5" //nolint:gosec // test verifies protocol checksum formatting.
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestBuildSendPayload_Text(t *testing.T) {
	payload, cmd, reqID, err := buildSendPayload(channel.Message{
		Format: channel.MessageFormatPlain,
		Text:   "hello",
	}, "chat_1")
	if err != nil {
		t.Fatalf("buildSendPayload error = %v", err)
	}
	if cmd != WSCmdSendMessage {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	if reqID == "" {
		t.Fatal("reqID should not be empty")
	}
	p, ok := payload.(SendMessageMarkdownBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.Markdown.Content != "hello" {
		t.Fatalf("unexpected payload content: %q", p.Markdown.Content)
	}
}

func TestBuildSendPayload_AttachmentNotSupported(t *testing.T) {
	_, _, _, err := buildSendPayload(channel.Message{
		Attachments: []channel.Attachment{
			{Type: channel.AttachmentImage, Base64: "aGVsbG8="},
		},
	}, "chat_1")
	if err == nil {
		t.Fatal("expected error for proactive attachment send")
	}
}

func TestBuildRespondPayload_Stream(t *testing.T) {
	payload, cmd, reqID, err := buildRespondPayload(channel.Message{
		Format: channel.MessageFormatMarkdown,
		Text:   "world",
	}, "req_abc")
	if err != nil {
		t.Fatalf("buildRespondPayload error = %v", err)
	}
	if cmd != WSCmdRespond {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	if reqID != "req_abc" {
		t.Fatalf("unexpected req id: %q", reqID)
	}
	p, ok := payload.(StreamReplyBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.MsgType != "stream" || p.Stream.Content != "world" || !p.Stream.Finish {
		t.Fatalf("unexpected stream payload: %+v", p)
	}
}

func TestBuildRespondPayload_StreamWithFeedback(t *testing.T) {
	payload, cmd, _, err := buildRespondPayload(channel.Message{
		Text: "world",
		Metadata: map[string]any{
			"wecom_feedback_id": "feedback_1",
		},
	}, "req_abc")
	if err != nil {
		t.Fatalf("buildRespondPayload error = %v", err)
	}
	if cmd != WSCmdRespond {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	p, ok := payload.(StreamReplyBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.Stream.Feedback == nil || p.Stream.Feedback.ID != "feedback_1" {
		t.Fatalf("unexpected feedback: %+v", p.Stream.Feedback)
	}
}

func TestBuildRespondPayloadWithStream_Delta(t *testing.T) {
	payload, cmd, reqID, err := buildRespondPayloadWithStream(channel.Message{
		Text: "delta",
	}, "req_abc", "stream_1", false)
	if err != nil {
		t.Fatalf("buildRespondPayloadWithStream error = %v", err)
	}
	if cmd != WSCmdRespond || reqID != "req_abc" {
		t.Fatalf("unexpected cmd/reqid: %q %q", cmd, reqID)
	}
	p, ok := payload.(StreamReplyBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.Stream.ID != "stream_1" || p.Stream.Finish {
		t.Fatalf("unexpected stream payload: %+v", p.Stream)
	}
}

func TestBuildRespondPayloadWithStream_EmptyDeltaRejected(t *testing.T) {
	_, _, _, err := buildRespondPayloadWithStream(channel.Message{}, "req_abc", "stream_1", false)
	if err == nil {
		t.Fatal("expected error for empty delta content")
	}
}

func TestBuildSendPayload_TemplateCard(t *testing.T) {
	payload, cmd, _, err := buildSendPayload(channel.Message{
		Metadata: map[string]any{
			"wecom_template_card": map[string]any{
				"card_type": "text_notice",
			},
		},
	}, "chat_1")
	if err != nil {
		t.Fatalf("buildSendPayload error = %v", err)
	}
	if cmd != WSCmdSendMessage {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	p, ok := payload.(SendMessageTemplateCardBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.MsgType != "template_card" {
		t.Fatalf("unexpected msg type: %q", p.MsgType)
	}
}

func TestBuildRespondPayload_StreamWithTemplateCard(t *testing.T) {
	payload, cmd, _, err := buildRespondPayload(channel.Message{
		Text: "x",
		Metadata: map[string]any{
			"wecom_template_card": map[string]any{
				"card_type": "text_notice",
			},
		},
	}, "req_abc")
	if err != nil {
		t.Fatalf("buildRespondPayload error = %v", err)
	}
	if cmd != WSCmdRespond {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	p, ok := payload.(StreamWithTemplateCardReplyBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.MsgType != "stream_with_template_card" {
		t.Fatalf("unexpected msg type: %q", p.MsgType)
	}
}

func TestBuildRespondPayload_UpdateTemplateCard(t *testing.T) {
	payload, cmd, reqID, err := buildRespondPayload(channel.Message{
		Metadata: map[string]any{
			"wecom_update_template_card": map[string]any{
				"card_type": "text_notice",
				"task_id":   "task-1",
			},
			"wecom_update_userids": []any{"u1", "u2"},
		},
	}, "req_abc")
	if err != nil {
		t.Fatalf("buildRespondPayload error = %v", err)
	}
	if cmd != WSCmdRespondUpdate {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	if reqID != "req_abc" {
		t.Fatalf("unexpected req id: %q", reqID)
	}
	p, ok := payload.(UpdateTemplateCardBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.ResponseType != "update_template_card" {
		t.Fatalf("unexpected response type: %q", p.ResponseType)
	}
	if len(p.UserIDs) != 2 || p.UserIDs[0] != "u1" || p.UserIDs[1] != "u2" {
		t.Fatalf("unexpected userids: %#v", p.UserIDs)
	}
}

func TestBuildRespondPayload_WelcomeText(t *testing.T) {
	payload, cmd, reqID, err := buildRespondPayload(channel.Message{
		Text: "welcome",
		Metadata: map[string]any{
			"wecom_welcome": true,
		},
	}, "req_abc")
	if err != nil {
		t.Fatalf("buildRespondPayload error = %v", err)
	}
	if cmd != WSCmdRespondWelcome {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	if reqID != "req_abc" {
		t.Fatalf("unexpected req id: %q", reqID)
	}
	p, ok := payload.(WelcomeTextReplyBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.MsgType != "text" || p.Text.Content != "welcome" {
		t.Fatalf("unexpected welcome payload: %+v", p)
	}
}

func TestBuildRespondPayload_WelcomeTemplateCard(t *testing.T) {
	payload, cmd, _, err := buildRespondPayload(channel.Message{
		Metadata: map[string]any{
			"wecom_welcome": true,
			"wecom_template_card": map[string]any{
				"card_type": "text_notice",
			},
		},
	}, "req_abc")
	if err != nil {
		t.Fatalf("buildRespondPayload error = %v", err)
	}
	if cmd != WSCmdRespondWelcome {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	p, ok := payload.(WelcomeTemplateCardReplyBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.MsgType != "template_card" {
		t.Fatalf("unexpected msg type: %q", p.MsgType)
	}
}

func TestBuildPreparedRespondPayloadWithStream_ImageAttachment(t *testing.T) {
	const rawImage = "image-bytes"

	payload, cmd, reqID, err := buildPreparedRespondPayloadWithStream(context.Background(), channel.PreparedMessage{
		Message: channel.Message{
			Reply: &channel.ReplyRef{MessageID: "msg_1"},
		},
		Attachments: []channel.PreparedAttachment{
			{
				Logical: channel.Attachment{Type: channel.AttachmentImage},
				Kind:    channel.PreparedAttachmentUpload,
				Mime:    "image/png",
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader(rawImage)), nil
				},
			},
		},
	}, "req_abc", "stream_1", true)
	if err != nil {
		t.Fatalf("buildPreparedRespondPayloadWithStream error = %v", err)
	}
	if cmd != WSCmdRespond {
		t.Fatalf("unexpected cmd: %q", cmd)
	}
	if reqID != "req_abc" {
		t.Fatalf("unexpected req id: %q", reqID)
	}
	p, ok := payload.(StreamReplyBody)
	if !ok {
		t.Fatalf("unexpected payload type: %T", payload)
	}
	if p.Stream.ID != "stream_1" || !p.Stream.Finish {
		t.Fatalf("unexpected stream block: %+v", p.Stream)
	}
	if len(p.Stream.MsgItems) != 1 || p.Stream.MsgItems[0].Image == nil {
		t.Fatalf("expected one image item, got %+v", p.Stream.MsgItems)
	}
	if got := p.Stream.MsgItems[0].Image.Base64; got != base64.StdEncoding.EncodeToString([]byte(rawImage)) {
		t.Fatalf("unexpected image base64: %q", got)
	}
	if got := p.Stream.MsgItems[0].Image.MD5; got != fmt.Sprintf("%x", md5.Sum([]byte(rawImage))) { //nolint:gosec
		t.Fatalf("unexpected image md5: %q", got)
	}
}

func TestBuildPreparedRespondPayloadWithStream_RejectsNonImageAttachment(t *testing.T) {
	_, _, _, err := buildPreparedRespondPayloadWithStream(context.Background(), channel.PreparedMessage{
		Attachments: []channel.PreparedAttachment{
			{
				Logical: channel.Attachment{Type: channel.AttachmentFile},
				Kind:    channel.PreparedAttachmentUpload,
				Mime:    "application/pdf",
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("pdf")), nil
				},
			},
		},
	}, "req_abc", "stream_1", true)
	if err == nil {
		t.Fatal("expected non-image attachment error")
	}
	if !strings.Contains(err.Error(), "only support images") {
		t.Fatalf("unexpected error: %v", err)
	}
}
