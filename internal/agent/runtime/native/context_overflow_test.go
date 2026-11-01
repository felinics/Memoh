package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestAgentGenerateCompactsProviderOverflowBeforeRetryingSameStep(t *testing.T) {
	history := sdk.UserMessage(strings.Repeat("old evidence ", 8000))
	input := sdk.UserMessage("Preserve the current task")
	folds, commits := 0, 0
	provider := &atomicMockProvider{handler: func(call int, req sdk.Request) (sdk.ModelResult, error) {
		if call == 1 {
			return sdk.ModelResult{}, &sdk.APIError{StatusCode: 400, Code: "context_length_exceeded"}
		}
		if folds != 1 || len(req.Messages) != 2 || !providerAttemptContainsText(req.Messages, "Preserve the current task") || providerAttemptContainsText(req.Messages, "old evidence") {
			t.Fatalf("retry did not adopt the committed fold: folds=%d messages=%d", folds, len(req.Messages))
		}
		return sdk.ModelResult{Text: "recovered", FinishReason: sdk.FinishReasonStop}, nil
	}}
	cfg := generateRetryRun(provider, fastRetry)
	cfg.Messages = []sdk.Message{history, input}
	cfg.ContextBudgetMaxTokens, cfg.ModelMaxOutputTokens = 200000, 8000
	cfg.ContextProvenance = []MessageProvenance{{Message: history, Sources: rowSources("history")}, {Message: input, Sources: rowSources("input"), Anchor: true}}
	cfg.SwitchContext = func(_ context.Context, sw ContextSwitch) (string, error) {
		folds++
		if sw.UsageTokens >= 192000 {
			t.Fatal("fixture must pass the original local budget")
		}
		sw.Fold(ContextFold{SummaryID: "checkpoint", Summary: sdk.UserMessage("Earlier evidence summarized"), Order: []int{-1, 1}})
		return "committed checkpoint", nil
	}
	cfg.OnStepCommitted = func(_ context.Context, index int, _ *step.Record) (StepDirective, error) {
		if index != 0 {
			t.Fatalf("step index=%d, want original step 0", index)
		}
		commits++
		return StepDirective{}, nil
	}
	result, err := New(Deps{}).Generate(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "recovered" || folds != 1 || commits != 1 || provider.calls.Load() != 2 {
		t.Fatalf("text=%q folds=%d commits=%d calls=%d", result.Text, folds, commits, provider.calls.Load())
	}
	if estimate := contextfrag.ProviderEnvelopeTokens("", cfg.Messages, nil); estimate >= 192000 {
		t.Fatal("fixture exceeds configured allowance")
	}
}

// A provider that reports its counts says how far the request was over: the
// recovery folds to that, less a margin, rather than a fixed cut, and the
// provider's count calibrates the steps that follow.
func TestContextOverflowFoldsToTheProviderReportedLimit(t *testing.T) {
	history := sdk.UserMessage(strings.Repeat("old evidence ", 8000))
	input := sdk.UserMessage("current task")
	rejected := 0
	var events []ContextSwitch
	provider := &atomicMockProvider{handler: func(call int, req sdk.Request) (sdk.ModelResult, error) {
		if call == 1 {
			rejected = contextfrag.ProviderEnvelopeTokens(req.System, req.Messages, req.Tools)
			return sdk.ModelResult{}, &sdk.APIError{StatusCode: 400, Type: "invalid_request_error", Message: fmt.Sprintf("prompt is too long: %d tokens > %d maximum", rejected*21/20, rejected)}
		}
		return sdk.ModelResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	cfg := generateRetryRun(provider, fastRetry)
	cfg.Messages = []sdk.Message{history, input}
	cfg.ContextBudgetMaxTokens, cfg.ModelMaxOutputTokens = 200000, 8000
	cfg.ContextProvenance = []MessageProvenance{{Message: history, Sources: rowSources("history")}, {Message: input, Sources: rowSources("input"), Anchor: true}}
	cfg.SwitchContext = func(_ context.Context, sw ContextSwitch) (string, error) {
		events = append(events, sw)
		sw.Fold(ContextFold{SummaryID: "checkpoint", Summary: sdk.UserMessage("Earlier evidence summarized"), Order: []int{-1, 1}})
		return "committed checkpoint", nil
	}
	if _, err := New(Deps{}).Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want the one recovery", len(events))
	}
	counted := rejected * 21 / 20
	want, density := int(float64(rejected)*float64(rejected)/float64(counted)*0.95), float64(counted)/float64(rejected)
	if sw := events[0]; sw.UsageTokens != rejected || sw.AllowanceTokens != want || sw.Density != density {
		t.Fatalf("event usage=%d allowance=%d density=%v, want %d folded to %d at density %v", sw.UsageTokens, sw.AllowanceTokens, sw.Density, rejected, want, density)
	}
	if want <= rejected*3/4 {
		t.Fatalf("allowance %d of %d is no tighter than the uncounted cut", want, rejected)
	}
}

func TestEmptyContextExhaustionRecoversBeforeCommittingInBothLoops(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			folds, commits := 0, 0
			provider := &atomicMockProvider{handler: func(call int, req sdk.Request) (sdk.ModelResult, error) {
				if call == 1 {
					return sdk.ModelResult{FinishReason: sdk.FinishReasonLength, Usage: sdk.Usage{InputTokens: 1048570, CachedInputTokens: 1048448, TotalTokens: 1048570}}, nil
				}
				if folds != 1 || providerAttemptContainsText(req.Messages, "old evidence") || !providerAttemptContainsText(req.Messages, "current task") {
					t.Fatal("empty exhausted response retried without a committed reduction")
				}
				return sdk.ModelResult{Text: "done", FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{InputTokens: 100, OutputTokens: 1, TotalTokens: 101}}, nil
			}}
			history := sdk.UserMessage(strings.Repeat("old evidence ", 8000))
			input := sdk.UserMessage("current task")
			cfg := generateRetryRun(provider, fastRetry)
			cfg.Messages = []sdk.Message{history, input}
			cfg.ContextBudgetMaxTokens, cfg.ModelMaxOutputTokens = 1000000, 8192
			cfg.ContextProvenance = []MessageProvenance{{Message: history, Sources: rowSources("history")}, {Message: input, Sources: rowSources("input"), Anchor: true}}
			cfg.SwitchContext = func(_ context.Context, sw ContextSwitch) (string, error) {
				folds++
				sw.Fold(ContextFold{SummaryID: "checkpoint", Summary: sdk.UserMessage("Earlier evidence summarized"), Order: []int{-1, 1}})
				return "committed checkpoint", nil
			}
			cfg.OnStepCommitted = func(_ context.Context, index int, record *step.Record) (StepDirective, error) {
				commits++
				if index != 0 || record.Result.Text != "done" {
					t.Errorf("committed empty exhaustion as completed work: index=%d text=%q", index, record.Result.Text)
				}
				return StepDirective{}, nil
			}
			a := New(Deps{})
			var usage sdk.Usage
			if stream {
				stepEnds, sawText := 0, false
				for event := range a.Stream(context.Background(), cfg) {
					if event.Type == EventError {
						t.Fatal(event.Cause)
					}
					if event.Type == EventRuntimeNotice {
						t.Error("recovered empty attempt emitted an output-truncation notice")
					}
					if event.Type == EventTextDelta {
						sawText = true
					}
					if event.Type == EventStepEnd {
						stepEnds++
						if !sawText || event.StepNumber != 0 {
							t.Error("unfinished attempt released the step-consumed barrier")
						}
					}
					if event.Type == EventAgentEnd {
						if err := json.Unmarshal(event.Usage, &usage); err != nil {
							t.Fatal(err)
						}
					}
				}
				if stepEnds != 1 {
					t.Errorf("step-end markers=%d, want only the committed step", stepEnds)
				}
			} else {
				result, err := a.Generate(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				usage = *result.Usage
			}
			// Like any attempt that does not commit, the exhausted one is not
			// part of the run's usage, which the committed steps make up.
			if usage.InputTokens != 100 || usage.OutputTokens != 1 || usage.TotalTokens != 101 || usage.CachedInputTokens != 0 {
				t.Errorf("usage = %+v, want only the committed retry", usage)
			}
			if folds != 1 || commits != 1 || provider.calls.Load() != 2 {
				t.Fatalf("folds=%d commits=%d calls=%d", folds, commits, provider.calls.Load())
			}
		})
	}
}

