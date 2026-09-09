package postgresstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (q *Queries) BindHistoryTurnAssistantByRequest(ctx context.Context, arg dbsqlc.BindHistoryTurnAssistantByRequestParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.BindHistoryTurnAssistantByRequest(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) BindLatestHistoryTurnAssistant(ctx context.Context, arg dbsqlc.BindLatestHistoryTurnAssistantParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.BindLatestHistoryTurnAssistant(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) CreateHistoryTurn(ctx context.Context, arg dbsqlc.CreateHistoryTurnParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.CreateHistoryTurn(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) CreateHistoryTurnWithID(ctx context.Context, arg dbsqlc.CreateHistoryTurnWithIDParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.CreateHistoryTurnWithID(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) CreateHistoryTurnWithIDAtPosition(ctx context.Context, arg dbsqlc.CreateHistoryTurnWithIDAtPositionParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.CreateHistoryTurnWithIDAtPosition(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) GetHistoryTurnByID(ctx context.Context, arg dbsqlc.GetHistoryTurnByIDParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.GetHistoryTurnByID(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) GetLatestVisibleHistoryTurnBySession(ctx context.Context, sessionID pgtype.UUID) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.GetLatestVisibleHistoryTurnBySession(ctx, sessionID)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) GetVisibleHistoryTurnByMessage(ctx context.Context, arg dbsqlc.GetVisibleHistoryTurnByMessageParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.GetVisibleHistoryTurnByMessage(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func (q *Queries) ListHistoryTurnsByBot(ctx context.Context, botID pgtype.UUID) ([]dbstore.HistoryTurn, error) {
	rows, err := q.Queries.ListHistoryTurnsByBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	result := make([]dbstore.HistoryTurn, 0, len(rows))
	for _, row := range rows {
		result = append(result, historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt))
	}
	return result, nil
}

func (q *Queries) SupersedeHistoryTurn(ctx context.Context, arg dbsqlc.SupersedeHistoryTurnParams) (dbstore.HistoryTurn, error) {
	row, err := q.Queries.SupersedeHistoryTurn(ctx, arg)
	if err != nil {
		return dbstore.HistoryTurn{}, err
	}
	return historyTurnFromFields(row.ID, row.BotID, row.SessionID, row.Position, row.RequestMessageID, row.AssistantMessageID, row.SupersededByTurnID, row.SupersededAt, row.SupersededReason, row.CreatedAt, row.UpdatedAt), nil
}

func historyTurnFromFields(
	id pgtype.UUID,
	botID pgtype.UUID,
	sessionID pgtype.UUID,
	position int64,
	requestMessageID pgtype.UUID,
	assistantMessageID pgtype.UUID,
	supersededByTurnID pgtype.UUID,
	supersededAt pgtype.Timestamptz,
	supersededReason any,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) dbstore.HistoryTurn {
	return dbstore.HistoryTurn{
		ID:                 id,
		BotID:              botID,
		SessionID:          sessionID,
		Position:           position,
		RequestMessageID:   requestMessageID,
		AssistantMessageID: assistantMessageID,
		SupersededByTurnID: supersededByTurnID,
		SupersededAt:       supersededAt,
		SupersededReason:   historyTurnReasonString(supersededReason),
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
	}
}

func historyTurnReasonString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case pgtype.Text:
		if v.Valid {
			return v.String
		}
	}
	return ""
}
