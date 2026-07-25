package input

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

type fakeInputPersistence struct {
	queries *fakeUserInputQueries
}

func newFakeInputPersistence(queries *fakeUserInputQueries) *fakeInputPersistence {
	return &fakeInputPersistence{queries: queries}
}

func (*fakeInputPersistence) ChannelIdentityExists(context.Context, string) (bool, error) {
	return false, nil
}

func (p *fakeInputPersistence) InInputCreateTransaction(_ context.Context, _, _ string, fn func(Store) error) error {
	return fn(p)
}

func (p *fakeInputPersistence) InInputFenceTransaction(_ context.Context, _, _ string, fn func(Store) error) error {
	return fn(p)
}

func (p *fakeInputPersistence) Create(ctx context.Context, in CreateRecordInput) (Record, error) {
	row, err := p.queries.CreateUserInputRequest(ctx, agentsqlc.CreateUserInputRequestParams{
		BotID: testPGUUID(in.BotID), SessionID: testPGUUID(in.SessionID),
		RouteID: testPGUUID(in.RouteID), ChannelIdentityID: testPGUUID(in.ChannelIdentityID),
		WorkspaceTargetID: in.WorkspaceTargetID, ToolCallID: in.ToolCallID, ToolName: in.ToolName,
		RuntimeFencingToken: testPGInt8(in.RuntimeFencingToken), InputJson: in.InputJSON,
		UiPayloadJson: in.UIPayloadJSON, ProviderMetadata: in.ProviderMetadata,
		RequestedByChannelIdentityID: testPGUUID(in.RequestedByChannelIdentityID),
		SourcePlatform:               in.SourcePlatform, ReplyTarget: in.ReplyTarget,
		ConversationType: in.ConversationType, ExpiresAt: testPGTime(in.ExpiresAt),
	})
	return testInputRecord(row), mapLegacyInputError(err)
}

func (p *fakeInputPersistence) Get(ctx context.Context, id string) (Record, error) {
	row, err := p.queries.GetUserInputRequest(ctx, testPGUUID(id))
	return testInputRecord(row), mapLegacyInputError(err)
}

func (p *fakeInputPersistence) GetRespondable(ctx context.Context, in ResolveRecordInput) (Record, error) {
	row, err := p.queries.GetRespondableUserInputRequest(ctx, agentsqlc.GetRespondableUserInputRequestParams{
		ID: testPGUUID(in.ID), RuntimeFencingToken: testPGInt8(in.RuntimeFencingToken),
	})
	return testInputRecord(row), mapLegacyInputError(err)
}

func (p *fakeInputPersistence) GetBySessionToolCall(ctx context.Context, sessionID, toolCallID string) (Record, error) {
	row, err := p.queries.GetUserInputRequestBySessionToolCall(ctx, agentsqlc.GetUserInputRequestBySessionToolCallParams{
		SessionID: testPGUUID(sessionID), ToolCallID: toolCallID,
	})
	return testInputRecord(row), mapLegacyInputError(err)
}

func (*fakeInputPersistence) GetPendingBySessionShortID(context.Context, string, string, int) (Record, error) {
	return Record{}, ErrNotFound
}

