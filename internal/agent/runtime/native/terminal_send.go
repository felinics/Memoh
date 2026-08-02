package native

import (
	"context"
	"strings"
	"sync/atomic"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/sessionmode"
	tools "github.com/memohai/memoh/internal/agent/tool"
	"github.com/memohai/memoh/internal/delivery"
)

const terminalSendResponseID = "memoh-terminal-send"

// terminalSendState lets a successful current-conversation send end a
// multi-step tool loop without making another provider request. The SDK still
// records the assistant tool call and its result before asking the wrapped
// provider for the next step; the provider then returns a local stop result.
type terminalSendState struct {
	delivered atomic.Bool
}

func (s *terminalSendState) markDelivered() {
	if s != nil {
		s.delivered.Store(true)
	}
}

func (s *terminalSendState) isDelivered() bool {
	return s != nil && s.delivered.Load()
}

func installTerminalSend(cfg RunConfig, sdkTools []sdk.Tool) ([]sdk.Tool, *sdk.Model) {
	if cfg.SessionType != sessionmode.Discuss || cfg.Model == nil || cfg.Model.Provider == nil {
		return sdkTools, cfg.Model
	}

	state := &terminalSendState{}
	wrapped := append([]sdk.Tool(nil), sdkTools...)
	found := false
	for i := range wrapped {
		if strings.TrimSpace(wrapped[i].Name) != tools.ToolSend().String() || wrapped[i].Execute == nil {
			continue
		}
		found = true
		execute := wrapped[i].Execute
		wrapped[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			output, err := execute(ctx, input)
			if err == nil && isSuccessfulCurrentSendResult(output, cfg) {
				state.markDelivered()
			}
			return output, err
		}
	}
	if !found {
		return sdkTools, cfg.Model
	}

	model := *cfg.Model
	model.Provider = &terminalSendProvider{Provider: cfg.Model.Provider, state: state}
	return wrapped, &model
}

func isSuccessfulCurrentSendResult(output any, cfg RunConfig) bool {
	result, ok := output.(map[string]any)
	if !ok {
		return false
	}
	return delivery.IsSuccessfulCurrentDelivery(
		result, cfg.Identity.CurrentPlatform, cfg.Identity.ReplyTarget,
	)
}

type terminalSendProvider struct {
	sdk.Provider
	state *terminalSendState
}

func (p *terminalSendProvider) DoGenerate(ctx context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	if p.state.isDelivered() {
		return &sdk.GenerateResult{
			FinishReason: sdk.FinishReasonStop,
			Response:     sdk.ResponseMetadata{ID: terminalSendResponseID},
		}, nil
	}
	return p.Provider.DoGenerate(ctx, params)
}

func (p *terminalSendProvider) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	if !p.state.isDelivered() {
		return p.Provider.DoStream(ctx, params)
	}
	parts := make(chan sdk.StreamPart, 2)
	parts <- &sdk.FinishStepPart{
		FinishReason: sdk.FinishReasonStop,
		Response:     sdk.ResponseMetadata{ID: terminalSendResponseID},
	}
	parts <- &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}
	close(parts)
	return &sdk.StreamResult{Stream: parts}, nil
}

func isTerminalSendStep(step *sdk.StepResult) bool {
	return step != nil && step.Response.ID == terminalSendResponseID
}
