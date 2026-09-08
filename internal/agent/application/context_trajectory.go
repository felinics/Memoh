package application

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/hooks"
)

type contextTrajectoryQueries interface {
	AppendContextTrajectoryEvent(context.Context, sqlc.AppendContextTrajectoryEventParams) (int64, error)
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
	queries contextTrajectoryQueries
	botID   pgtype.UUID
}

func (s contextTrajectorySink) Append(ctx context.Context, event trajectory.Event, contents []trajectory.Content) error {
	runID, err := db.ParseUUID(event.RunID)
	if err != nil {
		return err
	}
	sessionID, err := db.ParseUUID(event.SessionID)
	if err != nil {
		return err
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
		Sequence: event.Sequence, Event: raw,
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
