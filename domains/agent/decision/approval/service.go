package approval

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
	"github.com/memohai/memoh/domains/agent/decision"
	"github.com/memohai/memoh/domains/agent/extension/hooks"
)

type Service struct {
	persistence Persistence

	policies PolicyProvider
	hooks    *hooks.Service
	logger   *slog.Logger
	targets  WorkspaceTargetPolicyResolver

	waiter *decision.Waiter[Request]
}

func NewService(log *slog.Logger, persistence Persistence, policies PolicyProvider) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		persistence: persistence,
		policies:    policies,
		logger:      log.With(slog.String("service", "toolapproval")),
		waiter:      decision.NewWaiter[Request](),
	}
}

func (s *Service) SetHookService(h *hooks.Service) {
	if s != nil {
		s.hooks = h
	}
}

func (s *Service) SetWorkspaceTargetPolicyResolver(resolver WorkspaceTargetPolicyResolver) {
	if s != nil {
		s.targets = resolver
	}
}

func (s *Service) Evaluate(ctx context.Context, input CreatePendingInput) (Evaluation, error) {
	eval, err := s.EvaluatePolicy(ctx, input)
	if err != nil || eval.Decision != DecisionNeedsApproval {
		return eval, err
	}
	input.ExecutionLocation = eval.ExecutionLocation
	req, err := s.CreatePending(ctx, input)
	if err != nil {
		return Evaluation{}, err
	}
	return Evaluation{Decision: DecisionNeedsApproval, Request: req, ExecutionLocation: eval.ExecutionLocation}, nil
}

func (s *Service) EvaluatePolicy(ctx context.Context, input CreatePendingInput) (Evaluation, error) {
	if s == nil {
		return Evaluation{Decision: DecisionBypass}, nil
	}
	if input.WorkspaceTargeted && s.targets != nil {
		args, ok := input.ToolInput.(map[string]any)
		if !ok {
			return Evaluation{}, errors.New("workspace tool input must be an object")
		}
		target, err := s.targets.ResolveWorkspaceTargetPolicy(ctx, input.BotID, readString(args, "target_id"))
		if err != nil {
			return Evaluation{}, err
		}
		if strings.TrimSpace(target.TargetID) == "" {
			return Evaluation{}, errors.New("workspace target resolver returned an empty target_id")
		}
		// Mutate the original map so immediate execution and a deferred pending
		// request use the same canonical target even if Primary changes later.
		args["target_id"] = target.TargetID
		return Evaluation{
			Decision: policyDecision(target.Config, input.ToolName, args),
			ExecutionLocation: &ExecutionLocation{
				TargetID: strings.TrimSpace(target.TargetID),
				Kind:     strings.TrimSpace(target.Kind),
				Name:     strings.TrimSpace(target.Name),
			},
		}, nil
	}
	if s.policies == nil {
		return Evaluation{Decision: DecisionBypass}, nil
	}
	policy, err := s.policies.ToolApprovalPolicy(ctx, input.BotID)
	if err != nil {
		return Evaluation{}, err
	}
	return Evaluation{Decision: policyDecision(policy, input.ToolName, input.ToolInput)}, nil
}

func (s *Service) CreatePending(ctx context.Context, input CreatePendingInput) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("tool approval persistence not configured")
	}
	botID, err := canonicalID(input.BotID)
	if err != nil {
		return Request{}, err
	}
	sessionID, err := canonicalID(input.SessionID)
	if err != nil {
		return Request{}, err
	}
	toolInput, err := json.Marshal(input.ToolInput)
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
	operation, ok := OperationForTool(input.ToolName)
	if !ok {
		return Request{}, errors.New("unsupported tool approval operation")
	}
	workspaceTargetID := strings.TrimSpace(input.WorkspaceTargetID)
	if input.ExecutionLocation != nil && strings.TrimSpace(input.ExecutionLocation.TargetID) != "" {
		workspaceTargetID = strings.TrimSpace(input.ExecutionLocation.TargetID)
	}
	if err := s.runApprovalHook(ctx, hooks.EventBeforeApprovalCreate, input, Request{}, true); err != nil {
		return Request{}, err
	}
	params := CreateRecordInput{
		BotID:                        botID,
		SessionID:                    sessionID,
		RouteID:                      optionalID(input.RouteID),
		ChannelIdentityID:            channelIdentityID,
		WorkspaceTargetID:            workspaceTargetID,
		ToolCallID:                   strings.TrimSpace(input.ToolCallID),
		ToolName:                     strings.TrimSpace(input.ToolName),
		Operation:                    operation,
		ToolInput:                    toolInput,
		RuntimeFencingToken:          runtimeFencingToken(ctx),
		RequestedByChannelIdentityID: requestedByID,
		RequestedMessageID:           optionalID(input.RequestedMessageID),
		SourcePlatform:               strings.TrimSpace(input.SourcePlatform),
		ReplyTarget:                  strings.TrimSpace(input.ReplyTarget),
		ConversationType:             strings.TrimSpace(input.ConversationType),
	}
	var row Record
	err = s.persistence.InApprovalCreateTransaction(ctx, input.BotID, input.SessionID, func(store Store) error {
		var createErr error
		row, createErr = store.Create(ctx, params)
		return createErr
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Request{}, ErrAlreadyDecided
		}
		return Request{}, err
	}
	req := requestFromRow(row)
	req.ExecutionLocation = cloneExecutionLocation(input.ExecutionLocation)
	if req.Status != StatusPending {
		return Request{}, ErrAlreadyDecided
	}
	_ = s.runApprovalHook(ctx, hooks.EventApprovalRequested, input, req, false)
	return req, nil
}

