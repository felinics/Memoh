package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
	"github.com/memohai/memoh/domains/agent/decision"
)

const (
	submitInstruction = "The user submitted this answer for the current ask_user request. Use it only to resolve that specific question. If the user later asks for another choice, quiz, or decision, call ask_user again before grading or continuing."
	cancelInstruction = "The user canceled this input request. Do not ask the same question again; continue with a reasonable choice from the available context or briefly explain the next step."
)

type Service struct {
	persistence Persistence

	logger *slog.Logger

	waiter *decision.Waiter[Request]
}

func NewService(log *slog.Logger, persistence Persistence) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		persistence: persistence,
		logger:      log.With(slog.String("service", "userinput")),
		waiter:      decision.NewWaiter[Request](),
	}
}

// RegisterWaiter records that a caller in this process owns the request's
// resolution. Callers that announce a pending request to users must register
// BEFORE announcing, or an instant response can be misjudged as orphaned.
// The returned release must run when the wait ends.
func (s *Service) RegisterWaiter(requestID string) func() {
	if s == nil || s.waiter == nil {
		return func() {}
	}
	return s.waiter.Register(requestID)
}

// HasWaiter reports whether anyone in this process is currently registered
// for the request. It is only a local fast-path signal; DB status remains the
// cross-process source of truth for whether a request can accept a response.
func (s *Service) HasWaiter(requestID string) bool {
	return s != nil && s.waiter != nil && s.waiter.Has(requestID)
}

// CanRespond reports whether the UI should offer a response action for this
// request in the current server process. ACP/MCP requests are consumed by an
// in-process waiter, so a pending DB row alone is not enough.
func (s *Service) CanRespond(req Request) bool {
	if req.Status != StatusPending {
		return false
	}
	if IsACPMCPRequest(req) {
		return s.HasWaiter(req.ID)
	}
	return true
}

func (s *Service) notifyResolved(req Request) {
	if s == nil || s.waiter == nil {
		return
	}
	s.waiter.Notify(req.ID, req)
}

// resolveAndNotify converts a terminal-transition row, then wakes any waiters.
// Shared by Submit, Cancel, and Fail so notification can never drift between
// resolution paths. A guarded update that matched no row is disambiguated:
// an existing non-pending request means the transition lost a race to another
// decision (or to expiry), not that the request is unknown.
func (s *Service) resolveAndNotify(ctx context.Context, requestID string, row Record, err error) (Request, error) {
	resolved, err := requestFromRowOrErr(row, err)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if validateID(requestID) == nil {
				if existing, getErr := s.persistence.Get(ctx, requestID); getErr == nil &&
					(existing.Status != StatusPending || existing.RuntimeFencingToken != nil ||
						(existing.ExpiresAt != nil && !existing.ExpiresAt.After(time.Now()))) {
					return Request{}, ErrAlreadyDecided
				}
			}
		}
		return Request{}, err
	}
	s.notifyResolved(resolved)
	return resolved, nil
}