func (*fakeInputPersistence) GetPendingByReplyMessage(context.Context, string, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (p *fakeInputPersistence) GetLatestPendingBySession(ctx context.Context, botID, sessionID string) (Record, error) {
	row, err := p.queries.GetLatestPendingUserInputBySession(ctx, agentsqlc.GetLatestPendingUserInputBySessionParams{
		BotID: testPGUUID(botID), SessionID: testPGUUID(sessionID),
	})
	return testInputRecord(row), mapLegacyInputError(err)
}

func (p *fakeInputPersistence) UpdateInteraction(ctx context.Context, in InteractionRecordInput) (Record, error) {
	row, err := p.queries.UpdateUserInputInteraction(ctx, agentsqlc.UpdateUserInputInteractionParams{
		ID: testPGUUID(in.ID), InteractionJson: in.InteractionJSON, InteractionRevision: int32(in.InteractionRevision),
	})
	return testInputRecord(row), mapLegacyInputError(err)
}

func (p *fakeInputPersistence) Submit(ctx context.Context, in ResultRecordInput) (Record, error) {
	row, err := p.queries.SubmitUserInputRequest(ctx, agentsqlc.SubmitUserInputRequestParams{
		ID: testPGUUID(in.ID), ResultJson: in.ResultJSON,
		RespondedByChannelIdentityID: testPGUUID(in.RespondedByChannelIdentityID),
		RuntimeFencingToken:          testPGInt8(in.RuntimeFencingToken),
	})
	return testInputRecord(row), mapLegacyInputError(err)
}

func (p *fakeInputPersistence) Cancel(ctx context.Context, in ResultRecordInput) (Record, error) {
	row, err := p.queries.CancelUserInputRequest(ctx, agentsqlc.CancelUserInputRequestParams{
		ID: testPGUUID(in.ID), ResultJson: in.ResultJSON,
		RespondedByChannelIdentityID: testPGUUID(in.RespondedByChannelIdentityID),
		RuntimeFencingToken:          testPGInt8(in.RuntimeFencingToken),
	})
	return testInputRecord(row), mapLegacyInputError(err)
}

func (*fakeInputPersistence) CancelPendingBySession(context.Context, CancelSessionInput) ([]Record, error) {
	return nil, nil
}

func (*fakeInputPersistence) Fail(context.Context, ResultRecordInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*fakeInputPersistence) UpdatePrompt(context.Context, UpdatePromptInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*fakeInputPersistence) UpdateAssistantMessage(context.Context, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*fakeInputPersistence) UpdateToolResultMessage(context.Context, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (p *fakeInputPersistence) ListPendingBySession(ctx context.Context, botID, sessionID string) ([]Record, error) {
	rows, err := p.queries.ListPendingUserInputsBySession(ctx, agentsqlc.ListPendingUserInputsBySessionParams{
		BotID: testPGUUID(botID), SessionID: testPGUUID(sessionID),
	})
	result := make([]Record, 0, len(rows))
	for _, row := range rows {
		result = append(result, testInputRecord(row))
	}
	return result, err
}

func (*fakeInputPersistence) ListBySession(context.Context, string, string) ([]Record, error) {
	return nil, nil
}

func (*fakeInputPersistence) ListBySessionToolCalls(context.Context, string, string, []string) ([]Record, error) {
	return nil, nil
}

func testPGUUID(value string) pgtype.UUID {
	id, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}

func testPGInt8(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func testPGTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func testTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func testInputRecord(row agentsqlc.AgentUserInputRequest) Record {
	var token *int64
	if row.RuntimeFencingToken.Valid {
		value := row.RuntimeFencingToken.Int64
		token = &value
	}
	return Record{
		ID: uuid.UUID(row.ID.Bytes).String(), BotID: uuid.UUID(row.BotID.Bytes).String(),
		SessionID: uuid.UUID(row.SessionID.Bytes).String(), RouteID: testUUIDString(row.RouteID),
		ChannelIdentityID: testUUIDString(row.ChannelIdentityID), WorkspaceTargetID: row.WorkspaceTargetID,
		ToolCallID: row.ToolCallID, ToolName: row.ToolName, ShortID: int(row.ShortID), Status: row.Status,
		RuntimeFencingToken: token, InputJSON: row.InputJson, UIPayloadJSON: row.UiPayloadJson,
		InteractionJSON: row.InteractionJson, InteractionRevision: int(row.InteractionRevision),
		ResultJSON: row.ResultJson, ProviderMetadata: row.ProviderMetadata,
		PromptExternalMessageID: row.PromptExternalMessageID, SourcePlatform: row.SourcePlatform,
		ReplyTarget: row.ReplyTarget, ConversationType: row.ConversationType,
		ExpiresAt: testTime(row.ExpiresAt), CreatedAt: row.CreatedAt.Time,
		RespondedAt: testTime(row.RespondedAt), CanceledAt: testTime(row.CanceledAt),
	}
}

func testUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func mapLegacyInputError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
