package native

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/models"
)

type capabilityRefreshProvider struct {
	installed bool
	used      int
}

func (p *capabilityRefreshProvider) Tools(_ context.Context, session tools.SessionContext) ([]toolexec.Tool, error) {
	result := []toolexec.Tool{{Name: "install_test_capability", Parameters: toolexec.SchemaFromValue(map[string]any{"type": "object"}), Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
		p.installed = true
		session.CapabilitiesChanged()
		return toolexec.OutputFromValue(map[string]any{"installed": true}), nil
	}}}
	if p.installed {
		result = append(result, toolexec.Tool{Name: "new_capability", Parameters: toolexec.SchemaFromValue(map[string]any{"type": "object"}), Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			p.used++
			return toolexec.OutputFromValue("worked"), nil
		}})
	}
	return result, nil
}

func TestCapabilityChangeRefreshesExecutableToolsInSameRun(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			capability := &capabilityRefreshProvider{}
			preflights, recoveries := 0, 0
			a := New(Deps{ContextViewApplier: func(ctx context.Context, cfg RunConfig) (RunConfig, error) {
				preflights++
				if cfg.RecoverContextBudget != nil {
					var err error
					cfg, _, err = cfg.RecoverContextBudget(ctx, cfg)
					return cfg, err
				}
				return cfg, nil
			}})
			a.SetToolProviders([]tools.ToolProvider{capability})
			calls := 0
			next := func(params sdk.Request) (sdk.ModelResult, error) {
				calls++
				switch calls {
				case 1:
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "install", ToolName: "install_test_capability", Input: toolexec.ArgumentsFromValue(map[string]any{})}}}, nil
				case 2:
					found := false
					for _, tool := range params.Tools {
						if tool.Name == "new_capability" {
							found = true
						}
					}
					if !found {
						t.Error("new tool missing from model definitions")
					}
					if _, ok := findToolResult(params.Messages, "install_test_capability"); !ok {
						t.Error("installation result was not carried forward")
					}
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "use", ToolName: "new_capability", Input: toolexec.ArgumentsFromValue(map[string]any{})}}}, nil
				default:
					return sdk.ModelResult{FinishReason: sdk.FinishReasonStop, Text: "done"}, nil
				}
			}
			cfg := RunConfig{SupportsToolCall: true, Messages: []sdk.Message{sdk.UserMessage("install and use")}}
			cfg.RecoverContextBudget = func(_ context.Context, cfg RunConfig) (RunConfig, bool, error) {
				recoveries++
				if recoveries > 1 {
					return cfg, false, ErrContextRecompose
				}
				return cfg, false, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var committed []int
			cfg.OnStepCommitted = func(_ context.Context, index int, _ *step.Record) (StepDirective, error) {
				committed = append(committed, index)
				return StepDirective{}, nil
			}
			if streaming {
				cfg.Model = &sdk.Model{ID: "test", Provider: agentStreamTestProvider(func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
					result, err := next(params)
					if err != nil {
						return nil, err
					}
					_ = result
					parts := []sdk.StreamPart{}
					for _, call := range result.ToolCalls {
						parts = append(parts, &sdk.StreamToolCallPart{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Input: toolexec.ArgumentsFromValue(call.Input)})
					}
					if result.Text != "" {
						parts = append(parts, &sdk.TextDeltaPart{Text: result.Text})
					}
					parts = append(parts, &sdk.FinishStepPart{FinishReason: result.FinishReason})
					return closedAgentTestStream(parts...), nil
				})}
				for event := range a.Stream(ctx, cfg) {
					if event.Type == EventError {
						t.Error(event.Error)
					}
				}
			} else {
				cfg.Model = &sdk.Model{ID: "test", Provider: &atomicMockProvider{handler: func(_ int, params sdk.Request) (sdk.ModelResult, error) { return next(params) }}}
				if _, err := a.Generate(ctx, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if capability.used != 1 || calls != 3 {
				t.Fatalf("used=%d calls=%d", capability.used, calls)
			}
			if preflights != 1 || recoveries != 1 {
				t.Fatalf("old history recovery crossed a completed tool: preflights=%d recoveries=%d", preflights, recoveries)
			}
			if fmt.Sprint(committed) != "[0 1 2]" {
				t.Fatalf("step commits=%v", committed)
			}
		})
	}
}

