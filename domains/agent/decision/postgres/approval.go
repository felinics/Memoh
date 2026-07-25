package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
	"github.com/memohai/memoh/domains/agent/decision/approval"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

func (s *approvalStore) InApprovalCreateTransaction(ctx context.Context, botID, sessionID string, fn func(approval.Store) error) error {
	if fn == nil {
		return errors.New("approval create callback is required")
	}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) error {
		store := &approvalStore{
			queries:    s.queries.WithTx(tx),
			lock:       s.newLock(tx),
			identities: s.identities,
		}
		if err := lockBot(ctx, store.lock, botID); err != nil {
			return err
		}
		if fence, ok := runtimefence.FromContext(ctx); ok {
			if err := runtimefence.ValidateScope(ctx, botID, sessionID); err != nil {
				return err
			}
			if err := lockFence(ctx, store.queries, fence); err != nil {
				return err
			}
		}
		if err := lockDecisionSequence(ctx, store.queries, botID, sessionID); err != nil {
			return err
		}
		return fn(store)
	})
}

func (s *approvalStore) InApprovalFenceTransaction(ctx context.Context, botID, sessionID string, fn func(approval.Store) error) error {
	if fn == nil {
		return errors.New("approval fence callback is required")
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok {
		return errors.New("runtime persistence fence is missing")
	}
	if strings.TrimSpace(botID) == "" {
		botID = fence.BotID
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = fence.SessionID
	}
	if err := runtimefence.ValidateScope(ctx, botID, sessionID); err != nil {
		return err
	}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) error {
		store := &approvalStore{
			queries:    s.queries.WithTx(tx),
			lock:       s.newLock(tx),
			identities: s.identities,
		}
		if err := lockBot(ctx, store.lock, botID); err != nil {
			return err
		}
		if err := lockFence(ctx, store.queries, fence); err != nil {
			return err
		}
		return fn(store)
	})
}

func (s *approvalStore) ChannelIdentityExists(ctx context.Context, id string) (bool, error) {
	return channelIdentityExists(ctx, s.identities, id)
}

func (s *approvalStore) Create(ctx context.Context, in approval.CreateRecordInput) (approval.Record, error) {
	botID, err := parseUUID(in.BotID)
	if err != nil {
		return approval.Record{}, err
	}
	sessionID, err := parseUUID(in.SessionID)
	if err != nil {
		return approval.Record{}, err
	}
	row, err := s.queries.CreateToolApprovalRequest(ctx, agentsqlc.CreateToolApprovalRequestParams{
		BotID:                        botID,
		SessionID:                    sessionID,
		RouteID:                      optionalUUID(in.RouteID),
		ChannelIdentityID:            optionalUUID(in.ChannelIdentityID),
		WorkspaceTargetID:            in.WorkspaceTargetID,
		ToolCallID:                   in.ToolCallID,
		ToolName:                     in.ToolName,
		Operation:                    in.Operation,
		ToolInput:                    in.ToolInput,
		RuntimeFencingToken:          optionalInt64(in.RuntimeFencingToken),
		RequestedByChannelIdentityID: optionalUUID(in.RequestedByChannelIdentityID),
		RequestedMessageID:           optionalUUID(in.RequestedMessageID),
		SourcePlatform:               in.SourcePlatform,
		ReplyTarget:                  in.ReplyTarget,
		ConversationType:             in.ConversationType,
	})
	return approvalRecord(row), mapApprovalError(err)
}

func (s *approvalStore) Get(ctx context.Context, id string) (approval.Record, error) {
	pgID, err := parseUUID(id)
	if err != nil {
		return approval.Record{}, err
	}
	row, err := s.queries.GetToolApprovalRequest(ctx, pgID)
	return approvalRecord(row), mapApprovalError(err)
}

func (s *approvalStore) GetPendingBySessionShortID(ctx context.Context, botID, sessionID string, shortID int) (approval.Record, error) {
	bot, err := parseUUID(botID)
	if err != nil {
		return approval.Record{}, err
	}
	session, err := parseUUID(sessionID)
	if err != nil {
		return approval.Record{}, err
	}
	row, err := s.queries.GetPendingToolApprovalBySessionShortID(ctx, agentsqlc.GetPendingToolApprovalBySessionShortIDParams{
		BotID: bot, SessionID: session, ShortID: int32(shortID), //nolint:gosec
	})
	return approvalRecord(row), mapApprovalError(err)
}

func (s *approvalStore) GetPendingByReplyMessage(ctx context.Context, botID, sessionID, messageID string) (approval.Record, error) {
	bot, err := parseUUID(botID)
	if err != nil {
		return approval.Record{}, err
	}
	session, err := parseUUID(sessionID)
	if err != nil {
		return approval.Record{}, err
	}
	row, err := s.queries.GetPendingToolApprovalByReplyMessage(ctx, agentsqlc.GetPendingToolApprovalByReplyMessageParams{
		BotID: bot, SessionID: session, PromptExternalMessageID: messageID,
	})
	return approvalRecord(row), mapApprovalError(err)
}

func (s *approvalStore) GetLatestPendingBySession(ctx context.Context, botID, sessionID string) (approval.Record, error) {
	bot, err := parseUUID(botID)
	if err != nil {
		return approval.Record{}, err
	}
	session, err := parseUUID(sessionID)
	if err != nil {
		return approval.Record{}, err
	}
	row, err := s.queries.GetLatestPendingToolApprovalBySession(ctx, agentsqlc.GetLatestPendingToolApprovalBySessionParams{
		BotID: bot, SessionID: session,
	})
	return approvalRecord(row), mapApprovalError(err)
}