func (s *Service) CreatePending(ctx context.Context, input CreatePendingInput) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("user input persistence not configured")
	}
	botID, err := canonicalID(input.BotID)
	if err != nil {
		return Request{}, err
	}
	sessionID, err := canonicalID(input.SessionID)
	if err != nil {
		return Request{}, err
	}
	toolCallID := strings.TrimSpace(input.ToolCallID)
	if toolCallID == "" {
		return Request{}, errors.New("tool_call_id is required")
	}
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		toolName = ToolNameAskUser
	}
	if toolName != ToolNameAskUser {
		return Request{}, fmt.Errorf("unsupported user input tool %q", toolName)
	}
	uiPayload, err := ParseAskUserPayload(input.Input)
	if err != nil {
		return Request{}, err
	}
	rawInput, err := marshalObject(input.Input)
	if err != nil {
		return Request{}, err
	}
	uiPayloadJSON, err := json.Marshal(uiPayload)
	if err != nil {
		return Request{}, err
	}
	providerMetadata, err := marshalObject(input.ProviderMetadata)
	if err != nil {
		return Request{}, err
	}
	channelIdentityID, err := s.optionalChannelIdentityID(ctx, input.ChannelIdentityID)
	if err != nil {
		return Request{}, err
	}
	requestedByID, err := s.optionalChannelIdentityID(ctx, input.RequestedByChannelIdentityID)
	if err != nil {
		return Request{}, err
	}
	params := CreateRecordInput{
		BotID:                        botID,
		SessionID:                    sessionID,
		RouteID:                      optionalID(input.RouteID),
		ChannelIdentityID:            channelIdentityID,
		WorkspaceTargetID:            strings.TrimSpace(input.WorkspaceTargetID),
		ToolCallID:                   toolCallID,
		ToolName:                     toolName,
		RuntimeFencingToken:          runtimeFencingToken(ctx),
		InputJSON:                    rawInput,
		UIPayloadJSON:                uiPayloadJSON,
		ProviderMetadata:             providerMetadata,
		RequestedByChannelIdentityID: requestedByID,
		SourcePlatform:               strings.TrimSpace(input.SourcePlatform),
		ReplyTarget:                  strings.TrimSpace(input.ReplyTarget),
		ConversationType:             strings.TrimSpace(input.ConversationType),
		ExpiresAt:                    optionalTime(input.ExpiresAt),
	}
	var row Record
	err = s.persistence.InInputCreateTransaction(ctx, input.BotID, input.SessionID, func(store Store) error {
		var createErr error
		row, createErr = store.Create(ctx, params)
		return createErr
	})
	if err != nil {
		if errors.Is(mapLookupErr(err), ErrNotFound) {
			_, getErr := s.persistence.GetBySessionToolCall(ctx, input.SessionID, toolCallID)
			if getErr == nil {
				return Request{}, ErrAlreadyDecided
			}
		}
		return Request{}, mapLookupErr(err)
	}
	return requestFromRow(row), nil
}

func (s *Service) ResolveTarget(ctx context.Context, input ResolveInput) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("user input persistence not configured")
	}
	botID, err := canonicalID(input.BotID)
	if err != nil {
		return Request{}, err
	}
	explicit := strings.TrimSpace(input.ExplicitID)
	if strings.TrimSpace(input.SessionID) == "" && explicit != "" {
		if validateID(explicit) == nil {
			row, err := s.persistence.GetRespondable(ctx, ResolveRecordInput{
				ID:                  explicit,
				RuntimeFencingToken: runtimeFencingToken(ctx),
			})
			if err != nil {
				return Request{}, mapLookupErr(err)
			}
			req := requestFromRespondableRow(row)
			if req.BotID != botID {
				return Request{}, ErrNotFound
			}
			return req, nil
		}
		return Request{}, ErrNotFound
	}
	sessionID, err := canonicalID(input.SessionID)
	if err != nil {
		return Request{}, err
	}
	if explicit != "" {
		if shortID, err := strconv.Atoi(explicit); err == nil {
			row, err := s.persistence.GetPendingBySessionShortID(ctx, botID, sessionID, shortID)
			return requestFromRowOrErr(row, err)
		}
		if validateID(explicit) == nil {
			row, err := s.persistence.GetRespondable(ctx, ResolveRecordInput{
				ID:                  explicit,
				RuntimeFencingToken: runtimeFencingToken(ctx),
			})
			if err != nil {
				return Request{}, mapLookupErr(err)
			}
			req := requestFromRespondableRow(row)
			if req.BotID != botID || req.SessionID != sessionID {
				return Request{}, ErrNotFound
			}
			return req, nil
		}
		return Request{}, ErrNotFound
	}
	if replyID := strings.TrimSpace(input.ReplyExternalMessageID); replyID != "" {
		row, err := s.persistence.GetPendingByReplyMessage(ctx, botID, sessionID, replyID)
		if err == nil {
			return requestFromRow(row), nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Request{}, err
		}
	}
	row, err := s.persistence.GetLatestPendingBySession(ctx, botID, sessionID)
	return requestFromRowOrErr(row, err)
}

func (s *Service) Get(ctx context.Context, requestID string) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("user input persistence not configured")
	}
	if err := validateID(requestID); err != nil {
		return Request{}, err
	}
	row, err := s.persistence.Get(ctx, requestID)
	return requestFromRowOrErr(row, err)
}

// WaitForResponse blocks until the request leaves pending. Resolution inside
// this process arrives via the Submit/Cancel/Fail broadcast; the slow ticker
// is only a safety net for transitions this process cannot observe (another
// node, manual DB changes, time-based expiry).
func (s *Service) WaitForResponse(ctx context.Context, requestID string) (Request, error) {
	release := s.RegisterWaiter(requestID)
	defer release()
	return s.waitForResponse(ctx, requestID)
}