// A retryable provider failure after a capability refresh must rebuild the
// dispatch from the refreshed tool set: the retried request still offers the
// new tool and can execute it.
func TestCapabilityRefreshSurvivesMidStreamRetry(t *testing.T) {
	capability := &capabilityRefreshProvider{}
	a := New(Deps{})
	a.SetToolProviders([]tools.ToolProvider{capability})
	calls := 0
	provider := agentStreamTestProvider(func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
		calls++
		switch calls {
		case 1:
			return closedAgentTestStream(
				&sdk.StreamToolCallPart{ToolCallID: "install", ToolName: "install_test_capability", Input: toolexec.ArgumentsFromValue(map[string]any{})},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		case 2:
			return nil, errors.New("api error 429: engine overloaded")
		case 3:
			found := false
			for _, tool := range params.Tools {
				if tool.Name == "new_capability" {
					found = true
				}
			}
			if !found {
				t.Error("retried request lost the refreshed tool definitions")
			}
			return closedAgentTestStream(
				&sdk.StreamToolCallPart{ToolCallID: "use", ToolName: "new_capability", Input: toolexec.ArgumentsFromValue(map[string]any{})},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		default:
			return closedAgentTestStream(&sdk.TextDeltaPart{Text: "done"}, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
		}
	})
	cfg := RunConfig{
		SupportsToolCall: true,
		Messages:         []sdk.Message{sdk.UserMessage("install, survive a retry, then use")},
		Model:            &sdk.Model{ID: "test", Provider: provider},
		Retry:            RetryConfig{MaxAttempts: 3, FastAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// The loop publishes the failed attempt's error before it retries, so
	// only the terminal event decides the outcome.
	var terminal StreamEventType
	retried := false
	for event := range a.Stream(ctx, cfg) {
		switch event.Type {
		case EventRetry:
			retried = true
		case EventAgentEnd, EventAgentAbort:
			terminal = event.Type
		}
	}
	if !retried || terminal != EventAgentEnd {
		t.Fatalf("retried=%v terminal=%q, want a retried run that ends normally", retried, terminal)
	}
	if capability.used != 1 || calls != 4 {
		t.Fatalf("used=%d calls=%d, want the refreshed tool executed once after the retry", capability.used, calls)
	}
}

// capabilityUsageProvider is capabilityRefreshProvider plus tool-usage text
// that changes with the installed set, so a refresh must carry the new text
// into the request.
type capabilityUsageProvider struct{ capabilityRefreshProvider }

func (p *capabilityUsageProvider) Usage(context.Context, tools.SessionContext, tools.AvailableTools) string {
	if p.installed {
		return "USAGE-AFTER-INSTALL: prefer new_capability"
	}
	return "USAGE-BEFORE-INSTALL: install first"
}

// anthropicNamedProvider makes the prompt-cache plan promote the system prompt
// into the message prefix, the shape a capability refresh must rewrite.
type anthropicNamedProvider struct{ *atomicMockProvider }

func (anthropicNamedProvider) Name() string { return string(models.ClientTypeAnthropicMessages) }

// With Anthropic prompt caching the system prompt travels as the first
// message. A capability refresh must rewrite that message, or the model that
// is offered the new tool keeps reading the instructions of the old set.
func TestCapabilityRefreshRewritesPromotedSystemPrompt(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			capability := &capabilityUsageProvider{}
			a := New(Deps{})
			a.SetToolProviders([]tools.ToolProvider{capability})
			var systems []string
			calls := 0
			next := func(params sdk.Request) (sdk.ModelResult, error) {
				calls++
				if params.System != "" || len(params.Messages) == 0 || params.Messages[0].Role != sdk.MessageRoleSystem {
					t.Errorf("call %d: system not promoted into the prefix: system=%q first=%#v", calls, params.System, params.Messages[0])
				}
				if text, ok := params.Messages[0].Content[0].(sdk.TextPart); ok {
					systems = append(systems, text.Text)
					if text.CacheControl == nil {
						t.Errorf("call %d: promoted system lost its cache control", calls)
					}
				}
				switch calls {
				case 1:
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "install", ToolName: "install_test_capability", Input: toolexec.ArgumentsFromValue(map[string]any{})}}}, nil
				default:
					return sdk.ModelResult{FinishReason: sdk.FinishReasonStop, Text: "done"}, nil
				}
			}
			mock := &atomicMockProvider{handler: func(_ int, params sdk.Request) (sdk.ModelResult, error) { return next(params) }}
			mock.stream = func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
				result, err := next(params)
				if err != nil {
					return nil, err
				}
				parts := []sdk.StreamPart{}
				for _, call := range result.ToolCalls {
					parts = append(parts, &sdk.StreamToolCallPart{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Input: toolexec.ArgumentsFromValue(call.Input)})
				}
				if result.Text != "" {
					parts = append(parts, &sdk.TextDeltaPart{Text: result.Text})
				}
				parts = append(parts, &sdk.FinishStepPart{FinishReason: result.FinishReason})
				return closedAgentTestStream(parts...), nil
			}
			cfg := RunConfig{
				Model:            &sdk.Model{ID: "claude-test", Provider: anthropicNamedProvider{mock}},
				Messages:         []sdk.Message{sdk.UserMessage("install")},
				System:           "base system",
				PromptCacheTTL:   models.PromptCacheTTL5m,
				SupportsToolCall: true,
				Identity:         SessionContext{BotID: "bot-1", SessionID: "session-1"},
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if streaming {
				for event := range a.Stream(ctx, cfg) {
					if event.Type == EventError {
						t.Error(event.Error)
					}
				}
			} else if _, err := a.Generate(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if len(systems) != 2 {
				t.Fatalf("calls=%d systems=%q, want 2 calls", calls, systems)
			}
			if !strings.Contains(systems[0], "USAGE-BEFORE-INSTALL") || strings.Contains(systems[0], "USAGE-AFTER-INSTALL") {
				t.Fatalf("first request system = %q, want the pre-install usage text", systems[0])
			}
			if !strings.Contains(systems[1], "USAGE-AFTER-INSTALL") || strings.Contains(systems[1], "USAGE-BEFORE-INSTALL") {
				t.Fatalf("request after refresh system = %q, want the refreshed usage text in the promoted prefix", systems[1])
			}
			if !strings.HasPrefix(systems[1], "base system") {
				t.Fatalf("request after refresh lost the base system prompt: %q", systems[1])
			}
		})
	}
}

