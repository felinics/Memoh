package claudecode

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type steerRecorder struct{ ids []string }

func (*steerRecorder) Enable(context.Context) error { return nil }
func (*steerRecorder) Wake() <-chan struct{}        { return nil }
func (*steerRecorder) Next(context.Context) (external.SteerInput, bool, error) {
	return external.SteerInput{}, false, nil
}

func (s *steerRecorder) Accepted(_ context.Context, id string, _ int) error {
	s.ids = append(s.ids, id)
	return nil
}
func (*steerRecorder) Close(context.Context) error { return nil }

func TestClaudeSteerLifecyclePreservesTranscriptAndWaitsForResult(t *testing.T) {
	r := newTestRunner(&recordingSink{})
	source := &steerRecorder{}
	r.input.Steering = source
	r.steers["one"] = &pendingSteer{input: external.SteerInput{ID: "one", Text: "same"}, settled: make(chan error, 1)}
	r.steers["two"] = &pendingSteer{input: external.SteerInput{ID: "two", Text: "same"}, step: 1, settled: make(chan error, 1)}
	feed(t, r, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"before"}}}`)
	feed(t, r, `{"type":"command_lifecycle","command_uuid":"one","state":"queued"}`)
	if len(source.ids) != 0 {
		t.Fatal("queued input was acknowledged as consumed")
	}
	feed(t, r, `{"type":"command_lifecycle","command_uuid":"one","state":"started"}`)
	feed(t, r, `{"type":"command_lifecycle","command_uuid":"one","state":"started"}`)
	feed(t, r, `{"type":"assistant","message":{"content":[{"type":"text","text":"middle"}]}}`)
	feed(t, r, `{"type":"result","subtype":"success","queued_turn_count":1}`)
	feed(t, r, `{"type":"command_lifecycle","command_uuid":"one","state":"completed"}`)
	select {
	case <-r.done:
		t.Fatal("first result lost pending input")
	default:
	}
	feed(t, r, `{"type":"command_lifecycle","command_uuid":"two","state":"started"}`)
	feed(t, r, `{"type":"command_lifecycle","command_uuid":"two","state":"completed"}`)
	select {
	case <-r.done:
		t.Fatal("completion event preceded the final result")
	default:
	}
	feed(t, r, `{"type":"result","subtype":"success","queued_turn_count":0,"result":"after"}`)
	select {
	case <-r.done:
	default:
		t.Fatal("final result did not complete")
	}
	result, err := r.buildResult("")
	if err != nil {
		t.Fatal(err)
	}
	if len(source.ids) != 2 || len(result.SteerInputIDs) != 2 || len(result.Output) != 5 {
		t.Fatalf("source %v, result %+v", source.ids, result)
	}
	for index, text := range map[int]string{0: "before", 2: "middle", 4: "after"} {
		part, ok := result.Output[index].Content[0].(sdk.TextPart)
		if !ok || part.Text != text {
			t.Fatalf("reply missing or duplicated: %#v", result.Output[index])
		}
	}
	if result.AgentTurnID != "" {
		t.Fatal("invented a Claude fork anchor")
	}
}