// WaitForRegisteredResponse waits like WaitForResponse but assumes the caller
// already registered with RegisterWaiter before announcing the request.
func (s *Service) WaitForRegisteredResponse(ctx context.Context, requestID string) (Request, error) {
	return s.waitForResponse(ctx, requestID)
}

func (s *Service) waitForResponse(ctx context.Context, requestID string) (Request, error) {
	poll := func(ctx context.Context) (Request, bool, error) {
		req, err := s.Get(ctx, requestID)
		if err != nil {
			return Request{}, false, err
		}
		if req.Status != StatusPending {
			return req, true, nil
		}
		return Request{}, false, nil
	}
	req, err := s.waiter.Await(ctx, requestID, decision.DefaultFallbackInterval, poll)
	if err != nil && ctx.Err() != nil {
		return s.resolvedAfterContextDone(ctx, requestID)
	}
	return req, err
}

func (s *Service) resolvedAfterContextDone(ctx context.Context, requestID string) (Request, error) {
	// A resolution may have committed before its notification was delivered.
	// Prefer the answer over the caller's cancellation.
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if req, err := s.Get(finalCtx, requestID); err == nil && req.Status != StatusPending {
		return req, nil
	}
	return Request{}, ctx.Err()
}

func (s *Service) Submit(ctx context.Context, input SubmitInput) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("user input persistence not configured")
	}
	if err := validateID(input.RequestID); err != nil {
		return Request{}, err
	}
	actorID, err := s.optionalChannelIdentityID(ctx, input.ActorChannelIdentityID)
	if err != nil {
		return Request{}, err
	}
	respondableRow, err := s.persistence.GetRespondable(ctx, ResolveRecordInput{
		ID:                  input.RequestID,
		RuntimeFencingToken: runtimeFencingToken(ctx),
	})
	if err != nil {
		if errors.Is(mapLookupErr(err), ErrNotFound) {
			return Request{}, ErrAlreadyDecided
		}
		return Request{}, err
	}
	req := requestFromRespondableRow(respondableRow)
	if err := runtimefence.ValidateScope(ctx, req.BotID, req.SessionID); err != nil {
		return Request{}, err
	}
	result, err := submittedResult(req.UIPayload, input.Answers)
	if err != nil {
		return Request{}, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return Request{}, err
	}
	runtimeToken := runtimeFencingToken(ctx)
	var row Record
	err = s.withRuntimeFence(ctx, req.BotID, req.SessionID, func(store Store) error {
		var submitErr error
		row, submitErr = store.Submit(ctx, ResultRecordInput{
			ID:                           input.RequestID,
			ResultJSON:                   resultJSON,
			RespondedByChannelIdentityID: actorID,
			RuntimeFencingToken:          runtimeToken,
		})
		return submitErr
	})
	return s.resolveAndNotify(ctx, input.RequestID, row, err)
}

func (s *Service) Cancel(ctx context.Context, input CancelInput) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("user input persistence not configured")
	}
	if err := validateID(input.RequestID); err != nil {
		return Request{}, err
	}
	actorID, err := s.optionalChannelIdentityID(ctx, input.ActorChannelIdentityID)
	if err != nil {
		return Request{}, err
	}
	result := canceledResult(input.Reason)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return Request{}, err
	}
	runtimeToken := runtimeFencingToken(ctx)
	var row Record
	err = s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateUserInputFence(ctx, store, input.RequestID); err != nil {
			return err
		}
		var cancelErr error
		row, cancelErr = store.Cancel(ctx, ResultRecordInput{
			ID:                           input.RequestID,
			ResultJSON:                   resultJSON,
			RespondedByChannelIdentityID: actorID,
			RuntimeFencingToken:          runtimeToken,
		})
		return cancelErr
	})
	return s.resolveAndNotify(ctx, input.RequestID, row, err)
}

func (s *Service) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]Request, error) {
	if s == nil || s.persistence == nil {
		return nil, errors.New("user input persistence not configured")
	}
	if err := validateID(botID); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	resultJSON, err := json.Marshal(canceledResult(reason))
	if err != nil {
		return nil, err
	}
	params := CancelSessionInput{
		SessionInput: SessionInput{
			BotID:               botID,
			SessionID:           sessionID,
			RuntimeFencingToken: runtimeFencingToken(ctx),
		},
		ResultJSON: resultJSON,
	}
	var rows []Record
	err = s.withRuntimeFence(ctx, botID, sessionID, func(store Store) error {
		var cancelErr error
		rows, cancelErr = store.CancelPendingBySession(ctx, params)
		return cancelErr
	})
	if err != nil {
		return nil, err
	}
	requests := make([]Request, 0, len(rows))
	for _, row := range rows {
		req := requestFromRow(row)
		requests = append(requests, req)
		s.notifyResolved(req)
	}
	return requests, nil
}

