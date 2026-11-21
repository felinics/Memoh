package client

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/toolcontext"
)

// UserQuestionCodec maps the request an agent sends for its own question
// tool, outside ACP elicitation, onto Memoh's question card and back.
type UserQuestionCodec struct {
	// Method is the agent's request method, sessionId among its params.
	Method string
	Decode func(params json.RawMessage) (toolCallID string, questions []userinput.ChoiceQuestion, err error)
	// Encode is the agent's response. answers follows questions; nil means
	// the user gave none.
	Encode func(questions []userinput.ChoiceQuestion, answers []string) any
}

func (c *clientCallbacks) AskUserQuestions(ctx context.Context, params json.RawMessage) (any, error) {
	codec := c.userQuestions
	c.decisions.enter()
	defer c.decisions.exit()
	toolCallID, questions, err := codec.Decode(params)
	if err != nil || len(questions) == 0 || len(questions) > userinput.MaxQuestionsPerRequest {
		return nil, acp.NewInvalidParams(map[string]any{"error": "invalid user questions"})
	}
	session := c.currentToolSession()
	if c.userInput == nil || !session.CanRequestUserInput || strings.TrimSpace(session.BotID) == "" || strings.TrimSpace(session.SessionID) == "" {
		return codec.Encode(questions, nil), nil
	}
	if strings.TrimSpace(toolCallID) == "" {
		toolCallID = "acp-question-" + uuid.NewString()
	}
	expiresAt := time.Now().Add(userinput.DefaultWaitTimeout + time.Minute)
	ctx, cancel := toolcontext.Bind(ctx, session)
	defer cancel()
	flow, err := userinput.RunFlow(ctx, c.userInput, userinput.FlowRequest{
		Input: userinput.CreatePendingInput{
			BotID:                        session.BotID,
			SessionID:                    session.SessionID,
			RouteID:                      session.RouteID,
			ChannelIdentityID:            session.ChannelIdentityID,
			RequestedByChannelIdentityID: session.ChannelIdentityID,
			ToolCallID:                   toolCallID,
			ToolName:                     userinput.ToolNameAskUser,
			Input:                        userinput.ChoiceQuestionsInput(questions),
			ProviderMetadata: map[string]any{
				"source":     userinput.ProviderSourceACPAgentQuestion,
				"runtime_id": session.RuntimeID,
				"run_id":     session.RunID,
			},
			SourcePlatform:   session.CurrentPlatform,
			ReplyTarget:      session.ReplyTarget,
			ConversationType: session.ConversationType,
			ExpiresAt:        &expiresAt,
		},
		ActorChannelIdentityID: session.ChannelIdentityID,
		Interactive:            true,
		WaitTimeout:            userinput.DefaultWaitTimeout,
		UndeliveredReason:      "agent question could not be delivered to the interactive stream",
		TimeoutReason:          "agent question timed out",
		AbortReason:            "agent question aborted",
		Emit:                   c.emitElicitationUserInput,
	})
	if err != nil {
		return nil, err
	}
	if flow.Request.Status != userinput.StatusSubmitted {
		return codec.Encode(questions, nil), nil
	}
	answers, ok := userinput.ChoiceAnswers(flow.Request.Result, len(questions))
	if !ok {
		return codec.Encode(questions, nil), nil
	}
	return codec.Encode(questions, answers), nil
}
