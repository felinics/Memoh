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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/decision"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/runtimefence"
)

type Service struct {
	queries dbstore.Queries

	policies PolicyProvider
	hooks    *hooks.Service
	logger   *slog.Logger
	targets  WorkspaceTargetPolicyResolver

	waiter *decision.Waiter[Request]
}

func NewService(log *slog.Logger, queries dbstore.Queries, policies PolicyProvider) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries:  queries,
		policies: policies,
		logger:   log.With(slog.String("service", "toolapproval")),
		waiter:   decision.NewWaiter[Request](),
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
	eval, err := s.policyEvaluation(ctx, input)
	if err != nil {
		return eval, err
	}
	if eval.Decision == DecisionDeny {
		// An explicit admin deny outranks a forced review: auto-declining is an
		// honest answer, while showing the card would let a user approve what
		// the policy hard-blocks.
		return eval, nil
	}
	if input.ForceReview {
		// Requests that must never be auto-answered (consent shapes, unmapped
		// generic permissions): with the policy disabled a bypass would select
		// the agent's accept option and report a consent the user never gave.
		// Interactive turns show the card; non-interactive ones auto-reject,
		// which reads as an honest decline instead of a false accept.
		eval.Decision = DecisionNeedsApproval
	}
	return eval, nil
}

