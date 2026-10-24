package application

import (
	"testing"

	"github.com/felinics/memoh/internal/chat/timeline"
)

// The pipeline is a request's context only when it holds the request's own
// message; a session it warmed for other messages is not.
func TestPipelineHoldsMessage(t *testing.T) {
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent("session-1", timeline.MessageEvent{SessionID: "session-1", MessageID: "msg-1", ReceivedAtMs: 1000, TimestampSec: 1, Content: []timeline.ContentNode{{Type: "text", Text: "hello"}}})
	s := &Service{pipeline: pipeline}
	for name, want := range map[[2]string]bool{
		{"session-1", "msg-1"}: true,
		{"session-1", "msg-2"}: false,
		{"session-1", ""}:      false,
		{"session-2", "msg-1"}: false,
	} {
		if got := s.pipelineHoldsMessage(name[0], name[1]); got != want {
			t.Errorf("pipelineHoldsMessage(%q, %q) = %v, want %v", name[0], name[1], got, want)
		}
	}
	if (&Service{}).pipelineHoldsMessage("session-1", "msg-1") {
		t.Error("a service without a pipeline reported holding the message")
	}
}
