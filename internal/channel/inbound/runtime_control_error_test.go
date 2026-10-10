package inbound

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
)

// A runtime control failure is answered with the copy for its code, and one
// without a public error with the generic copy; the error is returned for the
// message's result record.
func TestRuntimeControlErrorIsAnsweredAndReturned(t *testing.T) {
	t.Parallel()
	processor := NewChannelInboundProcessor(nil, nil, nil, nil, nil, nil, nil, "", 0)
	msg := channel.InboundMessage{Channel: channel.ChannelType("telegram"), ReplyTarget: "chat"}
	en := i18n.New("en")
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("SECRET driver pipe closed"), en.T("errors.internal")},
		{apperror.Wrap(apperror.CodeRuntimeControlFailed, errors.New("SECRET driver pipe closed"), nil), en.T("errors.runtime_control.failed")},
		{apperror.New(apperror.CodeRuntimeControlForbidden, nil), en.T("errors.runtime_control.forbidden")},
	} {
		sender := &fakeReplySender{}
		if err := processor.sendRuntimeControlError(context.Background(), sender, msg, InboundIdentity{BotID: "bot-1"}, tc.err); err != tc.err { //nolint:errorlint // identity: the failure is returned for the result record.
			t.Fatalf("send = %v, want %v", err, tc.err)
		}
		if len(sender.sent) != 1 || strings.Contains(sender.sent[0].Message.PlainText(), "SECRET") || sender.sent[0].Message.PlainText() != tc.want {
			t.Fatalf("replies = %+v, want %q", sender.sent, tc.want)
		}
	}
}