func (s *Service) Fail(ctx context.Context, requestID string, result map[string]any) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("user input persistence not configured")
	}
	if err := validateID(requestID); err != nil {
		return Request{}, err
	}
	if result == nil {
		result = map[string]any{"status": StatusFailed}
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return Request{}, err
	}
	runtimeToken := runtimeFencingToken(ctx)
	var row Record
	err = s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateUserInputFence(ctx, store, requestID); err != nil {
			return err
		}
		var failErr error
		row, failErr = store.Fail(ctx, ResultRecordInput{
			ID:                  requestID,
			ResultJSON:          resultJSON,
			RuntimeFencingToken: runtimeToken,
		})
		return failErr
	})
	return s.resolveAndNotify(ctx, requestID, row, err)
}

func (s *Service) UpdatePromptMessage(ctx context.Context, requestID, promptMessageID, externalID string) (Request, error) {
	if err := validateID(requestID); err != nil {
		return Request{}, err
	}
	var row Record
	err := s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateUserInputFence(ctx, store, requestID); err != nil {
			return err
		}
		var updateErr error
		row, updateErr = store.UpdatePrompt(ctx, UpdatePromptInput{
			ID:                      requestID,
			PromptMessageID:         optionalID(promptMessageID),
			PromptExternalMessageID: strings.TrimSpace(externalID),
		})
		return updateErr
	})
	return requestFromRowOrErr(row, err)
}

func (s *Service) UpdateAssistantMessage(ctx context.Context, requestID, messageID string) (Request, error) {
	if err := validateID(requestID); err != nil {
		return Request{}, err
	}
	var row Record
	err := s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateUserInputFence(ctx, store, requestID); err != nil {
			return err
		}
		var updateErr error
		row, updateErr = store.UpdateAssistantMessage(ctx, requestID, optionalID(messageID))
		return updateErr
	})
	return requestFromRowOrErr(row, err)
}

func (s *Service) UpdateToolResultMessage(ctx context.Context, requestID, messageID string) (Request, error) {
	if err := validateID(requestID); err != nil {
		return Request{}, err
	}
	var row Record
	err := s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateUserInputFence(ctx, store, requestID); err != nil {
			return err
		}
		var updateErr error
		row, updateErr = store.UpdateToolResultMessage(ctx, requestID, optionalID(messageID))
		return updateErr
	})
	return requestFromRowOrErr(row, err)
}

func (s *Service) ListPendingBySession(ctx context.Context, botID, sessionID string) ([]Request, error) {
	return s.listBySession(ctx, botID, sessionID, true)
}

func (s *Service) withRuntimeFence(ctx context.Context, botID, sessionID string, fn func(Store) error) error {
	if _, fenced := runtimefence.FromContext(ctx); !fenced {
		return fn(s.persistence)
	}
	return s.persistence.InInputFenceTransaction(ctx, botID, sessionID, fn)
}

func runtimeFencingToken(ctx context.Context) *int64 {
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return nil
	}
	token := fence.Token
	return &token
}

func validateUserInputFence(ctx context.Context, store Store, id string) error {
	if _, fenced := runtimefence.FromContext(ctx); !fenced {
		return nil
	}
	row, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	request := requestFromRow(row)
	return runtimefence.ValidateScope(ctx, request.BotID, request.SessionID)
}

func (s *Service) ListBySession(ctx context.Context, botID, sessionID string) ([]Request, error) {
	return s.listBySession(ctx, botID, sessionID, false)
}

func (s *Service) ListBySessionToolCalls(ctx context.Context, botID, sessionID string, toolCallIDs []string) ([]Request, error) {
	toolCallIDs = normalizeToolCallIDs(toolCallIDs)
	if len(toolCallIDs) == 0 {
		return nil, nil
	}
	if err := validateID(botID); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	rows, err := s.persistence.ListBySessionToolCalls(ctx, botID, sessionID, toolCallIDs)
	if err != nil {
		return nil, err
	}
	result := make([]Request, 0, len(rows))
	for _, row := range rows {
		result = append(result, requestFromRow(row))
	}
	return result, nil
}