// capabilityReadProvider offers the read tool only after an install: the
// refreshed set must wrap it over the loop's read-media state, so the media
// it returns reaches the model as a message part instead of the internal
// envelope.
type capabilityReadProvider struct {
	installed bool
	readRuns  int
}

func (p *capabilityReadProvider) Tools(_ context.Context, session tools.SessionContext) ([]toolexec.Tool, error) {
	result := []toolexec.Tool{{Name: "install_reader", Parameters: toolexec.SchemaFromValue(map[string]any{"type": "object"}), Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
		p.installed = true
		session.CapabilitiesChanged()
		return toolexec.OutputFromValue(map[string]any{"installed": true}), nil
	}}}
	if p.installed {
		result = append(result, toolexec.Tool{Name: tools.ReadMediaToolName().String(), Parameters: toolexec.SchemaFromValue(map[string]any{"type": "object"}), Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			p.readRuns++
			return toolexec.OutputFromValue(tools.ReadMediaToolOutput{ImageBase64: "aW1hZ2U=", ImageMediaType: "image/png"}), nil
		}})
	}
	return result, nil
}

func TestCapabilityRefreshDecoratesNewReadTool(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			capability := &capabilityReadProvider{}
			a := New(Deps{})
			a.SetToolProviders([]tools.ToolProvider{capability})
			calls := 0
			var mediaDelivered, envelopeLeaked bool
			next := func(params sdk.Request) (sdk.ModelResult, error) {
				calls++
				switch calls {
				case 1:
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "install", ToolName: "install_reader", Input: toolexec.ArgumentsFromValue(map[string]any{})}}}, nil
				case 2:
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "read", ToolName: tools.ReadMediaToolName().String(), Input: toolexec.ArgumentsFromValue(map[string]any{"path": "a.png"})}}}, nil
				default:
					for _, msg := range params.Messages {
						for _, part := range msg.Content {
							if _, ok := part.(sdk.ImagePart); ok && msg.Role == sdk.MessageRoleUser {
								mediaDelivered = true
							}
							if result, ok := part.(sdk.ToolResultPart); ok && strings.Contains(result.Result.String(), "memoh_read_media") {
								envelopeLeaked = true
							}
						}
					}
					return sdk.ModelResult{FinishReason: sdk.FinishReasonStop, Text: "done"}, nil
				}
			}
			mock := &atomicMockProvider{handler: func(_ int, params sdk.Request) (sdk.ModelResult, error) { return next(params) }}
			mock.stream = func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
				result, err := next(params)
				if err != nil {
					return nil, err
				}
				parts := []sdk.StreamPart{}
				for _, call := range result.ToolCalls {
					parts = append(parts, &sdk.StreamToolCallPart{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Input: call.Input})
				}
				if result.Text != "" {
					parts = append(parts, &sdk.TextDeltaPart{Text: result.Text})
				}
				parts = append(parts, &sdk.FinishStepPart{FinishReason: result.FinishReason})
				return closedAgentTestStream(parts...), nil
			}
			cfg := RunConfig{
				Model:              &sdk.Model{ID: "test", Provider: mock},
				Messages:           []sdk.Message{sdk.UserMessage("install then read")},
				SupportsToolCall:   true,
				SupportsImageInput: true,
				Identity:           SessionContext{BotID: "bot-1"},
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if streaming {
				for event := range a.Stream(ctx, cfg) {
					if event.Type == EventError {
						t.Error(event.Error)
					}
				}
			} else if _, err := a.Generate(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if calls != 3 || capability.readRuns != 1 {
				t.Fatalf("calls=%d readRuns=%d", calls, capability.readRuns)
			}
			if envelopeLeaked {
				t.Fatal("the refreshed read tool returned the internal read-media envelope to the model")
			}
			if !mediaDelivered {
				t.Fatal("the image read by the refreshed read tool did not reach the model as a message part")
			}
		})
	}
}
