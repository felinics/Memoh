package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	"github.com/memohai/memoh/internal/bots"
	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/workspace"
)

// userInputService is the slice of *userinput.Service the application depends
// on, kept as an interface so the respond/resume routing can be tested with
// fakes.
type userInputService interface {
	CreatePending(ctx context.Context, input userinput.CreatePendingInput) (userinput.Request, error)
	ResolveTarget(ctx context.Context, input userinput.ResolveInput) (userinput.Request, error)
	AdvanceText(ctx context.Context, input userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error)
	Submit(ctx context.Context, input userinput.SubmitInput) (userinput.Request, error)
	Cancel(ctx context.Context, input userinput.CancelInput) (userinput.Request, error)
	CanRespond(req userinput.Request) bool
}

func (s *Service) AdvancePlainTextUserInput(ctx context.Context, input userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	if s.userInput == nil {
		return userinput.AdvanceTextResult{}, errors.New("user input service not configured")
	}
	return s.userInput.AdvanceText(ctx, input)
}

type UserInputResponseInput struct {
	ControlID                  string
	BotID                      string
	ThreadID                   string
	ActorChannelIdentityID     string
	ActorUserID                string
	UserInputID                string
	ExplicitID                 string
	ReplyExternalMessageID     string
	Answers                    []userinput.QuestionAnswer
	TextAnswer                 string
	Canceled                   bool
	Reason                     string
	ChatToken                  string
	SuppressActivePromptAttach bool
}

func (s *Service) respondUserInput(ctx context.Context, input UserInputResponseInput, eventCh chan<- WSStreamEvent) error {
	committed, err := s.CommitUserInputResponse(ctx, input)
	if err != nil {
		return err
	}
	return s.ContinueCommittedUserInputResponse(ctx, committed, eventCh)
}

// CommittedUserInputResponse is the durable half of an ask_user response.
// Keeping it separate from the continuation lets the runtime acknowledge the
// user's click as soon as the decision commits, without imposing the command
// acknowledgement deadline on the following model call.
type CommittedUserInputResponse struct {
	request      userinput.Request
	input        UserInputResponseInput
	activePrompt *acpActivePromptSubscription
	ackOnly      bool
}

func (s *Service) CommitUserInputResponse(ctx context.Context, input UserInputResponseInput) (CommittedUserInputResponse, error) {
	if s.userInput == nil {
		return CommittedUserInputResponse{}, errors.New("user input service not configured")
	}
	target, err := s.userInput.ResolveTarget(ctx, userinput.ResolveInput{
		BotID:                  input.BotID,
		SessionID:              input.ThreadID,
		ExplicitID:             firstNonEmpty(input.ExplicitID, input.UserInputID),
		ReplyExternalMessageID: input.ReplyExternalMessageID,
	})
	if err != nil {
		return CommittedUserInputResponse{}, err
	}

	isACPMCP := userinput.IsACPMCPRequest(target)
	if isACPMCP {
		if err := s.authorizeACPUserInputResponse(ctx, target, input); err != nil {
			return CommittedUserInputResponse{}, err
		}
	}
	if !isACPMCP {
		ctx = workspace.WithWorkspaceTarget(ctx, target.WorkspaceTargetID)
	}
	if isACPMCP && !s.userInput.CanRespond(target) {
		if _, err := s.userInput.Cancel(ctx, userinput.CancelInput{
			RequestID:              target.ID,
			ActorChannelIdentityID: input.ActorChannelIdentityID,
			Reason:                 "user input expired: the requesting tool call is no longer waiting",
		}); err != nil && !errors.Is(err, userinput.ErrAlreadyDecided) {
			return CommittedUserInputResponse{}, err
		}
		return CommittedUserInputResponse{request: target, input: input, ackOnly: true}, nil
	}
	var activePrompt *acpActivePromptSubscription
	if isACPMCP && !input.SuppressActivePromptAttach {
		activePrompt, _ = s.subscribeACPActivePrompt(
			firstNonEmpty(target.BotID, input.BotID),
			firstNonEmpty(target.SessionID, input.ThreadID),
		)
	}

	var resolved userinput.Request
	if input.Canceled {
		resolved, err = s.userInput.Cancel(ctx, userinput.CancelInput{
			RequestID:              target.ID,
			ActorChannelIdentityID: input.ActorChannelIdentityID,
			Reason:                 input.Reason,
		})
	} else {
		answers := input.Answers
		if len(answers) == 0 && strings.TrimSpace(input.TextAnswer) != "" {
			answers, err = userInputAnswersFromText(target.UIPayload, input.TextAnswer)
			if err != nil {
				if activePrompt != nil {
					activePrompt.release()
				}
				return CommittedUserInputResponse{}, err
			}
		}
		resolved, err = s.userInput.Submit(ctx, userinput.SubmitInput{
			RequestID:              target.ID,
			ActorChannelIdentityID: input.ActorChannelIdentityID,
			Answers:                answers,
		})
	}
	if err != nil {
		if activePrompt != nil {
			activePrompt.release()
		}
		if isACPMCP && errors.Is(err, userinput.ErrAlreadyDecided) {
			return CommittedUserInputResponse{request: target, input: input, ackOnly: true}, nil
		}
		return CommittedUserInputResponse{}, err
	}
	return CommittedUserInputResponse{
		request:      resolved,
		input:        input,
		activePrompt: activePrompt,
	}, nil
}

