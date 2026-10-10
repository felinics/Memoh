package inbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/slash"
)

// A slash refusal is answered with the copy for its catalog code, whether it
// is a code or the refusal a package returned.
func TestSlashErrorRepliesWithTheCatalogCopy(t *testing.T) {
	t.Parallel()
	processor := NewChannelInboundProcessor(nil, nil, nil, nil, nil, nil, nil, "", 0)
	msg := channel.InboundMessage{Channel: channel.ChannelType("telegram"), ReplyTarget: "chat"}
	en := i18n.New("en")
	for _, tc := range []struct {
		err  error
		code apperror.Code
	}{
		{apperror.New(slash.CodeUnknownSlash, nil), apperror.CodeSlashUnknownCommand},
		{apperror.New(slash.CodePermissionDenied, nil), apperror.CodeSlashPermissionDenied},
		{slash.NewError(slash.CodeRequestedSkillNotFound), apperror.CodeSlashSkillNotFound},
		{channel.RejectReservedSkillMetadata(channel.Message{Metadata: map[string]any{"requestedSkills": []string{"alpha"}}}), apperror.CodeSlashReservedMetadata},
		{apperror.New(apperror.CodeRuntimeControlUnsupported, nil), apperror.CodeRuntimeControlUnsupported},
	} {
		sender := &fakeReplySender{}
		if err := processor.sendSlashError(context.Background(), sender, msg, tc.err); err != nil {
			t.Fatalf("sendSlashError(%v) = %v", tc.err, err)
		}
		want := en.T("errors." + string(tc.code))
		if len(sender.sent) != 1 || sender.sent[0].Message.PlainText() != want {
			t.Fatalf("replies for %v = %+v, want %q", tc.err, sender.sent, want)
		}
	}
}

// A queue command refused with a catalog code is answered with that code's
// copy. Any other admission failure is answered as unavailable, and its
// cause is recorded once, as an event, since the reply does not carry it.
func TestQueueCommandAdmissionFailureReplyAndRecord(t *testing.T) {
	t.Parallel()
	en := i18n.New("en")
	for _, tc := range []struct {
		name    string
		err     error
		code    apperror.Code
		records int
	}{
		{"catalog refusal", NewQueueCommandError(QueueCommandCodeCapacity), apperror.CodeQueueCapacityExceeded, 0},
		{"other failure", errors.New("SECRET queue store unreachable"), apperror.CodeQueueAdmissionUnavailable, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
			chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "bot-1", RouteID: "route-1"}}
			processor := NewChannelInboundProcessor(slog.New(slog.NewJSONHandler(&logs, nil)), nil, chatSvc, chatSvc, &fakeChatGateway{}, channelIdentitySvc, &fakePolicyService{}, "", 0)
			processor.SetACLService(&fakeChatACL{allowed: true})
			processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1"}})
			processor.SetQueueCommandHandler(&fakeQueueCommandHandler{steerErr: tc.err})
			cfg := channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}
			msg := channel.InboundMessage{
				BotID: "bot-1", Channel: cfg.ChannelType, ReplyTarget: "target-id",
				Message:      channel.Message{ID: "steer-1", Text: "/steer use bun"},
				Sender:       channel.Identity{SubjectID: "ext-1"},
				Conversation: channel.Conversation{ID: "chat-1", Type: channel.ConversationTypePrivate},
			}
			sender := &fakeReplySender{}
			if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
				t.Fatalf("HandleInbound() error = %v", err)
			}
			want := en.T("errors." + string(tc.code))
			if len(sender.sent) != 1 || sender.sent[0].Message.PlainText() != want {
				t.Fatalf("replies = %+v, want %q", sender.sent, want)
			}
			var failures []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "queue command admission failed" {
					failures = append(failures, record)
				}
			}
			if len(failures) != tc.records {
				t.Fatalf("admission failure records = %v, want %d", failures, tc.records)
			}
			if tc.records == 1 {
				if got, _ := failures[0]["error"].(string); !strings.Contains(got, "SECRET queue store unreachable") || failures[0]["level"] != "WARN" {
					t.Fatalf("record = %v, want the cause as a WARN event", failures[0])
				}
			}
		})
	}
}