func (s *approvalStore) Approve(ctx context.Context, in approval.DecisionRecordInput) (approval.Record, error) {
	return s.decide(ctx, in, true)
}

func (s *approvalStore) Reject(ctx context.Context, in approval.DecisionRecordInput) (approval.Record, error) {
	return s.decide(ctx, in, false)
}

func (s *approvalStore) decide(ctx context.Context, in approval.DecisionRecordInput, approve bool) (approval.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return approval.Record{}, err
	}
	if approve {
		row, err := s.queries.ApproveToolApprovalRequest(ctx, agentsqlc.ApproveToolApprovalRequestParams{
			ID: id, Reason: in.Reason, DecidedByChannelIdentityID: optionalUUID(in.DecidedByChannelIdentityID),
			RuntimeFencingToken: optionalInt64(in.RuntimeFencingToken),
		})
		return approvalRecord(row), mapApprovalError(err)
	}
	row, err := s.queries.RejectToolApprovalRequest(ctx, agentsqlc.RejectToolApprovalRequestParams{
		ID: id, Reason: in.Reason, DecidedByChannelIdentityID: optionalUUID(in.DecidedByChannelIdentityID),
		RuntimeFencingToken: optionalInt64(in.RuntimeFencingToken),
	})
	return approvalRecord(row), mapApprovalError(err)
}

func (s *approvalStore) CancelPendingBySession(ctx context.Context, in approval.CancelSessionInput) ([]approval.Record, error) {
	bot, err := parseUUID(in.BotID)
	if err != nil {
		return nil, err
	}
	session, err := parseUUID(in.SessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.CancelPendingToolApprovalsBySession(ctx, agentsqlc.CancelPendingToolApprovalsBySessionParams{
		BotID: bot, SessionID: session, Reason: in.Reason,
		RuntimeFencingToken: optionalInt64(in.RuntimeFencingToken),
	})
	return approvalRecords(rows), err
}

func (s *approvalStore) UpdatePrompt(ctx context.Context, in approval.UpdatePromptInput) (approval.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return approval.Record{}, err
	}
	row, err := s.queries.UpdateToolApprovalPromptMessage(ctx, agentsqlc.UpdateToolApprovalPromptMessageParams{
		ID: id, PromptMessageID: optionalUUID(in.PromptMessageID),
		PromptExternalMessageID: in.PromptExternalMessageID,
	})
	return approvalRecord(row), mapApprovalError(err)
}

func (s *approvalStore) ListPendingBySession(ctx context.Context, botID, sessionID string) ([]approval.Record, error) {
	bot, session, err := approvalSessionIDs(botID, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListPendingToolApprovalsBySession(ctx, agentsqlc.ListPendingToolApprovalsBySessionParams{BotID: bot, SessionID: session})
	return approvalRecords(rows), err
}

func (s *approvalStore) ListBySession(ctx context.Context, botID, sessionID string) ([]approval.Record, error) {
	bot, session, err := approvalSessionIDs(botID, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListToolApprovalsBySession(ctx, agentsqlc.ListToolApprovalsBySessionParams{BotID: bot, SessionID: session})
	return approvalRecords(rows), err
}

func (s *approvalStore) ListBySessionToolCalls(ctx context.Context, botID, sessionID string, toolCallIDs []string) ([]approval.Record, error) {
	bot, session, err := approvalSessionIDs(botID, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListToolApprovalsBySessionToolCalls(ctx, agentsqlc.ListToolApprovalsBySessionToolCallsParams{
		BotID: bot, SessionID: session, ToolCallIds: toolCallIDs,
	})
	return approvalRecords(rows), err
}

func approvalSessionIDs(botID, sessionID string) (pgtype.UUID, pgtype.UUID, error) {
	bot, err := parseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	session, err := parseUUID(sessionID)
	return bot, session, err
}

func approvalRecords(rows []agentsqlc.AgentToolApprovalRequest) []approval.Record {
	result := make([]approval.Record, 0, len(rows))
	for _, row := range rows {
		result = append(result, approvalRecord(row))
	}
	return result
}

func approvalRecord(row agentsqlc.AgentToolApprovalRequest) approval.Record {
	return approval.Record{
		ID:                           uuidString(row.ID),
		BotID:                        uuidString(row.BotID),
		SessionID:                    uuidString(row.SessionID),
		RouteID:                      uuidString(row.RouteID),
		ChannelIdentityID:            uuidString(row.ChannelIdentityID),
		WorkspaceTargetID:            row.WorkspaceTargetID,
		ToolCallID:                   row.ToolCallID,
		ToolName:                     row.ToolName,
		Operation:                    row.Operation,
		ToolInput:                    row.ToolInput,
		ShortID:                      int(row.ShortID),
		Status:                       row.Status,
		RuntimeFencingToken:          int64Pointer(row.RuntimeFencingToken),
		DecisionReason:               row.DecisionReason,
		RequestedByChannelIdentityID: uuidString(row.RequestedByChannelIdentityID),
		DecidedByChannelIdentityID:   uuidString(row.DecidedByChannelIdentityID),
		RequestedMessageID:           uuidString(row.RequestedMessageID),
		PromptMessageID:              uuidString(row.PromptMessageID),
		PromptExternalMessageID:      row.PromptExternalMessageID,
		SourcePlatform:               row.SourcePlatform,
		ReplyTarget:                  row.ReplyTarget,
		ConversationType:             row.ConversationType,
		CreatedAt:                    row.CreatedAt.Time,
		DecidedAt:                    timePointer(row.DecidedAt),
	}
}
