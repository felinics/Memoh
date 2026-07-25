package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
	"github.com/memohai/memoh/domains/agent/decision/input"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

func (s *inputStore) InInputCreateTransaction(ctx context.Context, botID, sessionID string, fn func(input.Store) error) error {
	if fn == nil {
		return errors.New("input create callback is required")
	}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) error {
		store := &inputStore{
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

func (s *inputStore) InInputFenceTransaction(ctx context.Context, botID, sessionID string, fn func(input.Store) error) error {
	if fn == nil {
		return errors.New("input fence callback is required")
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
		store := &inputStore{
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

func (s *inputStore) ChannelIdentityExists(ctx context.Context, id string) (bool, error) {
	return channelIdentityExists(ctx, s.identities, id)
}

func (s *inputStore) Create(ctx context.Context, in input.CreateRecordInput) (input.Record, error) {
	bot, session, err := inputSessionIDs(in.BotID, in.SessionID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.CreateUserInputRequest(ctx, agentsqlc.CreateUserInputRequestParams{
		BotID:                        bot,
		SessionID:                    session,
		RouteID:                      optionalUUID(in.RouteID),
		ChannelIdentityID:            optionalUUID(in.ChannelIdentityID),
		WorkspaceTargetID:            in.WorkspaceTargetID,
		ToolCallID:                   in.ToolCallID,
		ToolName:                     in.ToolName,
		RuntimeFencingToken:          optionalInt64(in.RuntimeFencingToken),
		InputJson:                    in.InputJSON,
		UiPayloadJson:                in.UIPayloadJSON,
		ProviderMetadata:             in.ProviderMetadata,
		RequestedByChannelIdentityID: optionalUUID(in.RequestedByChannelIdentityID),
		SourcePlatform:               in.SourcePlatform,
		ReplyTarget:                  in.ReplyTarget,
		ConversationType:             in.ConversationType,
		ExpiresAt:                    optionalTime(in.ExpiresAt),
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) Get(ctx context.Context, id string) (input.Record, error) {
	pgID, err := parseUUID(id)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.GetUserInputRequest(ctx, pgID)
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) GetRespondable(ctx context.Context, in input.ResolveRecordInput) (input.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.GetRespondableUserInputRequest(ctx, agentsqlc.GetRespondableUserInputRequestParams{
		ID: id, RuntimeFencingToken: optionalInt64(in.RuntimeFencingToken),
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) GetBySessionToolCall(ctx context.Context, sessionID, toolCallID string) (input.Record, error) {
	session, err := parseUUID(sessionID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.GetUserInputRequestBySessionToolCall(ctx, agentsqlc.GetUserInputRequestBySessionToolCallParams{
		SessionID: session, ToolCallID: toolCallID,
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) GetPendingBySessionShortID(ctx context.Context, botID, sessionID string, shortID int) (input.Record, error) {
	bot, session, err := inputSessionIDs(botID, sessionID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.GetPendingUserInputBySessionShortID(ctx, agentsqlc.GetPendingUserInputBySessionShortIDParams{
		BotID: bot, SessionID: session, ShortID: int32(shortID), //nolint:gosec
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) GetPendingByReplyMessage(ctx context.Context, botID, sessionID, messageID string) (input.Record, error) {
	bot, session, err := inputSessionIDs(botID, sessionID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.GetPendingUserInputByReplyMessage(ctx, agentsqlc.GetPendingUserInputByReplyMessageParams{
		BotID: bot, SessionID: session, PromptExternalMessageID: messageID,
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) GetLatestPendingBySession(ctx context.Context, botID, sessionID string) (input.Record, error) {
	bot, session, err := inputSessionIDs(botID, sessionID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.GetLatestPendingUserInputBySession(ctx, agentsqlc.GetLatestPendingUserInputBySessionParams{BotID: bot, SessionID: session})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) UpdateInteraction(ctx context.Context, in input.InteractionRecordInput) (input.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.UpdateUserInputInteraction(ctx, agentsqlc.UpdateUserInputInteractionParams{
		ID: id, InteractionJson: in.InteractionJSON, InteractionRevision: int32(in.InteractionRevision), //nolint:gosec
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) Submit(ctx context.Context, in input.ResultRecordInput) (input.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.SubmitUserInputRequest(ctx, agentsqlc.SubmitUserInputRequestParams{
		ID: id, ResultJson: in.ResultJSON,
		RespondedByChannelIdentityID: optionalUUID(in.RespondedByChannelIdentityID),
		RuntimeFencingToken:          optionalInt64(in.RuntimeFencingToken),
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) Cancel(ctx context.Context, in input.ResultRecordInput) (input.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.CancelUserInputRequest(ctx, agentsqlc.CancelUserInputRequestParams{
		ID: id, ResultJson: in.ResultJSON,
		RespondedByChannelIdentityID: optionalUUID(in.RespondedByChannelIdentityID),
		RuntimeFencingToken:          optionalInt64(in.RuntimeFencingToken),
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) CancelPendingBySession(ctx context.Context, in input.CancelSessionInput) ([]input.Record, error) {
	bot, session, err := inputSessionIDs(in.BotID, in.SessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.CancelPendingUserInputsBySession(ctx, agentsqlc.CancelPendingUserInputsBySessionParams{
		BotID: bot, SessionID: session, ResultJson: in.ResultJSON,
		RuntimeFencingToken: optionalInt64(in.RuntimeFencingToken),
	})
	return inputRecords(rows), err
}

func (s *inputStore) Fail(ctx context.Context, in input.ResultRecordInput) (input.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.FailUserInputRequest(ctx, agentsqlc.FailUserInputRequestParams{
		ID: id, ResultJson: in.ResultJSON, RuntimeFencingToken: optionalInt64(in.RuntimeFencingToken),
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) UpdatePrompt(ctx context.Context, in input.UpdatePromptInput) (input.Record, error) {
	id, err := parseUUID(in.ID)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.UpdateUserInputPromptMessage(ctx, agentsqlc.UpdateUserInputPromptMessageParams{
		ID: id, PromptMessageID: optionalUUID(in.PromptMessageID), PromptExternalMessageID: in.PromptExternalMessageID,
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) UpdateAssistantMessage(ctx context.Context, id, messageID string) (input.Record, error) {
	pgID, err := parseUUID(id)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.UpdateUserInputAssistantMessage(ctx, agentsqlc.UpdateUserInputAssistantMessageParams{
		ID: pgID, AssistantMessageID: optionalUUID(messageID),
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) UpdateToolResultMessage(ctx context.Context, id, messageID string) (input.Record, error) {
	pgID, err := parseUUID(id)
	if err != nil {
		return input.Record{}, err
	}
	row, err := s.queries.UpdateUserInputToolResultMessage(ctx, agentsqlc.UpdateUserInputToolResultMessageParams{
		ID: pgID, ToolResultMessageID: optionalUUID(messageID),
	})
	return inputRecord(row), mapInputError(err)
}

func (s *inputStore) ListPendingBySession(ctx context.Context, botID, sessionID string) ([]input.Record, error) {
	bot, session, err := inputSessionIDs(botID, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListPendingUserInputsBySession(ctx, agentsqlc.ListPendingUserInputsBySessionParams{BotID: bot, SessionID: session})
	return inputRecords(rows), err
}

func (s *inputStore) ListBySession(ctx context.Context, botID, sessionID string) ([]input.Record, error) {
	bot, session, err := inputSessionIDs(botID, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUserInputsBySession(ctx, agentsqlc.ListUserInputsBySessionParams{BotID: bot, SessionID: session})
	return inputRecords(rows), err
}

func (s *inputStore) ListBySessionToolCalls(ctx context.Context, botID, sessionID string, toolCallIDs []string) ([]input.Record, error) {
	bot, session, err := inputSessionIDs(botID, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListUserInputsBySessionToolCalls(ctx, agentsqlc.ListUserInputsBySessionToolCallsParams{
		BotID: bot, SessionID: session, ToolCallIds: toolCallIDs,
	})
	return inputRecords(rows), err
}

func inputSessionIDs(botID, sessionID string) (pgtype.UUID, pgtype.UUID, error) {
	bot, err := parseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	session, err := parseUUID(sessionID)
	return bot, session, err
}

func inputRecords(rows []agentsqlc.AgentUserInputRequest) []input.Record {
	result := make([]input.Record, 0, len(rows))
	for _, row := range rows {
		result = append(result, inputRecord(row))
	}
	return result
}

func inputRecord(row agentsqlc.AgentUserInputRequest) input.Record {
	return input.Record{
		ID:                           uuidString(row.ID),
		BotID:                        uuidString(row.BotID),
		SessionID:                    uuidString(row.SessionID),
		RouteID:                      uuidString(row.RouteID),
		ChannelIdentityID:            uuidString(row.ChannelIdentityID),
		WorkspaceTargetID:            row.WorkspaceTargetID,
		ToolCallID:                   row.ToolCallID,
		ToolName:                     row.ToolName,
		ShortID:                      int(row.ShortID),
		Status:                       row.Status,
		RuntimeFencingToken:          int64Pointer(row.RuntimeFencingToken),
		InputJSON:                    row.InputJson,
		UIPayloadJSON:                row.UiPayloadJson,
		InteractionJSON:              row.InteractionJson,
		InteractionRevision:          int(row.InteractionRevision),
		ResultJSON:                   row.ResultJson,
		ProviderMetadata:             row.ProviderMetadata,
		RequestedByChannelIdentityID: uuidString(row.RequestedByChannelIdentityID),
		RespondedByChannelIdentityID: uuidString(row.RespondedByChannelIdentityID),
		AssistantMessageID:           uuidString(row.AssistantMessageID),
		ToolResultMessageID:          uuidString(row.ToolResultMessageID),
		PromptMessageID:              uuidString(row.PromptMessageID),
		PromptExternalMessageID:      row.PromptExternalMessageID,
		SourcePlatform:               row.SourcePlatform,
		ReplyTarget:                  row.ReplyTarget,
		ConversationType:             row.ConversationType,
		ExpiresAt:                    timePointer(row.ExpiresAt),
		CreatedAt:                    row.CreatedAt.Time,
		RespondedAt:                  timePointer(row.RespondedAt),
		CanceledAt:                   timePointer(row.CanceledAt),
		UpdatedAt:                    row.UpdatedAt.Time,
	}
}