func (s *Service) listBySession(ctx context.Context, botID, sessionID string, pendingOnly bool) ([]Request, error) {
	if err := validateID(botID); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	var rows []Record
	var err error
	if pendingOnly {
		rows, err = s.persistence.ListPendingBySession(ctx, botID, sessionID)
	} else {
		rows, err = s.persistence.ListBySession(ctx, botID, sessionID)
	}
	if err != nil {
		return nil, err
	}
	result := make([]Request, 0, len(rows))
	for _, row := range rows {
		result = append(result, requestFromRow(row))
	}
	return result, nil
}

func normalizeToolCallIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func DeferredMetadata(req Request) map[string]any {
	return map[string]any{
		"kind":          DeferredKind,
		"user_input_id": req.ID,
		"short_id":      req.ShortID,
		"status":        req.Status,
		"tool_call_id":  req.ToolCallID,
		"tool_name":     req.ToolName,
		"ui_payload":    req.UIPayload,
	}
}

// submittedResult validates the user's answers against the stored payload and
// builds the tool result returned to the model. Every question needs an explicit
// entry so a deliberate skip cannot be confused with a broken client payload.
func ValidateAnswers(payload UIPayload, answers []agentdomain.QuestionAnswer) error {
	_, err := submittedResult(payload, answers)
	return err
}

func submittedResult(payload UIPayload, answers []agentdomain.QuestionAnswer) (map[string]any, error) {
	if len(payload.Questions) == 0 {
		return nil, errors.New("user input request has no questions")
	}
	byQuestion := make(map[string]agentdomain.QuestionAnswer, len(answers))
	for _, answer := range answers {
		id := strings.TrimSpace(answer.QuestionID)
		if id == "" {
			return nil, errors.New("answers[].question_id is required")
		}
		if _, ok := payload.Question(id); !ok {
			return nil, fmt.Errorf("unknown question %q", id)
		}
		if _, dup := byQuestion[id]; dup {
			return nil, fmt.Errorf("duplicate answer for question %q", id)
		}
		byQuestion[id] = answer
	}

	resultAnswers := make([]map[string]any, 0, len(payload.Questions))
	for _, question := range payload.Questions {
		answer, ok := byQuestion[question.ID]
		if !ok {
			return nil, fmt.Errorf("missing answer for question %q", question.ID)
		}
		entry, err := answerEntry(question, answer)
		if err != nil {
			return nil, err
		}
		resultAnswers = append(resultAnswers, entry)
	}
	return map[string]any{
		"status":      StatusSubmitted,
		"answers":     resultAnswers,
		"instruction": submitInstruction,
	}, nil
}

func answerEntry(question UIQuestion, answer agentdomain.QuestionAnswer) (map[string]any, error) {
	entry := map[string]any{
		"question_id": question.ID,
		"question":    question.Text,
	}
	optionIDs := cleanIDs(answer.OptionIDs)
	customText := strings.TrimSpace(answer.CustomText)
	text := strings.TrimSpace(answer.Text)
	if answer.Skipped {
		if len(optionIDs) > 0 || customText != "" || text != "" {
			return nil, fmt.Errorf("question %q cannot be skipped and answered", question.ID)
		}
		entry["skipped"] = true
		return entry, nil
	}

	if question.Kind == QuestionKindText {
		if len(optionIDs) > 0 || customText != "" {
			return nil, fmt.Errorf("question %q is free text and does not accept option selections", question.ID)
		}
		if text == "" {
			return nil, fmt.Errorf("question %q requires a text answer", question.ID)
		}
		entry["text"] = text
		return entry, nil
	}

	if text != "" {
		return nil, fmt.Errorf("question %q is a select question; use option_ids or custom_text", question.ID)
	}
	if customText != "" && !question.AllowCustom {
		return nil, fmt.Errorf("question %q does not allow a custom answer", question.ID)
	}
	if question.Kind == QuestionKindSingleSelect {
		if len(optionIDs) > 1 {
			return nil, fmt.Errorf("question %q accepts exactly one option", question.ID)
		}
		if len(optionIDs) == 1 && customText != "" {
			return nil, fmt.Errorf("question %q accepts either one option or a custom answer, not both", question.ID)
		}
	}
	if len(optionIDs) == 0 && customText == "" {
		return nil, fmt.Errorf("question %q requires a selection", question.ID)
	}

	selected := make([]map[string]any, 0, len(optionIDs))
	seen := make(map[string]struct{}, len(optionIDs))
	for _, id := range optionIDs {
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("question %q selects option %q more than once", question.ID, id)
		}
		seen[id] = struct{}{}
		option, ok := question.Option(id)
		if !ok {
			return nil, fmt.Errorf("question %q has no option %q", question.ID, id)
		}
		selected = append(selected, map[string]any{"id": option.ID, "label": option.Label})
	}
	if len(selected) > 0 {
		entry["selected"] = selected
	}
	if customText != "" {
		entry["custom_text"] = customText
	}
	return entry, nil
}