func TestContextOverflowKeepsCommittedToolEffectsInBothLoops(t *testing.T) {
	for _, emptyLength := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("empty_length=%v/stream=%v", emptyLength, stream), func(t *testing.T) {
				effects, commits, folds, committedInputs := 0, 0, 0, 0
				provider := &atomicMockProvider{handler: func(call int, req sdk.Request) (sdk.ModelResult, error) {
					switch call {
					case 1:
						return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "effect-1", ToolName: "effect", Input: toolexec.ArgumentsFromValue(map[string]any{})}}}, nil
					case 2:
						if emptyLength {
							return sdk.ModelResult{FinishReason: sdk.FinishReasonLength, Usage: sdk.Usage{InputTokens: 1048570, TotalTokens: 1048570}}, nil
						}
						return sdk.ModelResult{}, &sdk.APIError{Code: "context_length_exceeded"}
					default:
						if !providerAttemptContainsText(req.Messages, "STEER: retain the current goal") {
							t.Fatal("retry lost the admitted steer")
						}
						if effects != 1 || folds != 1 || commits != 1 {
							t.Fatalf("effects=%d folds=%d commits=%d", effects, folds, commits)
						}
						if callID, resultID := unpairedToolParts(req.Messages); callID != "" || resultID != "" {
							t.Fatal("retry broke call/result pairing")
						}
						foundEffect := false
						for _, message := range req.Messages {
							for _, part := range message.Content {
								if result, ok := part.(sdk.ToolResultPart); ok && strings.Contains(result.Result.String(), "effect completed") {
									foundEffect = true
								}
							}
						}
						if !foundEffect {
							t.Fatal("retry lost the completed effect")
						}
						return sdk.ModelResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
					}
				}}
				a := New(Deps{})
				a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{Name: "effect", Parameters: &jsonschema.Schema{Type: "object"}, Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
					effects++
					return sdk.TextOutput("effect completed"), nil
				}}}}})
				history := sdk.UserMessage(strings.Repeat("evidence ", 10000))
				input := sdk.UserMessage("Perform one effect then finish")
				cfg := generateRetryRun(provider, fastRetry)
				cfg.Messages = []sdk.Message{history, input}
				cfg.SupportsToolCall = true
				cfg.ContextBudgetMaxTokens, cfg.ModelMaxOutputTokens = 1000000, 8192
				cfg.ContextProvenance = []MessageProvenance{{Message: history, Sources: rowSources("history")}, {Message: input, Sources: rowSources("input"), Anchor: true}}
				cfg.SwitchContext = func(_ context.Context, sw ContextSwitch) (string, error) {
					folds++
					sw.Fold(foldCovering(sw, "checkpoint", sdk.UserMessage("Evidence summarized"), rowSources("history")...))
					return "committed", nil
				}
				cfg.OnStepCommitted = func(_ context.Context, index int, record *step.Record) (StepDirective, error) {
					if index != commits {
						t.Fatalf("commit index=%d, want %d", index, commits)
					}
					commits++
					if providerAttemptContainsText(record.Messages, "STEER: retain the current goal") {
						committedInputs++
					}
					var provenance []MessageProvenance
					for i := range record.Messages {
						provenance = append(provenance, MessageProvenance{Sources: rowSources(fmt.Sprintf("step-%d-%d", index, i))})
					}
					dir := StepDirective{Provenance: provenance}
					if index == 0 {
						dir.NextInputs = []DirectiveInput{{ID: "steer-1", Text: "STEER: retain the current goal"}}
					}
					return dir, nil
				}
				if stream {
					for event := range a.Stream(context.Background(), cfg) {
						if event.Type == EventError {
							t.Fatal(event.Cause)
						}
					}
				} else if _, err := a.Generate(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				if effects != 1 || commits != 2 || folds != 1 || provider.calls.Load() != 3 {
					t.Fatalf("effects=%d commits=%d folds=%d calls=%d", effects, commits, folds, provider.calls.Load())
				}
				if committedInputs != 1 {
					t.Fatalf("persisted steers=%d, want the admitted steer exactly once", committedInputs)
				}
			})
		}
	}
}

