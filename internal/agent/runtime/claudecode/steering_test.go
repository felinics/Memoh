package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
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

func TestClaudeSteerUnconsumedCancelDoesNotEnterHistory(t *testing.T) {
	r := newTestRunner(&recordingSink{})
	r.input.Steering = &steerRecorder{}
	r.steers["one"] = &pendingSteer{input: external.SteerInput{ID: "one", Text: "never delivered"}, settled: make(chan error, 1)}
	feed(t, r, `{"type":"command_lifecycle","command_uuid":"one","state":"cancelled"}`)
	feed(t, r, `{"type":"result","subtype":"success","queued_turn_count":0}`)
	result, err := r.buildResult("")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SteerInputIDs) != 0 {
		t.Fatalf("unconsumed input persisted: %+v", result)
	}
}

func TestClaudeRecordedNativeLifecycle(t *testing.T) {
	raw, err := os.ReadFile("protocolref/lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		CLIVersion string `json:"claudeCodeCLI"`
		SDKVersion string `json:"claudeAgentSDK"`
		Now        struct {
			SteerID string            `json:"steer_id"`
			Events  []json.RawMessage `json:"events"`
		} `json:"now"`
		Next struct {
			SteerID string            `json:"steer_id"`
			Events  []json.RawMessage `json:"events"`
		} `json:"next"`
		Compact []json.RawMessage `json:"compact"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	versionRaw, err := os.ReadFile("protocolref/VERSION.json")
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		CLIVersion string `json:"claudeCodeCLI"`
		SDKVersion string `json:"claudeAgentSDK"`
	}
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		t.Fatal(err)
	}
	if fixture.CLIVersion != PinnedCLIVersion || fixture.CLIVersion != version.CLIVersion || fixture.SDKVersion != version.SDKVersion {
		t.Fatal("recapture native lifecycle fixtures when updating the SDK/CLI baseline")
	}
	for _, sample := range []struct {
		name, id                  string
		events                    []json.RawMessage
		inputTokens, outputTokens int
	}{{"now", fixture.Now.SteerID, fixture.Now.Events, 100, 10}, {"next", fixture.Next.SteerID, fixture.Next.Events, 200, 20}} {
		t.Run(sample.name, func(t *testing.T) {
			r := newTestRunner(&recordingSink{})
			r.input.Steering = &steerRecorder{}
			var cliVersion string
			r.onCLIVersion = func(_ context.Context, version string) { cliVersion = version }
			r.steers[sample.id] = &pendingSteer{input: external.SteerInput{ID: sample.id, Text: "follow-up"}, settled: make(chan error, 1)}
			for i, line := range sample.events {
				select {
				case <-r.done:
					t.Fatalf("finished before native event %d", i)
				default:
				}
				feed(t, r, string(line))
			}
			result, err := r.buildResult("")
			if err != nil || !result.TurnCompleted || len(result.SteerInputIDs) != 1 {
				t.Fatalf("result %+v, %v", result, err)
			}
			if cliVersion != PinnedCLIVersion || !r.steerSupported {
				t.Fatalf("unexpected native handshake: version %q, steer %v", cliVersion, r.steerSupported)
			}
			if result.Usage == nil || result.Usage.InputTokens != sample.inputTokens || result.Usage.OutputTokens != sample.outputTokens {
				t.Fatalf("native result usage was not accumulated: %+v", result.Usage)
			}
			select {
			case <-r.done:
			default:
				t.Fatal("native lifecycle did not settle the run")
			}
		})
	}
	r := newTestRunner(&recordingSink{})
	r.input.Command = "compact"
	for _, line := range fixture.Compact {
		feed(t, r, string(line))
	}
	if result, err := r.buildResult(""); err != nil || !result.TurnCompleted || result.RuntimeMetadata["claude_usage"] != nil {
		t.Fatalf("compact must complete without overwriting model usage: %+v, %v", result, err)
	}
}

type queuedSteering struct {
	steerRecorder
	inputs  chan external.SteerInput
	enabled chan struct{}
	closed  chan struct{}
}

func (s *queuedSteering) Enable(context.Context) error { close(s.enabled); return nil }
func (s *queuedSteering) Close(context.Context) error  { close(s.closed); return nil }
func (s *queuedSteering) Next(context.Context) (external.SteerInput, bool, error) {
	select {
	case input := <-s.inputs:
		return input, true, nil
	default:
		return external.SteerInput{}, false, nil
	}
}

func TestClaudeSteeringWorkerUsesNativeCapabilitiesAndInputIdentity(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(strconv.FormatBool(supported), func(t *testing.T) {
			r, p := controlTestRunner()
			defer r.close()
			source := &queuedSteering{inputs: make(chan external.SteerInput, 1), enabled: make(chan struct{}), closed: make(chan struct{})}
			source.inputs <- external.SteerInput{ID: "e93c6d72-f087-4e68-8600-bab4a9a68819", Text: "Change the approach"}
			r.input.Steering = source
			stop := r.startSteering(t.Context())
			defer stop()
			caps := `["msg_lifecycle_v1"]`
			if supported {
				caps = `["msg_lifecycle_v1","interrupt_cancel_queued_v1"]`
			}
			feed(t, r, `{"type":"system","subtype":"init","session_id":"native-session","capabilities":`+caps+`}`)
			if !supported {
				select {
				case <-source.closed:
				case <-time.After(time.Second):
					t.Fatal("unsupported worker remained active")
				}
				select {
				case <-source.enabled:
					t.Fatal("enabled steer without queued cancellation")
				default:
				}
				return
			}
			var message map[string]any
			select {
			case raw := <-p.writes:
				if err := json.Unmarshal(raw, &message); err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("no native input")
			}
			id := message["uuid"].(string)
			if id != "e93c6d72-f087-4e68-8600-bab4a9a68819" || message["priority"] != "now" || message["session_id"] != "native-session" {
				t.Fatalf("input %v", message)
			}
			feed(t, r, `{"type":"command_lifecycle","command_uuid":"`+id+`","state":"started"}`)
			feed(t, r, `{"type":"result","subtype":"success"}`)
			feed(t, r, `{"type":"command_lifecycle","command_uuid":"`+id+`","state":"completed"}`)
			stop()
			if len(source.ids) != 1 || source.ids[0] != id {
				t.Fatalf("consumed %v", source.ids)
			}
		})
	}
}

type blockingSteering struct {
	steerRecorder
	entered chan struct{}
}

func (s *blockingSteering) Next(ctx context.Context) (external.SteerInput, bool, error) {
	close(s.entered)
	<-ctx.Done()
	return external.SteerInput{}, false, ctx.Err()
}

func TestClaudeSteeringStopDoesNotReportUndeliveredInput(t *testing.T) {
	sink := &recordingSink{}
	r := newTestRunner(sink)
	defer r.close()
	source := &blockingSteering{entered: make(chan struct{})}
	r.input.Steering = source
	feed(t, r, `{"type":"system","subtype":"init","capabilities":["msg_lifecycle_v1","interrupt_cancel_queued_v1"]}`)
	stop := r.startSteering(t.Context())
	defer stop()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not read input")
	}
	feed(t, r, `{"type":"result","subtype":"success"}`)
	stop()
	for _, ev := range sink.snapshot() {
		if ev.Type == event.RuntimeNotice {
			t.Fatalf("normal shutdown emitted %s", ev.Code)
		}
	}
}
