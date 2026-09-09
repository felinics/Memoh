package application

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/runtimefence"
)

type contextTrajectoryQueries interface {
	AppendContextTrajectoryEvent(context.Context, sqlc.AppendContextTrajectoryEventParams) (int64, error)
	GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error)
}

func recordContextStage(ctx context.Context, stage string, value any) {
	if recorder := trajectory.FromContext(ctx); recorder != nil {
		recorder.Record(ctx, stage, nil, trajectory.JSONBlock("context", stage, value))
	}
}

func recordChatTrigger(ctx context.Context, req ChatRequest) {
	recordContextStage(ctx, "trigger", map[string]any{
		"query": req.Query, "raw_query": req.RawQuery, "model_query": req.ModelQuery,
		"messages": req.Messages, "attachments": req.Attachments, "reply_attachments": req.ReplyAttachments,
		"requested_skills": req.RequestedSkills, "session_type": req.SessionType,
		"channel": req.CurrentChannel, "external_message_id": req.ExternalMessageID,
		"reused_user_message": req.ReusePersistedUserMessage,
	})
}

func recordHookContextStage(ctx context.Context, stage string, result hooks.Result) {
	recordContextStage(ctx, stage, map[string]any{
		"append_context": result.AppendContext, "append_system_sections": result.AppendSystemSections,
		"decision": result.Decision, "warnings": result.Warnings,
	})
}

type contextTrajectorySink struct {
	queries   contextTrajectoryQueries
	botID     pgtype.UUID
	sessionID pgtype.UUID
	token     int64
	scopeErr  error
}

func newContextTrajectorySink(ctx context.Context, queries contextTrajectoryQueries, botID pgtype.UUID, sessionID string) (sink contextTrajectorySink) {
	sink = contextTrajectorySink{queries: queries, botID: botID}
	defer func() {
		if recovered := recover(); recovered != nil {
			sink.scopeErr = fmt.Errorf("context trajectory binding failed: %v", recovered)
		}
	}()
	capability, ok := queries.(interface{ SupportsTransactions() bool })
	if !ok || !capability.SupportsTransactions() {
		sink.scopeErr = runtimefence.ErrTransactionsUnsupported
		return sink
	}
	sink.sessionID, sink.scopeErr = db.ParseUUID(sessionID)
	if sink.scopeErr != nil {
		return sink
	}
	if fence, ok := runtimefence.FromContext(ctx); ok {
		sink.scopeErr = runtimefence.ValidateScope(ctx, uuid.UUID(botID.Bytes).String(), sessionID)
		sink.token = fence.Token
		return sink
	}
	session, err := queries.GetSessionByID(ctx, sink.sessionID)
	sink.scopeErr = err
	if err == nil {
		if session.BotID != botID || session.DeletedAt.Valid {
			sink.scopeErr = runtimefence.ErrStale
		}
		sink.token = session.RuntimeFencingToken
	}
	return sink
}

func (s contextTrajectorySink) Append(ctx context.Context, event trajectory.Event, contents []trajectory.Content) error {
	if s.scopeErr != nil {
		return s.scopeErr
	}
	if fence, ok := runtimefence.FromContext(ctx); ok {
		if err := runtimefence.ValidateScope(ctx, uuid.UUID(s.botID.Bytes).String(), uuid.UUID(s.sessionID.Bytes).String()); err != nil {
			return err
		}
		if fence.Token != s.token {
			return runtimefence.ErrStale
		}
	}
	runID, err := db.ParseUUID(event.RunID)
	if err != nil {
		return err
	}
	sessionID, err := db.ParseUUID(event.SessionID)
	if err != nil {
		return err
	}
	if sessionID != s.sessionID {
		return runtimefence.ErrStale
	}
	captureID, err := db.ParseUUID(event.CaptureID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	params := sqlc.AppendContextTrajectoryEventParams{
		BotID: s.botID, SessionID: sessionID, RunID: runID, CaptureID: captureID,
		Sequence: event.Sequence, Event: raw, RuntimeFencingToken: s.token,
	}
	for _, content := range contents {
		params.ContentHashes = append(params.ContentHashes, content.Hash)
		params.Contents = append(params.Contents, content.Data)
	}
	sequence, err := s.queries.AppendContextTrajectoryEvent(ctx, params)
	if err != nil {
		return err
	}
	if sequence != event.Sequence {
		return fmt.Errorf("context trajectory sequence mismatch: %d != %d", sequence, event.Sequence)
	}
	return nil
}