func canceledResult(reason string) map[string]any {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "user_canceled"
	}
	return map[string]any{
		"status":      StatusCanceled,
		"reason":      reason,
		"instruction": cancelInstruction,
	}
}

func IsACPMCPRequest(req Request) bool {
	if req.ProviderMetadata == nil {
		return false
	}
	return strings.TrimSpace(stringValue(req.ProviderMetadata["source"])) == ProviderSourceACPMCP
}

func cleanIDs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func requestFromRowOrErr(row Record, err error) (Request, error) {
	if err != nil {
		return Request{}, mapLookupErr(err)
	}
	return requestFromRow(row), nil
}

func requestFromRespondableRow(row Record) Request {
	request := requestFromRow(row)
	request.Status = strings.TrimSpace(row.Status)
	return request
}

func mapLookupErr(err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func requestFromRow(row Record) Request {
	req := Request{
		ID:                      row.ID,
		BotID:                   row.BotID,
		SessionID:               row.SessionID,
		WorkspaceTargetID:       strings.TrimSpace(row.WorkspaceTargetID),
		ToolCallID:              strings.TrimSpace(row.ToolCallID),
		ToolName:                strings.TrimSpace(row.ToolName),
		ShortID:                 row.ShortID,
		Status:                  strings.TrimSpace(row.Status),
		InteractionRevision:     row.InteractionRevision,
		PromptExternalMessageID: strings.TrimSpace(row.PromptExternalMessageID),
		SourcePlatform:          strings.TrimSpace(row.SourcePlatform),
		ReplyTarget:             strings.TrimSpace(row.ReplyTarget),
		ConversationType:        strings.TrimSpace(row.ConversationType),
		CreatedAt:               row.CreatedAt,
		RuntimeFenced:           row.RuntimeFencingToken != nil,
	}
	req.RouteID = row.RouteID
	req.ChannelIdentityID = row.ChannelIdentityID
	req.RespondedAt = row.RespondedAt
	req.CanceledAt = row.CanceledAt
	req.ExpiresAt = row.ExpiresAt
	// Present overdue pending rows as expired even before any sweeper runs;
	// the SQL pending/submit guards enforce the same boundary transactionally.
	if req.Status == StatusPending && req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		req.Status = StatusExpired
	}
	_ = json.Unmarshal(row.InputJSON, &req.Input)
	req.UIPayload = PayloadFromStored(row.UIPayloadJSON)
	_ = json.Unmarshal(row.InteractionJSON, &req.Interaction)
	_ = json.Unmarshal(row.ResultJSON, &req.Result)
	_ = json.Unmarshal(row.ProviderMetadata, &req.ProviderMetadata)
	return req
}

func marshalObject(value any) ([]byte, error) {
	if value == nil {
		return []byte("{}"), nil
	}
	if data, ok := value.([]byte); ok {
		if len(data) == 0 {
			return []byte("{}"), nil
		}
		return data, nil
	}
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return []byte("{}"), nil
		}
		return []byte(trimmed), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []byte("{}"), nil
	}
	return data, nil
}

func optionalID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	id, err := canonicalID(trimmed)
	if err != nil {
		return ""
	}
	return id
}

func optionalTime(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	result := *value
	return &result
}

func (s *Service) optionalChannelIdentityID(ctx context.Context, value string) (string, error) {
	id := optionalID(value)
	if id == "" {
		return "", nil
	}
	if s == nil || s.persistence == nil {
		return "", nil
	}
	exists, err := s.persistence.ChannelIdentityExists(ctx, id)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	return id, nil
}

func validateID(value string) error {
	_, err := canonicalID(value)
	return err
}

func canonicalID(value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