func (s *Service) policyEvaluation(ctx context.Context, input CreatePendingInput) (Evaluation, error) {
	if input.WorkspaceTargeted && s.targets != nil {
		args, ok := input.ToolInput.(map[string]any)
		if !ok {
			return Evaluation{}, errors.New("workspace tool input must be an object")
		}
		targetID := readString(args, "target_id")
		if targetID == "" {
			targetID = strings.TrimSpace(input.WorkspaceTargetID)
		}
		target, err := s.targets.ResolveWorkspaceTargetPolicy(ctx, input.BotID, targetID)
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
	if s == nil || s.queries == nil {
		return Request{}, errors.New("tool approval queries not configured")
	}
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return Request{}, err
	}
	sessionID, err := db.ParseUUID(input.SessionID)
	if err != nil {
		return Request{}, err
	}
	toolInput, err := json.Marshal(input.ToolInput)
	if err != nil {
		return Request{}, err
	}
	options := input.Options
	if options == nil {
		options = []PermissionOption{}
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return Request{}, err
	}
	channelIdentityID, err := s.optionalChannelIdentityUUID(ctx, input.ChannelIdentityID)
	if err != nil {
		return Request{}, err
	}
	requestedByID, err := s.optionalChannelIdentityUUID(ctx, input.RequestedByChannelIdentityID)
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
	params := sqlc.CreateToolApprovalRequestParams{
		BotID:                        botID,
		SessionID:                    sessionID,
		RouteID:                      optionalUUID(input.RouteID),
		ChannelIdentityID:            channelIdentityID,
		WorkspaceTargetID:            workspaceTargetID,
		ToolCallID:                   strings.TrimSpace(input.ToolCallID),
		ToolName:                     strings.TrimSpace(input.ToolName),
		Operation:                    operation,
		ToolInput:                    toolInput,
		Options:                      optionsJSON,
		RuntimeFencingToken:          runtimeFencingToken(ctx),
		RequestedByChannelIdentityID: requestedByID,
		RequestedMessageID:           optionalUUID(input.RequestedMessageID),
		SourcePlatform:               strings.TrimSpace(input.SourcePlatform),
		ReplyTarget:                  strings.TrimSpace(input.ReplyTarget),
		ConversationType:             strings.TrimSpace(input.ConversationType),
	}
	var row sqlc.ToolApprovalRequest
	err = decision.InCreateTransaction(ctx, s.queries, input.BotID, input.SessionID, func(queries dbstore.Queries) error {
		var createErr error
		row, createErr = queries.CreateToolApprovalRequest(ctx, params)
		return createErr
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return Request{}, err
	}
	explicit := strings.TrimSpace(input.ExplicitID)
	if strings.TrimSpace(input.SessionID) == "" && explicit != "" {
		if parsed, err := db.ParseUUID(explicit); err == nil {
			row, err := s.queries.GetToolApprovalRequest(ctx, parsed)
			if err != nil {
				return Request{}, mapLookupErr(err)
			}
			req := requestFromRow(row)
			if req.BotID != uuid.UUID(botID.Bytes).String() || req.Status != StatusPending {
				return Request{}, ErrNotFound
			}
			return req, nil
		}
		return Request{}, ErrNotFound
	}
	sessionID, err := db.ParseUUID(input.SessionID)
	if err != nil {
		return Request{}, err
	}
	if explicit != "" {
		if shortID, err := strconv.Atoi(explicit); err == nil {
			row, err := s.queries.GetPendingToolApprovalBySessionShortID(ctx, sqlc.GetPendingToolApprovalBySessionShortIDParams{
				BotID:     botID,
				SessionID: sessionID,
				ShortID:   int32(shortID), //nolint:gosec // user-facing approval numbers are small positive integers.
			})
			return requestFromRowOrErr(row, err)
		}
		if parsed, err := db.ParseUUID(explicit); err == nil {
			row, err := s.queries.GetToolApprovalRequest(ctx, parsed)
			if err != nil {
				return Request{}, mapLookupErr(err)
			}
			req := requestFromRow(row)
			if req.BotID != uuid.UUID(botID.Bytes).String() || req.SessionID != uuid.UUID(sessionID.Bytes).String() || req.Status != StatusPending {
				return Request{}, ErrNotFound
			}
			return req, nil
		}
		return Request{}, ErrNotFound
	}
	if replyID := strings.TrimSpace(input.ReplyExternalMessageID); replyID != "" {
		row, err := s.queries.GetPendingToolApprovalByReplyMessage(ctx, sqlc.GetPendingToolApprovalByReplyMessageParams{
			BotID:                   botID,
			SessionID:               sessionID,
			PromptExternalMessageID: replyID,
		})
		if err == nil {
			return requestFromRow(row), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Request{}, err
		}
	}
	row, err := s.queries.GetLatestPendingToolApprovalBySession(ctx, sqlc.GetLatestPendingToolApprovalBySessionParams{
		BotID:     botID,
		SessionID: sessionID,
	})
	return requestFromRowOrErr(row, err)
}

func (s *Service) Approve(ctx context.Context, approvalID, actorID, reason string) (Request, error) {
	return s.ApproveOption(ctx, approvalID, actorID, reason, "")
}

// ApproveOption approves the request recording the agent-provided option the
// decider selected; an empty optionID is the plain binary approve.
func (s *Service) ApproveOption(ctx context.Context, approvalID, actorID, reason, optionID string) (Request, error) {
	id, err := db.ParseUUID(approvalID)
	if err != nil {
		return Request{}, err
	}
	decidedBy, err := s.optionalChannelIdentityUUID(ctx, actorID)
	if err != nil {
		return Request{}, err
	}
	runtimeToken := runtimeFencingToken(ctx)
	responseControlID, responsePayloadHash := decisionResponseIdentity(ctx)
	var row sqlc.ToolApprovalRequest
	err = s.withRuntimeFence(ctx, "", "", func(queries dbstore.Queries) error {
		if err := validateToolApprovalFence(ctx, queries, id); err != nil {
			return err
		}
		var approveErr error
		row, approveErr = queries.ApproveToolApprovalRequest(ctx, sqlc.ApproveToolApprovalRequestParams{
			ID:                         id,
			Reason:                     strings.TrimSpace(reason),
			SelectedOptionID:           optionID,
			DecidedByChannelIdentityID: decidedBy,
			RuntimeFencingToken:        runtimeToken,
			ResponseControlID:          responseControlID,
			ResponsePayloadHash:        responsePayloadHash,
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
	return s.RejectOption(ctx, approvalID, actorID, reason, "")
}

// RejectOption rejects the request recording the agent-provided option the
// decider selected; an empty optionID is the plain binary reject.
func (s *Service) RejectOption(ctx context.Context, approvalID, actorID, reason, optionID string) (Request, error) {
	id, err := db.ParseUUID(approvalID)
	if err != nil {
		return Request{}, err
	}
	decidedBy, err := s.optionalChannelIdentityUUID(ctx, actorID)
	if err != nil {
		return Request{}, err
	}
	runtimeToken := runtimeFencingToken(ctx)
	responseControlID, responsePayloadHash := decisionResponseIdentity(ctx)
	var row sqlc.ToolApprovalRequest
	err = s.withRuntimeFence(ctx, "", "", func(queries dbstore.Queries) error {
		if err := validateToolApprovalFence(ctx, queries, id); err != nil {
			return err
		}
		var rejectErr error
		row, rejectErr = queries.RejectToolApprovalRequest(ctx, sqlc.RejectToolApprovalRequestParams{
			ID:                         id,
			Reason:                     strings.TrimSpace(reason),
			SelectedOptionID:           optionID,
			DecidedByChannelIdentityID: decidedBy,
			RuntimeFencingToken:        runtimeToken,
			ResponseControlID:          responseControlID,
			ResponsePayloadHash:        responsePayloadHash,
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

func decisionResponseIdentity(ctx context.Context) (pgtype.Text, pgtype.Text) {
	identity, ok := decision.ResponseIdentityFromContext(ctx)
	if !ok {
		return pgtype.Text{}, pgtype.Text{}
	}
	return pgtype.Text{String: identity.ControlID, Valid: true},
		pgtype.Text{String: identity.PayloadHash, Valid: true}
}

// CancelPendingForSession closes pending approvals that belonged to an ended
// turn and wakes any in-process waiters.
func (s *Service) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]Request, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("tool approval queries not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "tool approval cancelled: the turn that requested it ended"
	}
	params := sqlc.CancelPendingToolApprovalsBySessionParams{
		BotID:               pgBotID,
		SessionID:           pgSessionID,
		Reason:              reason,
		RuntimeFencingToken: runtimeFencingToken(ctx),
	}
	var rows []sqlc.ToolApprovalRequest
	err = s.withRuntimeFence(ctx, botID, sessionID, func(queries dbstore.Queries) error {
		var cancelErr error
		rows, cancelErr = queries.CancelPendingToolApprovalsBySession(ctx, params)
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

func (s *Service) withRuntimeFence(ctx context.Context, botID, sessionID string, fn func(dbstore.Queries) error) error {
	if _, fenced := runtimefence.FromContext(ctx); !fenced {
		return fn(s.queries)
	}
	return runtimefence.InTransaction(ctx, s.queries, botID, sessionID, fn)
}

func runtimeFencingToken(ctx context.Context) pgtype.Int8 {
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: fence.Token, Valid: true}
}

func validateToolApprovalFence(ctx context.Context, queries dbstore.Queries, id pgtype.UUID) error {
	if _, fenced := runtimefence.FromContext(ctx); !fenced {
		return nil
	}
	row, err := queries.GetToolApprovalRequest(ctx, id)
	if err != nil {
		return err
	}
	request := requestFromRow(row)
	return runtimefence.ValidateScope(ctx, request.BotID, request.SessionID)
}

func (s *Service) Get(ctx context.Context, approvalID string) (Request, error) {
	if s == nil || s.queries == nil {
		return Request{}, errors.New("tool approval queries not configured")
	}
	id, err := db.ParseUUID(approvalID)
	if err != nil {
		return Request{}, err
	}
	row, err := s.queries.GetToolApprovalRequest(ctx, id)
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
			s.logger.Warn("approval hook failed",
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
func (s *Service) resolveAndNotify(ctx context.Context, approvalID string, row sqlc.ToolApprovalRequest, err error) (Request, error) {
	req, err := requestFromRowOrErr(row, err)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if id, parseErr := db.ParseUUID(approvalID); parseErr == nil {
				if existing, getErr := s.queries.GetToolApprovalRequest(ctx, id); getErr == nil &&
					(existing.Status != StatusPending || existing.RuntimeFencingToken.Valid) {
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
	id, err := db.ParseUUID(approvalID)
	if err != nil {
		return Request{}, err
	}
	var row sqlc.ToolApprovalRequest
	err = s.withRuntimeFence(ctx, "", "", func(queries dbstore.Queries) error {
		if err := validateToolApprovalFence(ctx, queries, id); err != nil {
			return err
		}
		var updateErr error
		row, updateErr = queries.UpdateToolApprovalPromptMessage(ctx, sqlc.UpdateToolApprovalPromptMessageParams{
			ID:                      id,
			PromptMessageID:         optionalUUID(promptMessageID),
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
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListToolApprovalsBySessionToolCalls(ctx, sqlc.ListToolApprovalsBySessionToolCallsParams{
		BotID:       pgBotID,
		SessionID:   pgSessionID,
		ToolCallIds: toolCallIDs,
	})
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
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	var rows []sqlc.ToolApprovalRequest
	if pendingOnly {
		rows, err = s.queries.ListPendingToolApprovalsBySession(ctx, sqlc.ListPendingToolApprovalsBySessionParams{
			BotID:     pgBotID,
			SessionID: pgSessionID,
		})
	} else {
		rows, err = s.queries.ListToolApprovalsBySession(ctx, sqlc.ListToolApprovalsBySessionParams{
			BotID:     pgBotID,
			SessionID: pgSessionID,
		})
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

func requestFromRowOrErr(row sqlc.ToolApprovalRequest, err error) (Request, error) {
	if err != nil {
		return Request{}, mapLookupErr(err)
	}
	return requestFromRow(row), nil
}

func mapLookupErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func optionalUUID(value string) pgtype.UUID {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return pgtype.UUID{}
	}
	parsed, err := db.ParseUUID(trimmed)
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
}

func (s *Service) optionalChannelIdentityUUID(ctx context.Context, value string) (pgtype.UUID, error) {
	id := optionalUUID(value)
	if !id.Valid {
		return pgtype.UUID{}, nil
	}
	if s == nil || s.queries == nil {
		return pgtype.UUID{}, nil
	}
	if _, err := s.queries.GetChannelIdentityByID(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, nil
		}
		return pgtype.UUID{}, err
	}
	return id, nil
}

func requestFromRow(row sqlc.ToolApprovalRequest) Request {
	var input map[string]any
	_ = json.Unmarshal(row.ToolInput, &input)
	var options []PermissionOption
	_ = json.Unmarshal(row.Options, &options)
	req := Request{
		ID:                      uuid.UUID(row.ID.Bytes).String(),
		BotID:                   uuid.UUID(row.BotID.Bytes).String(),
		SessionID:               uuid.UUID(row.SessionID.Bytes).String(),
		WorkspaceTargetID:       strings.TrimSpace(row.WorkspaceTargetID),
		ToolCallID:              strings.TrimSpace(row.ToolCallID),
		ToolName:                strings.TrimSpace(row.ToolName),
		Operation:               strings.TrimSpace(row.Operation),
		ToolInput:               input,
		Options:                 options,
		SelectedOptionID:        row.SelectedOptionID,
		ShortID:                 int(row.ShortID),
		Status:                  strings.TrimSpace(row.Status),
		DecisionReason:          strings.TrimSpace(row.DecisionReason),
		PromptExternalMessageID: strings.TrimSpace(row.PromptExternalMessageID),
		SourcePlatform:          strings.TrimSpace(row.SourcePlatform),
		ReplyTarget:             strings.TrimSpace(row.ReplyTarget),
		ConversationType:        strings.TrimSpace(row.ConversationType),
		CreatedAt:               row.CreatedAt.Time,
		RuntimeFenced:           row.RuntimeFencingToken.Valid,
	}
	if req.Operation == "" {
		req.Operation, _ = OperationForTool(req.ToolName)
	}
	if row.RouteID.Valid {
		req.RouteID = uuid.UUID(row.RouteID.Bytes).String()
	}
	if row.ChannelIdentityID.Valid {
		req.ChannelIdentityID = uuid.UUID(row.ChannelIdentityID.Bytes).String()
	}
	if row.DecidedByChannelIdentityID.Valid {
		req.DecidedByUser = true
	}
	if row.DecidedAt.Valid {
		decided := row.DecidedAt.Time
		req.DecidedAt = &decided
	}
	return req
}