func TestLengthFinishesDoNotReplayCompletedOrUnmeasuredWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result sdk.ModelResult
	}{
		{"text", sdk.ModelResult{Text: "partial answer", FinishReason: sdk.FinishReasonLength, Usage: sdk.Usage{InputTokens: 1048570}}},
		{"output budget", sdk.ModelResult{FinishReason: sdk.FinishReasonLength, Usage: sdk.Usage{InputTokens: 1048570, OutputTokens: 8192}}},
		{"input fits", sdk.ModelResult{FinishReason: sdk.FinishReasonLength, Usage: sdk.Usage{InputTokens: 991808}}},
		{"missing usage", sdk.ModelResult{FinishReason: sdk.FinishReasonLength}},
		{"intentional silence", sdk.ModelResult{FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{InputTokens: 1048570}}},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				provider := &atomicMockProvider{handler: func(int, sdk.Request) (sdk.ModelResult, error) { return tc.result, nil }}
				cfg := generateRetryRun(provider, fastRetry)
				cfg.ContextBudgetMaxTokens, cfg.ModelMaxOutputTokens = 1000000, 8192
				cfg.SwitchContext = func(context.Context, ContextSwitch) (string, error) {
					t.Fatal("finished output was compacted for replay")
					return "", nil
				}
				a := New(Deps{})
				if stream {
					for event := range a.Stream(context.Background(), cfg) {
						if event.Type == EventError {
							t.Fatal(event.Cause)
						}
					}
				} else if _, err := a.Generate(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				if provider.calls.Load() != 1 {
					t.Fatalf("calls=%d, want no replay", provider.calls.Load())
				}
			})
		}
	}
}

