package inbound

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

// A runtime control that fails without a code is answered with the generic
// copy, and its cause is recorded, since the reply does not carry it.
func TestRuntimeControlErrorRecordsTheUncodedCause(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	processor := NewChannelInboundProcessor(slog.New(slog.NewJSONHandler(&logs, nil)), nil, nil, nil, nil, nil, nil, "", 0)
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{Channel: channel.ChannelType("telegram"), ReplyTarget: "chat"}

	err := processor.sendRuntimeControlError(context.Background(), sender, msg, InboundIdentity{BotID: "bot-1"}, errors.New("SECRET driver pipe closed"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(sender.sent) != 1 || strings.Contains(sender.sent[0].Message.PlainText(), "SECRET") {
		t.Fatalf("replies = %+v, want one reply without the cause", sender.sent)
	}
	if !strings.Contains(logs.String(), `"msg":"runtime control failed"`) || !strings.Contains(logs.String(), "SECRET driver pipe closed") {
		t.Fatalf("records = %s, want the cause recorded", logs.String())
	}
}