func cloneExecutionLocation(location *ExecutionLocation) *ExecutionLocation {
	if location == nil {
		return nil
	}
	clone := *location
	return &clone
}

func (s *Service) ResolveTarget(ctx context.Context, input ResolveInput) (Request, error) {
	botID, err := canonicalID(input.BotID)
	if err != nil {
		return Request{}, err
	}
	explicit := strings.TrimSpace(input.ExplicitID)
	if strings.TrimSpace(input.SessionID) == "" && explicit != "" {
		if validateID(explicit) == nil {
			row, err := s.persistence.Get(ctx, explicit)
			if err != nil {
				return Request{}, mapLookupErr(err)
			}
			req := requestFromRow(row)
			if req.BotID != botID || req.Status != StatusPending {
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
			row, err := s.persistence.Get(ctx, explicit)
			if err != nil {
				return Request{}, mapLookupErr(err)
			}
			req := requestFromRow(row)
			if req.BotID != botID || req.SessionID != sessionID || req.Status != StatusPending {
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

func (s *Service) Approve(ctx context.Context, approvalID, actorID, reason string) (Request, error) {
	if err := validateID(approvalID); err != nil {
		return Request{}, err
	}
	decidedBy, err := s.optionalChannelIdentityID(ctx, actorID)
	if err != nil {
		return Request{}, err
	}
	runtimeToken := runtimeFencingToken(ctx)
	var row Record
	err = s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateToolApprovalFence(ctx, store, approvalID); err != nil {
			return err
		}
		var approveErr error
		row, approveErr = store.Approve(ctx, DecisionRecordInput{
			ID:                         approvalID,
			Reason:                     strings.TrimSpace(reason),
			DecidedByChannelIdentityID: decidedBy,
			RuntimeFencingToken:        runtimeToken,
		})
		return approveErr
	})
	req, err := s.resolveAndNotify(ctx, approvalID, row, err)
	if err == nil && strings.TrimSpace(actorID) != "" {
		req.DecidedByUser = true
	}
	if err == nil {
		_ = s.runApprovalHook(ctx, hooks.EventApprovalResolved, CreatePendingInput{}, req, false)
	}
	return req, err
}

func (s *Service) Reject(ctx context.Context, approvalID, actorID, reason string) (Request, error) {
	if err := validateID(approvalID); err != nil {
		return Request{}, err
	}
	decidedBy, err := s.optionalChannelIdentityID(ctx, actorID)
	if err != nil {
		return Request{}, err
	}
	runtimeToken := runtimeFencingToken(ctx)
	var row Record
	err = s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateToolApprovalFence(ctx, store, approvalID); err != nil {
			return err
		}
		var rejectErr error
		row, rejectErr = store.Reject(ctx, DecisionRecordInput{
			ID:                         approvalID,
			Reason:                     strings.TrimSpace(reason),
			DecidedByChannelIdentityID: decidedBy,
			RuntimeFencingToken:        runtimeToken,
		})
		return rejectErr
	})
	req, err := s.resolveAndNotify(ctx, approvalID, row, err)
	if err == nil && strings.TrimSpace(actorID) != "" {
		req.DecidedByUser = true
	}
	if err == nil {
		_ = s.runApprovalHook(ctx, hooks.EventApprovalResolved, CreatePendingInput{}, req, false)
	}
	return req, err
}

// CancelPendingForSession closes pending approvals that belonged to an ended
// turn and wakes any in-process waiters.
func (s *Service) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]Request, error) {
	if s == nil || s.persistence == nil {
		return nil, errors.New("tool approval persistence not configured")
	}
	if err := validateID(botID); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "tool approval cancelled: the turn that requested it ended"
	}
	params := CancelSessionInput{
		SessionInput: SessionInput{
			BotID:               botID,
			SessionID:           sessionID,
			RuntimeFencingToken: runtimeFencingToken(ctx),
		},
		Reason: reason,
	}
	var rows []Record
	err := s.withRuntimeFence(ctx, botID, sessionID, func(store Store) error {
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

func (s *Service) withRuntimeFence(ctx context.Context, botID, sessionID string, fn func(Store) error) error {
	if _, fenced := runtimefence.FromContext(ctx); !fenced {
		return fn(s.persistence)
	}
	return s.persistence.InApprovalFenceTransaction(ctx, botID, sessionID, fn)
}

func runtimeFencingToken(ctx context.Context) *int64 {
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return nil
	}
	token := fence.Token
	return &token
}

func validateToolApprovalFence(ctx context.Context, store Store, id string) error {
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

func (s *Service) Get(ctx context.Context, approvalID string) (Request, error) {
	if s == nil || s.persistence == nil {
		return Request{}, errors.New("tool approval persistence not configured")
	}
	if err := validateID(approvalID); err != nil {
		return Request{}, err
	}
	row, err := s.persistence.Get(ctx, approvalID)
	return requestFromRowOrErr(row, err)
}

func (s *Service) WaitForDecision(ctx context.Context, approvalID string) (Request, error) {
	if s == nil {
		return Request{}, errors.New("tool approval service not configured")
	}
	poll := func(ctx context.Context) (Request, bool, error) {
		req, err := s.Get(ctx, approvalID)
		if err != nil {
			return Request{}, false, err
		}
		if req.Status != StatusPending {
			return req, true, nil
		}
		return Request{}, false, nil
	}
	req, err := s.waiter.Await(ctx, approvalID, decision.DefaultFallbackInterval, poll)
	if err != nil && ctx.Err() != nil {
		return s.resolvedAfterContextDone(ctx, approvalID)
	}
	return req, err
}

func (s *Service) RegisterWaiter(approvalID string) func() {
	if s == nil || s.waiter == nil {
		return func() {}
	}
	return s.waiter.Register(approvalID)
}

func (s *Service) NotifyApprovalTimeout(ctx context.Context, req Request) {
	_ = s.runApprovalHook(ctx, hooks.EventApprovalTimeout, CreatePendingInput{}, req, false)
}

func (s *Service) runApprovalHook(ctx context.Context, event string, input CreatePendingInput, req Request, failOnError bool) error {
	if s == nil || s.hooks == nil {
		return nil
	}
	botID := firstApprovalValue(req.BotID, input.BotID)
	sessionID := firstApprovalValue(req.SessionID, input.SessionID)
	payload := map[string]any{
		"tool_call_id": firstApprovalValue(req.ToolCallID, input.ToolCallID),
		"tool_name":    firstApprovalValue(req.ToolName, input.ToolName),
		"operation":    req.Operation,
		"status":       req.Status,
		"approval_id":  req.ID,
		"short_id":     req.ShortID,
		"reason":       req.DecisionReason,
	}
	if req.Operation == "" {
		if operation, ok := OperationForTool(input.ToolName); ok {
			payload["operation"] = operation
		}
	}
	if req.ToolInput != nil {
		payload["tool_input"] = req.ToolInput
	} else if input.ToolInput != nil {
		payload["tool_input"] = input.ToolInput
	}
	result, err := s.hooks.Run(ctx, hooks.Request{
		Version:   1,
		Event:     event,
		BotID:     botID,
		SessionID: sessionID,
		Tool: &hooks.ToolPayload{
			Name:   firstApprovalValue(req.ToolName, input.ToolName),
			CallID: firstApprovalValue(req.ToolCallID, input.ToolCallID),
			Input:  payload["tool_input"],
		},
		Approval: payload,
	}, nil)
	if err == nil && result.Decision == hooks.DecisionDeny {
		err = hooks.ErrDenied
	}
	if err != nil {
		if failOnError {
			return err
		}
		if s.logger != nil {
			s.logger.WarnContext(ctx, "approval hook failed",
				slog.String("event", event),
				slog.String("bot_id", botID),
				slog.String("session_id", sessionID),
				slog.Any("error", err),
			)
		}
	}
	return nil
}

func firstApprovalValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Service) HasWaiter(approvalID string) bool {
	return s != nil && s.waiter != nil && s.waiter.Has(approvalID)
}

// CanRespond reports whether a waiter-backed approval can accept a user
// decision in this process. Native chat approvals are DB-deferred and must not
// use this helper; it is for ACP/MCP flows whose caller is blocked in process.
func (s *Service) CanRespond(req Request) bool {
	return strings.EqualFold(NormalizedStatus(req.Status), StatusPending) && s.HasWaiter(req.ID)
}

// resolveAndNotify converts a guarded approval update into the shared service
// result. If the update matched no pending row, a terminal row with the same
// ID means another responder or waiter won the race.
func (s *Service) resolveAndNotify(ctx context.Context, approvalID string, row Record, err error) (Request, error) {
	req, err := requestFromRowOrErr(row, err)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if validateID(approvalID) == nil {
				if existing, getErr := s.persistence.Get(ctx, approvalID); getErr == nil &&
					(existing.Status != StatusPending || existing.RuntimeFencingToken != nil) {
					return Request{}, ErrAlreadyDecided
				}
			}
		}
		return Request{}, err
	}
	s.notifyResolved(req)
	return req, nil
}