func TestStreamLengthFinishDoesNotReplayNonTextOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		part sdk.StreamPart
	}{
		{"file", &sdk.StreamFilePart{File: sdk.GeneratedFile{MediaType: "image/png", Data: "eA=="}}},
		{"source", &sdk.StreamSourcePart{}},
		{"reasoning", &sdk.ReasoningDeltaPart{ID: "reasoning", Text: "already reasoned"}},
		{"tool input", &sdk.ToolInputStartPart{ID: "partial", ToolName: "effect"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
				calls++
				return closedAgentTestStream(tc.part, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonLength, Usage: sdk.Usage{InputTokens: 1048570}}), nil
			})
			cfg := generateRetryRun(provider, fastRetry)
			cfg.ContextBudgetMaxTokens, cfg.ModelMaxOutputTokens = 1000000, 8192
			cfg.SwitchContext = func(context.Context, ContextSwitch) (string, error) {
				t.Fatal("non-text output was compacted for replay")
				return "", nil
			}
			for event := range New(Deps{}).Stream(context.Background(), cfg) {
				if event.Type == EventError {
					t.Fatal(event.Cause)
				}
			}
			if calls != 1 {
				t.Fatalf("calls=%d, want no replay", calls)
			}
		})
	}
}

func TestStreamContextOverflowOnlyRecoversBeforeOutput(t *testing.T) {
	for _, output := range []string{"none", "before error", "after error", "source before error", "source after error"} {
		t.Run(output, func(t *testing.T) {
			calls, folds := 0, 0
			provider := agentStreamTestProvider(func(_ context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
				calls++
				if calls == 1 {
					parts := []sdk.StreamPart{&sdk.StartStepPart{}}
					if output == "before error" {
						parts = append(parts, &sdk.TextDeltaPart{ID: "text", Text: "partial answer"})
					}
					if output == "source before error" {
						parts = append(parts, &sdk.StreamSourcePart{})
					}
					parts = append(parts, &sdk.ErrorPart{Error: &sdk.APIError{Code: "context_length_exceeded"}})
					if output == "after error" {
						parts = append(parts, &sdk.TextDeltaPart{ID: "text", Text: "buffered answer"})
					}
					if output == "source after error" {
						parts = append(parts, &sdk.StreamSourcePart{})
					}
					return closedAgentTestStream(parts...), nil
				}
				return closedAgentTestStream(&sdk.TextDeltaPart{ID: "text", Text: "done"}, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
			})
			cfg := generateRetryRun(provider, fastRetry)
			history, input := sdk.UserMessage(strings.Repeat("history ", 10000)), sdk.UserMessage("task")
			cfg.Messages = []sdk.Message{history, input}
			cfg.ContextBudgetMaxTokens, cfg.ModelMaxOutputTokens = 200000, 8000
			cfg.ContextProvenance = []MessageProvenance{{Message: history, Sources: rowSources("history")}, {Message: input, Sources: rowSources("input"), Anchor: true}}
			cfg.SwitchContext = func(_ context.Context, sw ContextSwitch) (string, error) {
				folds++
				sw.Fold(ContextFold{SummaryID: "checkpoint", Summary: sdk.UserMessage("summary"), Order: []int{-1, 1}})
				return "committed", nil
			}
			failed := false
			for event := range New(Deps{}).Stream(context.Background(), cfg) {
				failed = failed || event.Type == EventError
			}
			if output != "none" {
				if !failed || calls != 1 || folds != 0 {
					t.Fatalf("failed=%v calls=%d folds=%d", failed, calls, folds)
				}
			} else if failed || calls != 2 || folds != 1 {
				t.Fatalf("failed=%v calls=%d folds=%d", failed, calls, folds)
			}
		})
	}
}

func TestContextOverflowDoesNotRetryWithoutCommittedReduction(t *testing.T) {
	for _, mode := range []string{"no pass", "unchanged", "commit failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			provider := &atomicMockProvider{handler: func(int, sdk.Request) (sdk.ModelResult, error) {
				return sdk.ModelResult{}, &sdk.APIError{Code: "context_length_exceeded"}
			}}
			cfg := generateRetryRun(provider, fastRetry)
			cfg.ContextBudgetMaxTokens = 200000
			history, input := sdk.UserMessage(strings.Repeat("history ", 8000)), sdk.UserMessage("task")
			cfg.Messages = []sdk.Message{history, input}
			cfg.ContextProvenance = []MessageProvenance{{Message: history, Sources: rowSources("history")}, {Message: input, Sources: rowSources("input"), Anchor: true}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := 0
			cfg.SwitchContext = func(_ context.Context, sw ContextSwitch) (string, error) {
				events++
				switch mode {
				case "unchanged":
					sw.Fold(ContextFold{Order: []int{0, 1}})
				case "commit failure":
					return "", errors.New("checkpoint commit failed")
				case "cancel":
					cancel()
					return "", ctx.Err()
				}
				return "", nil
			}
			_, err := New(Deps{}).Generate(ctx, cfg)
			if err == nil || events != 1 || provider.calls.Load() != 1 {
				t.Fatalf("err=%v events=%d calls=%d", err, events, provider.calls.Load())
			}
		})
	}
}

