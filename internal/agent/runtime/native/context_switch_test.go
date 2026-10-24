package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
)

func rowSources(ids ...string) []turn.ContextMessageSource {
	sources := make([]turn.ContextMessageSource, len(ids))
	for i, id := range ids {
		sources[i] = turn.ContextMessageSource{Kind: "history", ID: id}
	}
	return sources
}

// switchRequest is an armed run's request before its third step: an earlier
// summary, two history rows around a pipeline message the fold leaves alone
// and a passive message the run never rendered as a source, the run's input,
// its first step, a steer, and its latest step.
func switchRequest() ([]sdk.Message, []MessageProvenance) {
	call := func(id string) sdk.Message {
		return sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: id, ToolName: "effect", Input: toolexec.ArgumentsFromValue(map[string]any{"id": id})}}}
	}
	result := func(id string) sdk.Message {
		return sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: id, ToolName: "effect", Result: toolexec.OutputFromValue(id + strings.Repeat(" r", 2000))})
	}
	messages := []sdk.Message{
		sdk.UserMessage("<summary>\nolder\n"),
		sdk.UserMessage("h1" + strings.Repeat(" h", 2000)),
		sdk.UserMessage("pipeline message"),
		sdk.UserMessage("passive message"),
		sdk.AssistantMessage("h2" + strings.Repeat(" h", 2000)),
		sdk.UserMessage("INPUT"),
		call("c1"), result("c1"),
		sdk.UserMessage("STEER"),
		call("c2"), result("c2"),
	}
	sources := [][]turn.ContextMessageSource{
		{{Kind: "summary", ID: "A0"}},
		rowSources("h1"),
		{{Kind: "external", ID: "rc1"}},
		nil, rowSources("h2"),
		rowSources("input"), rowSources("c1"), rowSources("r1"), rowSources("steer"),
	}
	var provenance []MessageProvenance
	for i, source := range sources {
		if source != nil {
			provenance = append(provenance, MessageProvenance{Message: messages[i], Sources: source, Anchor: i == 5 || i == 8})
		}
	}
	return messages, provenance
}

// armedRun is a run armed with event that has committed the latest step of
// switchRequest, with an allowance only the request spliced to keep fits in.
func armedRun(t *testing.T, messages []sdk.Message, provenance []MessageProvenance, keep []int, event func(context.Context, ContextSwitch) (string, error)) (RunConfig, *error) {
	t.Helper()
	var failure error
	var kept []sdk.Message
	for _, i := range keep {
		kept = append(kept, messages[i])
	}
	cfg := RunConfig{
		ContextMutations:  contextfrag.NewMutationLedger(),
		ContextManifest:   contextfrag.Manifest{BudgetPlan: &contextfrag.ContextBudgetPlan{Window: contextfrag.ProviderEnvelopeTokens("", append(kept, sdk.UserMessage("<summary>\nnew\n")), nil) + 8}},
		SwitchContext:     event,
		ContextProvenance: provenance,
	}
	cfg.contextSwitch = newContextSwitchState(cfg)
	cfg.contextSwitch.committed(messages[9:], []MessageProvenance{{Sources: rowSources("c2")}, {Sources: rowSources("r2")}})
	cfg.contextStepFailure = func(err error) { failure = err }
	return cfg, &failure
}

func prepareArmed(cfg RunConfig, messages []sdk.Message) *sdk.Request {
	return prepareProviderAttempt(context.Background(), cfg, newProviderAttemptHandoff(cfg, 0), false, 6, 2, nil,
		&sdk.Request{Messages: append([]sdk.Message(nil), messages...)})
}

func switchDetails(cfg RunConfig) []string {
	var details []string
	for _, record := range cfg.ContextMutations.Records() {
		if record.Kind == contextfrag.MutationContextSwitch {
			details = append(details, record.Detail)
		}
	}
	return details
}