func (s *Service) notifyResolved(req Request) {
	if s == nil || s.waiter == nil {
		return
	}
	s.waiter.Notify(req.ID, req)
}

func (s *Service) resolvedAfterContextDone(ctx context.Context, approvalID string) (Request, error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if req, err := s.Get(finalCtx, approvalID); err == nil && req.Status != StatusPending {
		return req, nil
	}
	return Request{}, ctx.Err()
}

func (s *Service) UpdatePromptMessage(ctx context.Context, approvalID, promptMessageID, externalID string) (Request, error) {
	if err := validateID(approvalID); err != nil {
		return Request{}, err
	}
	var row Record
	err := s.withRuntimeFence(ctx, "", "", func(store Store) error {
		if err := validateToolApprovalFence(ctx, store, approvalID); err != nil {
			return err
		}
		var updateErr error
		row, updateErr = store.UpdatePrompt(ctx, UpdatePromptInput{
			ID:                      approvalID,
			PromptMessageID:         optionalID(promptMessageID),
			PromptExternalMessageID: strings.TrimSpace(externalID),
		})
		return updateErr
	})
	return requestFromRowOrErr(row, err)
}

func (s *Service) ListPendingBySession(ctx context.Context, botID, sessionID string) ([]Request, error) {
	return s.listBySession(ctx, botID, sessionID, true)
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

func requestFromRowOrErr(row Record, err error) (Request, error) {
	if err != nil {
		return Request{}, mapLookupErr(err)
	}
	return requestFromRow(row), nil
}

func mapLookupErr(err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return err
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

func requestFromRow(row Record) Request {
	var input map[string]any
	_ = json.Unmarshal(row.ToolInput, &input)
	req := Request{
		ID:                      row.ID,
		BotID:                   row.BotID,
		SessionID:               row.SessionID,
		WorkspaceTargetID:       strings.TrimSpace(row.WorkspaceTargetID),
		ToolCallID:              strings.TrimSpace(row.ToolCallID),
		ToolName:                strings.TrimSpace(row.ToolName),
		Operation:               strings.TrimSpace(row.Operation),
		ToolInput:               input,
		ShortID:                 row.ShortID,
		Status:                  strings.TrimSpace(row.Status),
		DecisionReason:          strings.TrimSpace(row.DecisionReason),
		PromptExternalMessageID: strings.TrimSpace(row.PromptExternalMessageID),
		SourcePlatform:          strings.TrimSpace(row.SourcePlatform),
		ReplyTarget:             strings.TrimSpace(row.ReplyTarget),
		ConversationType:        strings.TrimSpace(row.ConversationType),
		CreatedAt:               row.CreatedAt,
		RuntimeFenced:           row.RuntimeFencingToken != nil,
	}
	if req.Operation == "" {
		req.Operation, _ = OperationForTool(req.ToolName)
	}
	req.RouteID = row.RouteID
	req.ChannelIdentityID = row.ChannelIdentityID
	if row.DecidedByChannelIdentityID != "" {
		req.DecidedByUser = true
	}
	req.DecidedAt = row.DecidedAt
	return req
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