func (s *Service) ContinueCommittedUserInputResponse(ctx context.Context, committed CommittedUserInputResponse, eventCh chan<- WSStreamEvent) error {
	resolved := committed.request
	if strings.TrimSpace(resolved.ID) == "" {
		return errors.New("committed user input response is missing its request")
	}
	if committed.ackOnly {
		return emitApprovalAck(ctx, eventCh)
	}
	if userinput.IsACPMCPRequest(resolved) {
		// An ACP/MCP waiter is blocked on this request and resumes the run
		// itself. When this response stream has reattached to the active ACP
		// prompt, forward that live continuation so refreshes observe the same
		// loading/progress shape as native deferred requests.
		if committed.activePrompt != nil {
			return forwardACPActivePrompt(ctx, committed.activePrompt, eventCh, acpActivePromptForwardOptions{
				SkipToolCallID:  resolved.ToolCallID,
				SkipUserInputID: resolved.ID,
			})
		}
		return emitApprovalAck(ctx, eventCh)
	}

	toolResult := sdk.ToolResultPart{
		ToolCallID: resolved.ToolCallID,
		ToolName:   resolved.ToolName,
		Result:     s.limitToolResultValue(resolved.Result, resolved.ToolName),
		IsError:    false,
	}
	continueFn := s.continueUserInputFn
	if continueFn == nil {
		continueFn = s.storeUserInputResultAndContinue
	}
	return continueFn(ctx, resolved, committed.input, toolResult, eventCh)
}

func (s *Service) authorizeACPUserInputResponse(ctx context.Context, target userinput.Request, input UserInputResponseInput) error {
	if s == nil || s.sessionService == nil {
		return errors.New("session service not configured")
	}
	if s.botPermissions == nil {
		return errors.New("bot permission checker not configured")
	}
	sessionID := firstNonEmpty(target.SessionID, input.ThreadID)
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if !sessionpkg.IsACPRuntime(sess) {
		return nil
	}
	botID := firstNonEmpty(target.BotID, input.BotID)
	if strings.TrimSpace(sess.BotID) != "" && strings.TrimSpace(botID) != "" && sess.BotID != botID {
		return userinput.ErrForbidden
	}
	if strings.TrimSpace(botID) == "" {
		botID = sess.BotID
	}
	actorID := firstNonEmpty(input.ActorUserID, input.ActorChannelIdentityID)
	if actorID == "" {
		return userinput.ErrForbidden
	}
	acpMeta := mergeACPRuntimeMetadata(sess.Metadata, sess.RuntimeMetadata)
	runtimeOwnerID := metadataString(acpMeta, "runtime_owner_account_id")
	if runtimeOwnerID == "" {
		return userinput.ErrForbidden
	}
	if ok, err := s.botPermissions.HasBotPermission(ctx, botID, runtimeOwnerID, bots.PermissionWorkspaceExec); err != nil {
		return err
	} else if !ok {
		return userinput.ErrForbidden
	}
	if actorID != runtimeOwnerID {
		return userinput.ErrForbidden
	}
	return nil
}

func userInputAnswersFromText(payload userinput.UIPayload, text string) ([]userinput.QuestionAnswer, error) {
	answerText := strings.TrimSpace(text)
	if answerText == "" {
		return nil, errors.New("user input answer is required")
	}
	if len(payload.Questions) != 1 {
		return nil, errors.New("text response command can answer exactly one user input question")
	}
	question := payload.Questions[0]
	answer := userinput.QuestionAnswer{QuestionID: question.ID}
	switch question.Kind {
	case userinput.QuestionKindText:
		answer.Text = answerText
	case userinput.QuestionKindSingleSelect:
		optionID, ok := matchUserInputOption(question, answerText)
		switch {
		case ok:
			answer.OptionIDs = []string{optionID}
		case question.AllowCustom:
			answer.CustomText = answerText
		default:
			return nil, fmt.Errorf("answer %q does not match an option for question %q", answerText, question.ID)
		}
	case userinput.QuestionKindMultiSelect:
		parts := splitUserInputAnswerText(answerText)
		optionIDs := make([]string, 0, len(parts))
		custom := ""
		for _, part := range parts {
			if optionID, ok := matchUserInputOption(question, part); ok {
				optionIDs = append(optionIDs, optionID)
				continue
			}
			if question.AllowCustom && custom == "" {
				custom = part
				continue
			}
			return nil, fmt.Errorf("answer %q does not match an option for question %q", part, question.ID)
		}
		answer.OptionIDs = optionIDs
		answer.CustomText = custom
	default:
		return nil, fmt.Errorf("question %q has unsupported kind %q", question.ID, question.Kind)
	}
	return []userinput.QuestionAnswer{answer}, nil
}