// The event sees what the request renders, keeps the input, the steer and the
// latest step out of the fold, and the request the run sends next is the one
// it had with the covered messages replaced by the summary at rule P: every
// other message is the same message, in its order.
func TestArmedRunFoldsItsOwnRequestInPlace(t *testing.T) {
	t.Parallel()

	messages, provenance := switchRequest()
	summary := sdk.UserMessage("<summary>\nnew\n")
	var seen ContextSwitch
	cfg, failure := armedRun(t, messages, provenance, []int{2, 3, 5, 8, 9, 10}, func(_ context.Context, sw ContextSwitch) (string, error) {
		seen = sw
		if tokens := sw.Fold(ContextFold{Artifact: "A1", Summary: summary, Covered: rowSources("h1", "h2", "c1", "r1"), Absorbed: []string{"A0"}}); tokens > sw.AllowanceTokens {
			t.Errorf("the spliced request takes %d tokens, over its %d allowance", tokens, sw.AllowanceTokens)
		}
		return "trigger=in_run passes=1", nil
	})
	cfg.ContextCachePlan = contextfrag.CachePlan{StablePrefixHash: "compiled", StableMessageCount: 5, StablePrefixTokenEstimate: 4000}
	params := prepareArmed(cfg, messages)

	want := []sdk.Message{messages[2], messages[3], messages[5], summary, messages[8], messages[9], messages[10]}
	if *failure != nil || !reflect.DeepEqual(params.Messages, want) {
		t.Fatalf("failure=%v messages=%d, want the request spliced at rule P with every other message unchanged", *failure, len(params.Messages))
	}
	view := append(append([]turn.ContextMessageSource{{Kind: "summary", ID: "A0"}}, rowSources("h1")...), turn.ContextMessageSource{Kind: "external", ID: "rc1"})
	view = append(view, rowSources("h2", "input", "c1", "r1", "steer", "c2", "r2")...)
	if !reflect.DeepEqual(seen.View, view) || !reflect.DeepEqual(seen.Anchors, rowSources("input", "steer")) || !reflect.DeepEqual(seen.Tail, rowSources("c2", "r2")) {
		t.Fatalf("view=%v anchors=%v tail=%v", seen.View, seen.Anchors, seen.Tail)
	}
	kept := contextfrag.ProviderEnvelopeTokens("", []sdk.Message{messages[3], messages[5], messages[8], messages[9], messages[10]}, nil)
	if seen.HistoryBudgetTokens != seen.AllowanceTokens-kept || !reflect.DeepEqual(switchDetails(cfg), []string{"trigger=in_run passes=1"}) {
		t.Fatalf("history budget=%d (allowance %d, kept %d) details=%q", seen.HistoryBudgetTokens, seen.AllowanceTokens, kept, switchDetails(cfg))
	}

	cfg.Messages = messages
	next, ok := cfg.contextSwitch.redispatch(cfg, generateDispatch{initialMessageCount: len(messages)}, *params)
	if !ok || len(next.Messages) != len(want) || next.initialProviderMessageCount != len(want) || next.ContextCachePlan.StableMessageCount != 2 {
		t.Fatalf("redispatch ok=%v messages=%d prefix=%d stable=%d, want the spliced request with its prefix and breakpoint moved", ok, len(next.Messages), next.initialProviderMessageCount, next.ContextCachePlan.StableMessageCount)
	}
	estimate := 4000 + messagesTokens(want[:2]) - messagesTokens(messages[:5])
	if plan := next.ContextCachePlan; plan.StablePrefixTokenEstimate != estimate || plan.StablePrefixHash != "" {
		t.Fatalf("cache plan = %+v, want the estimate %d of the spliced prefix and no compiled hash", plan, estimate)
	}
	if _, again := cfg.contextSwitch.redispatch(cfg, generateDispatch{}, *params); again {
		t.Fatal("a second redispatch without a splice")
	}
}