func TestContextOverflowRecoveryIsBoundedPerStep(t *testing.T) {
	provider := &atomicMockProvider{handler: func(int, sdk.Request) (sdk.ModelResult, error) {
		return sdk.ModelResult{}, &sdk.APIError{Code: "context_length_exceeded"}
	}}
	cfg := generateRetryRun(provider, fastRetry)
	cfg.ContextBudgetMaxTokens = 200000
	history, input := sdk.UserMessage(strings.Repeat("history ", 8000)), sdk.UserMessage("task")
	cfg.Messages = []sdk.Message{history, input}
	cfg.ContextProvenance = []MessageProvenance{{Message: history, Sources: rowSources("history")}, {Message: input, Sources: rowSources("input"), Anchor: true}}
	folds := 0
	cfg.SwitchContext = func(_ context.Context, sw ContextSwitch) (string, error) {
		folds++
		text := sw.Request.Messages[0].Content[0].(sdk.TextPart).Text
		sw.Fold(ContextFold{SummaryID: fmt.Sprintf("summary-%d", folds), Summary: sdk.UserMessage(text[:len(text)/2]), Order: []int{-1, 1}})
		return "committed", nil
	}
	_, err := New(Deps{}).Generate(context.Background(), cfg)
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) || folds != 2 || provider.calls.Load() != 3 {
		t.Fatalf("err=%v folds=%d calls=%d", err, folds, provider.calls.Load())
	}
}
