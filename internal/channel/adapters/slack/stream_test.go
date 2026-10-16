package slack

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/slack-go/slack"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func TestSlackStreamErrorReply(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)
	const secret = "slack-secret-value-123456"
	redact.SetSecrets("slack-stream-test", secret)

	cases := []struct {
		name  string
		event channel.PreparedStreamEvent
		want  []string
	}{
		{
			name:  "coded error shows the copy as it is",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "The workspace is unreachable.", ErrorCode: "workspace.unreachable"},
			want:  []string{"The workspace is unreachable."},
		},
		{
			name:  "uncoded error is redacted and labelled",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "request failed with token " + secret},
			want:  []string{"Error: request failed with token " + strings.Repeat("*", len(secret))},
		},
		{
			name:  "blank error sends nothing",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "  "},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent []string
			api := slack.New(
				testBotToken,
				slack.OptionAPIURL("https://slack.test/api/"),
				slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "https://slack.test/api/chat.postMessage" {
						t.Errorf("unexpected request %s", r.URL.String())
						return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
					}
					if err := r.ParseForm(); err != nil {
						t.Errorf("ParseForm: %v", err)
					}
					sent = append(sent, r.FormValue("text"))
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"ok":true,"channel":"C123","ts":"1710000000.000100"}`)),
					}, nil
				})}),
			)
			stream := &slackOutboundStream{
				adapter: NewSlackAdapter(nil),
				target:  "C123",
				api:     api,
			}
			if err := stream.Push(context.Background(), tc.event); err != nil {
				t.Fatalf("push error: %v", err)
			}
			if len(sent) != len(tc.want) || strings.Join(sent, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("sent messages = %q, want %q", sent, tc.want)
			}
		})
	}
}

type slackRecordedCall struct {
	method string
	ts     string
	text   string
}

func newSlackRecordingStream(t *testing.T) (*slackOutboundStream, *[]slackRecordedCall) {
	t.Helper()
	var calls []slackRecordedCall
	posts := 0
	api := slack.New(
		testBotToken,
		slack.OptionAPIURL("https://slack.test/api/"),
		slack.OptionHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			method := strings.TrimPrefix(r.URL.Path, "/api/")
			ts := r.FormValue("ts")
			if method == "chat.postMessage" {
				posts++
				ts = "1710000000.00010" + strconv.Itoa(posts)
			}
			calls = append(calls, slackRecordedCall{method: method, ts: ts, text: r.FormValue("text")})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"channel":"C123","ts":"` + ts + `"}`)),
			}, nil
		})}),
	)
	return &slackOutboundStream{adapter: NewSlackAdapter(nil), target: "C123", api: api}, &calls
}

func pushSlackEvents(t *testing.T, stream *slackOutboundStream, events []channel.PreparedStreamEvent) {
	t.Helper()
	for _, event := range events {
		if err := stream.Push(context.Background(), event); err != nil {
			t.Fatalf("push %s: %v", event.Type, err)
		}
	}
}

func slackFinalEvent(text string) channel.PreparedStreamEvent {
	return channel.PreparedStreamEvent{
		Type:  channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{Message: channel.PreparedMessage{Message: channel.Message{Text: text}}},
	}
}

func TestSlackStreamSegmentEndKeepsPreToolText(t *testing.T) {
	stream, calls := newSlackRecordingStream(t)
	pushSlackEvents(t, stream, []channel.PreparedStreamEvent{
		{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted},
		{Type: channel.StreamEventDelta, Delta: "Let me check.", Phase: channel.StreamPhaseText},
		{Type: channel.StreamEventSegmentEnd},
		{Type: channel.StreamEventDelta, Delta: "Here is the answer.", Phase: channel.StreamPhaseText},
		slackFinalEvent("Here is the answer."),
	})

	want := []slackRecordedCall{
		{method: "chat.postMessage", ts: "1710000000.000101", text: "Thinking..."},
		{method: "chat.update", ts: "1710000000.000101", text: "Let me check."},
		{method: "chat.postMessage", ts: "1710000000.000102", text: "Here is the answer."},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %+v, want %+v", *calls, want)
	}
}

func TestSlackStreamSegmentEndWithoutTextKeepsPlaceholder(t *testing.T) {
	stream, calls := newSlackRecordingStream(t)
	pushSlackEvents(t, stream, []channel.PreparedStreamEvent{
		{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted},
		{Type: channel.StreamEventSegmentEnd},
		{Type: channel.StreamEventSegmentEnd},
		{Type: channel.StreamEventDelta, Delta: "Here is the answer.", Phase: channel.StreamPhaseText},
		slackFinalEvent("Here is the answer."),
	})

	want := []slackRecordedCall{
		{method: "chat.postMessage", ts: "1710000000.000101", text: "Thinking..."},
		{method: "chat.update", ts: "1710000000.000101", text: "Here is the answer."},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %+v, want %+v", *calls, want)
	}
}