func matchUserInputOption(question userinput.UIQuestion, text string) (string, bool) {
	target := strings.TrimSpace(text)
	for _, option := range question.Options {
		if strings.EqualFold(strings.TrimSpace(option.ID), target) || strings.EqualFold(strings.TrimSpace(option.Label), target) {
			return option.ID, true
		}
	}
	return "", false
}

func splitUserInputAnswerText(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；'
	})
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if part := strings.TrimSpace(field); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 && strings.TrimSpace(text) != "" {
		parts = append(parts, strings.TrimSpace(text))
	}
	return parts
}

func (s *Service) storeUserInputResultAndContinue(ctx context.Context, req userinput.Request, input UserInputResponseInput, result sdk.ToolResultPart, eventCh chan<- WSStreamEvent) error {
	req = withLocalWebUserInputReplyTarget(req)
	ctx = workspace.WithWorkspaceTarget(ctx, req.WorkspaceTargetID)
	target, err := s.resolveWorkspaceTargetSnapshot(ctx, input.BotID, req.WorkspaceTargetID)
	if err != nil {
		return err
	}
	modelMessages := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(result)})
	storeReq := ChatRequest{
		BotID:                   input.BotID,
		ChatID:                  input.BotID,
		ThreadID:                req.SessionID,
		SourceChannelIdentityID: firstNonEmpty(req.ChannelIdentityID, input.ActorChannelIdentityID),
		CurrentChannel:          req.SourcePlatform,
		ReplyTarget:             req.ReplyTarget,
		ConversationType:        req.ConversationType,
		UserMessagePersisted:    true,
		WorkspaceTargetID:       req.WorkspaceTargetID,
		WorkspaceTarget:         target,
	}
	if err := s.storeRoundWithOptions(ctx, storeReq, modelMessages, "", storeRoundOptions{AllowPendingToolCalls: true}); err != nil {
		return err
	}
	return s.continueUserInputSession(ctx, req, input, eventCh)
}

func (s *Service) continueUserInputSession(ctx context.Context, req userinput.Request, input UserInputResponseInput, eventCh chan<- WSStreamEvent) error {
	req = withLocalWebUserInputReplyTarget(req)
	ctx = workspace.WithWorkspaceTarget(ctx, req.WorkspaceTargetID)
	resolved, err := s.ResolveRunConfig(ctx,
		input.BotID,
		req.SessionID,
		firstNonEmpty(req.ChannelIdentityID, input.ActorChannelIdentityID),
		req.SourcePlatform,
		req.ReplyTarget,
		req.ConversationType,
		input.ChatToken,
	)
	if err != nil {
		return err
	}

	cfg, err := s.prepareContinuationRunConfig(
		ctx,
		resolved.RunConfig,
		historyScopeFallbackFromUserInputRequest(req),
		compactionSummaryScope(firstNonEmpty(req.BotID, input.BotID), "", req.SessionID, req.ConversationType, "", req.ReplyTarget),
		eventCh,
	)
	if err != nil {
		return err
	}

	chatReq := ChatRequest{
		BotID:                   input.BotID,
		ChatID:                  input.BotID,
		ThreadID:                req.SessionID,
		SourceChannelIdentityID: firstNonEmpty(req.ChannelIdentityID, input.ActorChannelIdentityID),
		CurrentChannel:          req.SourcePlatform,
		ReplyTarget:             req.ReplyTarget,
		ConversationType:        req.ConversationType,
		UserMessagePersisted:    true,
		WorkspaceTargetID:       req.WorkspaceTargetID,
		WorkspaceTarget:         workspaceTargetFromRunConfig(resolved.RunConfig),
	}

	stream := s.agent.Stream(ctx, cfg)
	stored := false
	for event := range stream {
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if !stored && event.IsTerminal() && len(event.Messages) > 0 {
			if snap, ok := extractTerminalSnapshot(data); ok {
				if storeErr := s.persistTerminalSnapshot(
					context.WithoutCancel(ctx),
					chatReq,
					resolvedContext{model: models.GetResponse{ID: resolved.ModelID}},
					snap,
				); storeErr != nil {
					return storeErr
				}
				stored = true
			}
		}
		if eventCh != nil {
			select {
			case eventCh <- json.RawMessage(data):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

func withLocalWebUserInputReplyTarget(req userinput.Request) userinput.Request {
	if strings.EqualFold(strings.TrimSpace(req.SourcePlatform), "web") && strings.TrimSpace(req.ReplyTarget) == "" {
		req.ReplyTarget = strings.TrimSpace(req.BotID)
	}
	return req
}
