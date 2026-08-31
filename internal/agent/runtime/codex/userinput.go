// Bridges codex `tool/requestUserInput` to Memoh's ask_user decision flow:
// codex questions render as ask_user cards, the answers map back to the
// question ids codex asked with, and a canceled or timed-out card degrades to
// the empty answer set codex treats as "proceed with defaults".
package codex

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
)

// UserInputService is the ask_user decision flow the driver routes
// tool/requestUserInput through.
type UserInputService interface {
	userinput.FlowService
}

// requestUserInput answers one tool/requestUserInput server request. It never
// fails the turn: any flow error or non-submitted outcome yields the empty
// answer set.
func (t *turnState) requestUserInput(ctx context.Context, params *protocol.ToolRequestUserInputParams) protocol.ToolRequestUserInputResponse {
	answers := map[string]protocol.ToolRequestUserInputAnswer{}
	if t.userInput == nil || len(params.Questions) == 0 {
		return protocol.ToolRequestUserInputResponse{Answers: answers}
	}
	// ask_user renders a bounded number of questions per card; longer
	// requests run as sequential cards and stop at the first card the user
	// does not submit.
	for start := 0; start < len(params.Questions); start += userinput.MaxQuestionsPerRequest {
		end := min(start+userinput.MaxQuestionsPerRequest, len(params.Questions))
		chunkAnswers, submitted := t.runUserInputCard(ctx, params, start, params.Questions[start:end])
		for id, answer := range chunkAnswers {
			answers[id] = answer
		}
		if !submitted {
			break
		}
	}
	return protocol.ToolRequestUserInputResponse{Answers: answers}
}

func (t *turnState) runUserInputCard(ctx context.Context, params *protocol.ToolRequestUserInputParams, offset int, questions []protocol.ToolRequestUserInputQuestion) (map[string]protocol.ToolRequestUserInputAnswer, bool) {
	payloadQuestions := make([]any, 0, len(questions))
	for _, question := range questions {
		payloadQuestions = append(payloadQuestions, codexQuestionToAskUser(question))
	}
	toolCallID := params.ItemID
	if offset > 0 {
		toolCallID = fmt.Sprintf("%s:%d", params.ItemID, offset)
	}
	flow, err := userinput.RunFlow(ctx, t.userInput, userinput.FlowRequest{
		Input: userinput.CreatePendingInput{
			BotID:                        t.input.BotID,
			SessionID:                    t.input.ThreadID,
			RouteID:                      t.input.RouteID,
			ChannelIdentityID:            t.input.ChannelIdentityID,
			RequestedByChannelIdentityID: t.input.ChannelIdentityID,
			ToolCallID:                   toolCallID,
			ToolName:                     userinput.ToolNameAskUser,
			Input:                        map[string]any{"questions": payloadQuestions},
			ProviderMetadata: map[string]any{
				"source":    userinput.ProviderSourceCodexUserInput,
				"thread_id": params.ThreadID,
				"turn_id":   params.TurnID,
				"item_id":   params.ItemID,
			},
			SourcePlatform: "",
		},
		ActorChannelIdentityID: t.input.ChannelIdentityID,
		Interactive:            t.input.CanRequestUserInput,
		WaitTimeout:            userinput.DefaultWaitTimeout,
		Emit:                   t.emitUserInputRequest,
		NonInteractiveReason:   "codex requested user input without an interactive stream",
		UndeliveredReason:      "codex user input request was not delivered to the interactive stream",
		TimeoutReason:          "codex user input timed out",
		AbortReason:            "codex user input aborted",
	})
	if err != nil {
		if ctx.Err() == nil {
			t.logger.Error("codex user input flow failed", slog.String("thread_id", t.threadID), slog.Any("error", err))
		}
		return nil, false
	}
	if flow.Request.Status != userinput.StatusSubmitted {
		return nil, false
	}

	byQuestion := map[string]userinput.UIAnswer{}
	for _, answer := range userinput.AnswersFromResult(flow.Request.Result) {
		byQuestion[answer.QuestionID] = answer
	}
	out := make(map[string]protocol.ToolRequestUserInputAnswer, len(questions))
	for idx, question := range questions {
		// ask_user generates ids positionally within the card.
		answer, ok := byQuestion[fmt.Sprintf("q%d", idx+1)]
		if !ok || answer.Skipped {
			continue
		}
		values := make([]string, 0, len(answer.Selected)+1)
		for _, selected := range answer.Selected {
			values = append(values, selected.Label)
		}
		if custom := strings.TrimSpace(answer.CustomText); custom != "" {
			values = append(values, custom)
		}
		if text := strings.TrimSpace(answer.Text); text != "" {
			values = append(values, text)
		}
		if len(values) == 0 {
			continue
		}
		out[question.ID] = protocol.ToolRequestUserInputAnswer{Answers: values}
	}
	return out, true
}

// codexQuestionToAskUser maps one codex question onto the strict ask_user
// question schema. Questions with fewer than the minimum selectable options
// degrade to free text, and `isOther` becomes the custom-answer affordance.
func codexQuestionToAskUser(question protocol.ToolRequestUserInputQuestion) map[string]any {
	text := strings.TrimSpace(question.Question)
	if header := strings.TrimSpace(question.Header); header != "" && !strings.EqualFold(header, text) {
		text = header + " — " + text
	}
	out := map[string]any{"text": text}
	if len(question.Options) >= userinput.MinOptionsPerQuestion {
		options := question.Options
		allowCustom := question.IsOther != nil && *question.IsOther
		if len(options) > userinput.MaxOptionsPerQuestion {
			// Beyond the render limit the tail options stay reachable as a
			// typed custom answer.
			options = options[:userinput.MaxOptionsPerQuestion]
			allowCustom = true
		}
		payloadOptions := make([]any, 0, len(options))
		for _, option := range options {
			entry := map[string]any{"label": option.Label}
			if description := strings.TrimSpace(option.Description); description != "" {
				entry["description"] = description
			}
			payloadOptions = append(payloadOptions, entry)
		}
		out["kind"] = userinput.QuestionKindSingleSelect
		out["options"] = payloadOptions
		if allowCustom {
			out["allow_custom"] = true
		}
		return out
	}
	out["kind"] = userinput.QuestionKindText
	if len(question.Options) == 1 {
		out["placeholder"] = strings.TrimSpace(question.Options[0].Label)
	}
	return out
}

func (t *turnState) emitUserInputRequest(req userinput.Request) bool {
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = userinput.StatusPending
	}
	t.emit(event.StreamEvent{
		Type:        event.UserInputRequest,
		ToolCallID:  req.ToolCallID,
		ToolName:    req.ToolName,
		Input:       req.Input,
		UserInputID: req.ID,
		ShortID:     req.ShortID,
		Status:      status,
		Metadata:    userinput.DeferredMetadata(req),
	})
	return true
}
