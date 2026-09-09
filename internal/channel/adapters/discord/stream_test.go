package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDiscordOutboundStream_PushErrorEventRedactsSecrets(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)

	const token = "discord-token-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	redact.SetSecrets("test", token)
	prefixHalf := token[:len(token)/2]

	var sentBody string
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.Client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			sentBody = string(body)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg-1","channel_id":"ch-1"}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}
			return resp, nil
		}),
	}

	stream := &discordOutboundStream{
		adapter: &DiscordAdapter{},
		target:  "ch-1",
		session: session,
	}

	err = stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventError,
		Error: "request failed: " + prefixHalf,
	})
	if err != nil {
		t.Fatalf("push error event: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(sentBody), &payload); err != nil {
		t.Fatalf("decode sent body: %v (body=%q)", err, sentBody)
	}
	content, _ := payload["content"].(string)
	if strings.Contains(content, prefixHalf) {
		t.Fatalf("expected prefix half to be redacted, got %q", content)
	}
	if !strings.Contains(content, "Error: ") {
		t.Fatalf("expected error prefix, got %q", content)
	}
	if !strings.Contains(content, strings.Repeat("*", len(prefixHalf))) {
		t.Fatalf("expected redaction mask, got %q", content)
	}
}

func TestDiscordOutboundStream_FinalUsesPartsRenderer(t *testing.T) {
	t.Parallel()

	var sentBody string
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.Client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			sentBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg-rich","channel_id":"ch-1"}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}),
	}

	stream := &discordOutboundStream{
		adapter: &DiscordAdapter{},
		target:  "ch-1",
		session: session,
	}

	err = stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{
				Message: channel.Message{
					Text:   "plain fallback",
					Format: channel.MessageFormatRich,
					Parts: []channel.MessagePart{
						{Type: channel.MessagePartText, Text: "Hello", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
						{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/a"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("push rich final: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(sentBody), &payload); err != nil {
		t.Fatalf("decode sent body: %v (body=%q)", err, sentBody)
	}
	want := "**Hello**\n\n[docs](https://example.test/a)"
	if payload["content"] != want {
		t.Fatalf("Discord stream rich content mismatch\n  got:  %q\n  want: %q", payload["content"], want)
	}
}

func TestRenderDiscordStreamFinalTextUsesAuthoritativeTextBeforeBuffer(t *testing.T) {
	t.Parallel()

	got := renderDiscordStreamFinalText(channel.Message{Text: "final answer"}, "partial buffered text")
	if got != "final answer" {
		t.Fatalf("expected authoritative final text, got %q", got)
	}
}

func TestRenderDiscordStreamFinalTextFallsBackWhenEscapedRichOverflows(t *testing.T) {
	t.Parallel()

	text := strings.Repeat(`\`, discordMaxLength)
	msg := channel.Message{Parts: []channel.MessagePart{{
		Type: channel.MessagePartText,
		Text: text,
	}}}
	if got := renderDiscordStreamFinalText(msg, ""); got != text {
		t.Fatalf("expected plain fallback when escaped rich overflows, got len=%d", len([]rune(got)))
	}
}

func TestDiscordOutboundStream_FinalPreservesURLActionsOnEdit(t *testing.T) {
	t.Parallel()

	var editBody string
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.Client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			if req.Method == http.MethodPatch {
				editBody = string(body)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg-1","channel_id":"ch-1"}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		}),
	}

	stream := &discordOutboundStream{
		adapter: &DiscordAdapter{},
		target:  "ch-1",
		session: session,
	}

	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:   channel.StreamEventStatus,
		Status: channel.StreamStatusStarted,
	}); err != nil {
		t.Fatalf("status push: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{
				Message: channel.Message{
					Text: "Read this",
					Actions: []channel.Action{
						{Label: "Open docs", URL: "https://example.test/docs"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("final push: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(editBody), &payload); err != nil {
		t.Fatalf("decode edit body: %v (body=%q)", err, editBody)
	}
	components, ok := payload["components"].([]any)
	if !ok || len(components) != 1 {
		t.Fatalf("expected one component row, got %#v", payload["components"])
	}
	row, ok := components[0].(map[string]any)
	if !ok {
		t.Fatalf("expected component row object, got %#v", components[0])
	}
	buttons, ok := row["components"].([]any)
	if !ok || len(buttons) != 1 {
		t.Fatalf("expected one button, got %#v", row["components"])
	}
	button, ok := buttons[0].(map[string]any)
	if !ok {
		t.Fatalf("expected button object, got %#v", buttons[0])
	}
	if button["label"] != "Open docs" || button["url"] != "https://example.test/docs" || button["style"] != float64(discordgo.LinkButton) {
		t.Fatalf("unexpected button payload: %#v", button)
	}
	allowed, ok := payload["allowed_mentions"].(map[string]any)
	if !ok {
		t.Fatalf("expected explicit allowed_mentions on edit, got %#v", payload["allowed_mentions"])
	}
	if parse, ok := allowed["parse"].([]any); !ok || len(parse) != 0 {
		t.Fatalf("expected parse=[] to suppress ambient mentions, got %#v", allowed["parse"])
	}
}