// An armed run whose event cannot make the request fit ends the run with the
// event's reason recorded and sends nothing: no step is dropped or trimmed
// to rescue it.
func TestArmedRunFailsClosedWhenTheEventDoesNotFit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		fold   []turn.ContextMessageSource
		err    error
		want   error
		detail string
	}{
		{name: "summarizer down", err: fmt.Errorf("%w: summarizer_failed", contextfrag.ErrBudgetUnsatisfied), want: contextfrag.ErrBudgetUnsatisfied, detail: "trigger=in_run passes=0 reason=summarizer_failed"},
		{name: "protected overflow", err: fmt.Errorf("%w: protected_overflow", contextfrag.ErrProtectedContextOverflow), want: contextfrag.ErrProtectedContextOverflow, detail: "trigger=in_run passes=0 reason=protected_overflow"},
		{name: "still over", fold: rowSources("h1"), want: contextfrag.ErrBudgetUnsatisfied, detail: "trigger=in_run reason=still_over"},
		{name: "result without its call", fold: rowSources("h1", "h2", "r1"), want: contextfrag.ErrBudgetUnsatisfied, detail: "trigger=in_run reason=postcondition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			messages, provenance := switchRequest()
			cfg, failure := armedRun(t, messages, provenance, []int{2, 3, 5, 8, 9, 10}, func(_ context.Context, sw ContextSwitch) (string, error) {
				if tc.fold != nil {
					sw.Fold(ContextFold{Artifact: "A1", Summary: sdk.UserMessage("<summary>\nnew\n"), Covered: tc.fold})
					return "", nil
				}
				return tc.detail, tc.err
			})
			prepareArmed(cfg, messages)
			details := switchDetails(cfg)
			if !errors.Is(*failure, tc.want) || len(details) != 1 || !strings.HasPrefix(details[0], tc.detail) {
				t.Fatalf("failure=%v details=%q, want %v recorded as %q", *failure, details, tc.want, tc.detail)
			}
		})
	}
}

// An armed run whose next step does not fit goes on from the request the
// event spliced, in both loops: the summary replaces the steps before the
// latest one, the input stays where it was, and the steps after the event
// are appended to the spliced request.
func TestArmedRunContinuesOnItsSplicedRequest(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			t.Parallel()
			var requests []sdk.Request
			provider := &atomicMockProvider{handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
				requests = append(requests, params)
				if call <= 4 {
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("c%d", call), ToolName: "effect", Input: toolexec.ArgumentsFromValue(map[string]any{"n": call}),
					}}}, nil
				}
				return sdk.ModelResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
			}}
			a := New(Deps{})
			a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
				Name: "effect", Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(strings.Repeat(" r", 2000)), nil
				},
			}}}})
			input := sdk.UserMessage("INPUT")
			summary := sdk.UserMessage("<summary>\nsteps\n")
			var events []ContextSwitch
			cfg := RunConfig{
				Model: &sdk.Model{ID: "mock-model", Provider: provider}, Messages: []sdk.Message{input}, SupportsToolCall: true,
				Identity: SessionContext{BotID: "bot-1", SessionID: "sess-1"}, ContextMutations: contextfrag.NewMutationLedger(),
				ContextManifest:   contextfrag.Manifest{BudgetPlan: &contextfrag.ContextBudgetPlan{Window: 2600}},
				ContextProvenance: []MessageProvenance{{Message: input, Sources: rowSources("input"), Anchor: true}},
				OnStepCommitted: func(_ context.Context, index int, record *step.Record) (StepDirective, error) {
					provenance := make([]MessageProvenance, len(record.Messages))
					for i := range provenance {
						provenance[i].Sources = rowSources(fmt.Sprintf("s%d.%d", index, i))
					}
					return StepDirective{Provenance: provenance}, nil
				},
				SwitchContext: func(_ context.Context, sw ContextSwitch) (string, error) {
					events = append(events, sw)
					sw.Fold(ContextFold{Artifact: "a1", Summary: summary, Covered: rowSources("s0.0", "s0.1", "s1.0", "s1.1")})
					return "trigger=in_run passes=1", nil
				},
			}
			if stream {
				for event := range a.Stream(context.Background(), cfg) {
					if event.Type == EventError {
						t.Fatalf("stream failed: %v", event.Cause)
					}
				}
			} else if _, err := a.Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			if len(requests) != 5 || len(events) != 1 || !reflect.DeepEqual(events[0].Tail, rowSources("s2.0", "s2.1")) {
				t.Fatalf("provider calls=%d events=%d, want one event before the fourth call with the third step as its tail", len(requests), len(events))
			}
			spliced, next := requests[3].Messages, requests[4].Messages
			call, _ := spliced[min(2, len(spliced)-1)].Content[0].(sdk.ToolCallPart)
			if len(spliced) != 4 || !reflect.DeepEqual(spliced[0], input) || !reflect.DeepEqual(spliced[1], summary) || call.ToolCallID != "c3" || spliced[3].Role != sdk.MessageRoleTool {
				t.Fatalf("fourth call sent %d messages, want the input, the summary and the latest step", len(spliced))
			}
			if len(next) != 6 || !reflect.DeepEqual(next[:4], spliced) {
				t.Fatalf("fifth call sent %d messages, want the spliced request with the fourth step appended", len(next))
			}
		})
	}
}
