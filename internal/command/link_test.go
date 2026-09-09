package command

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channelaccess"
)

// fakeLinkConsumer records ConsumeLinkCode calls and returns a configured result.
type fakeLinkConsumer struct {
	token             string
	channelIdentityID string
	binding           channelaccess.Binding
	err               error
}

func (f *fakeLinkConsumer) ConsumeLinkCode(_ context.Context, token, channelIdentityID string) (channelaccess.Binding, error) {
	f.token = token
	f.channelIdentityID = channelIdentityID
	return f.binding, f.err
}

func newLinkTestHandler(consumer LinkConsumer) *Handler {
	h := NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetLinkConsumer(consumer)
	return h
}

func TestLink_MissingCode(t *testing.T) {
	h := newLinkTestHandler(&fakeLinkConsumer{})
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/link",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "provide a link code") {
		t.Fatalf("expected missing-code reply, got: %s", result)
	}
}

func TestLink_NoIdentity(t *testing.T) {
	h := newLinkTestHandler(&fakeLinkConsumer{})
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "", // no identity
		Text:              "/link ABC12345",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "missing channel identity") {
		t.Fatalf("expected no-identity reply, got: %s", result)
	}
}

func TestLink_Success(t *testing.T) {
	consumer := &fakeLinkConsumer{
		binding: channelaccess.Binding{UserID: "user-1", ChannelIdentityID: "ci-1"},
	}
	h := newLinkTestHandler(consumer)
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/link ABC12345",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Connected") && !strings.Contains(result, "linked") {
		t.Fatalf("expected success reply, got: %s", result)
	}
	// The command parser lowercases the action text; normalizeLinkShorthand moves
	// it into Args. The real ConsumeLinkCode uppercases via normalizeToken, but
	// the fake records the raw value. Accept either case.
	if !strings.EqualFold(consumer.token, "ABC12345") {
		t.Fatalf("expected token ABC12345 forwarded, got: %s", consumer.token)
	}
	if consumer.channelIdentityID != "ci-1" {
		t.Fatalf("expected identity ci-1 forwarded, got: %s", consumer.channelIdentityID)
	}
}

func TestLink_ExpiredCode(t *testing.T) {
	consumer := &fakeLinkConsumer{err: channelaccess.ErrCodeExpired}
	h := newLinkTestHandler(consumer)
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/link EXPIRED1",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "expired") {
		t.Fatalf("expected expired reply, got: %s", result)
	}
}

func TestLink_NotFoundCode(t *testing.T) {
	consumer := &fakeLinkConsumer{err: channelaccess.ErrCodeNotFound}
	h := newLinkTestHandler(consumer)
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/link BADCODE1",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "invalid") {
		t.Fatalf("expected not-found reply, got: %s", result)
	}
}

func TestLink_ConsumedCode(t *testing.T) {
	consumer := &fakeLinkConsumer{err: channelaccess.ErrCodeConsumed}
	h := newLinkTestHandler(consumer)
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/link USED1234",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "already used") {
		t.Fatalf("expected consumed reply, got: %s", result)
	}
}

func TestLink_ConsumerUnavailable(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	// linkConsumer is nil → unavailable
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/link ABC12345",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "unavailable") {
		t.Fatalf("expected unavailable reply, got: %s", result)
	}
}

func TestLink_GenericError(t *testing.T) {
	consumer := &fakeLinkConsumer{err: errors.New("db timeout")}
	h := newLinkTestHandler(consumer)
	result, err := h.ExecuteWithInput(context.Background(), ExecuteInput{
		BotID:             "bot-1",
		ChannelIdentityID: "ci-1",
		Text:              "/link ABC12345",
		ChannelType:       "telegram",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Failed") || !strings.Contains(result, "try again") {
		t.Fatalf("expected generic failure reply, got: %s", result)
	}
}

func TestNormalizeLinkShorthand(t *testing.T) {
	tests := []struct {
		name       string
		resource   string
		action     string
		args       []string
		wantAction string
		wantArgs   []string
	}{
		{
			name:       "bare code rewrites to consume",
			resource:   "link",
			action:     "ABC12345",
			args:       nil,
			wantAction: "consume",
			wantArgs:   []string{"ABC12345"},
		},
		{
			name:       "explicit consume stays",
			resource:   "link",
			action:     "consume",
			args:       []string{"ABC12345"},
			wantAction: "consume",
			wantArgs:   []string{"ABC12345"},
		},
		{
			name:       "empty action stays",
			resource:   "link",
			action:     "",
			args:       nil,
			wantAction: "",
			wantArgs:   nil,
		},
		{
			name:       "non-link resource untouched",
			resource:   "model",
			action:     "set",
			args:       []string{"gpt-4"},
			wantAction: "set",
			wantArgs:   []string{"gpt-4"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed := &ParsedCommand{Action: tc.action, Args: tc.args}
			normalizeLinkShorthand(tc.resource, parsed)
			if parsed.Action != tc.wantAction {
				t.Errorf("action = %q, want %q", parsed.Action, tc.wantAction)
			}
			if len(parsed.Args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", parsed.Args, tc.wantArgs)
			}
			for i, a := range parsed.Args {
				if a != tc.wantArgs[i] {
					t.Errorf("args[%d] = %q, want %q", i, a, tc.wantArgs[i])
				}
			}
		})
	}
}
