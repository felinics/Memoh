package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type ContextTrajectoryEntry struct {
	ID            string    `json:"id"`
	RunID         string    `json:"run_id"`
	CaptureID     string    `json:"capture_id"`
	Sequence      int64     `json:"sequence"`
	Stage         string    `json:"stage"`
	StepIndex     *int      `json:"step_index,omitempty"`
	Request       int64     `json:"request,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
	CaptureErrors int64     `json:"capture_errors,omitempty"`
	BlockCount    int       `json:"block_count"`
}

type ContextTrajectoryResponse struct {
	Events     []ContextTrajectoryEntry `json:"events"`
	HasMore    bool                     `json:"has_more"`
	NextCursor string                   `json:"next_cursor,omitempty"`
}

type ContextTrajectoryBlock struct {
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	Format    string `json:"format,omitempty"`
	Encoding  string `json:"encoding,omitempty"`
	Hash      string `json:"hash"`
	Bytes     int    `json:"bytes"`
	Content   string `json:"content"`
	Available bool   `json:"available"`
}

type ContextTrajectoryEventResponse struct {
	Event    ContextTrajectoryEntry   `json:"event"`
	Blocks   []ContextTrajectoryBlock `json:"blocks"`
	Complete bool                     `json:"complete"`
}

// GetSessionContextTrajectory godoc
// @Summary List context assembly stages and provider requests
// @Description Read ordered, content-light capture metadata independently of conversation messages. Full captured content is available on demand with workspace_read. A capture_id distinguishes continuations that reuse a run_id; capture_errors reports preceding capture failures
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param before query string false "Continue before this event ID"
// @Param limit query int false "Page size (default 200, max 200)"
// @Success 200 {object} ContextTrajectoryResponse
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/context-trajectory [get].
func (h *SessionInfoHandler) GetSessionContextTrajectory(c echo.Context) error {
	access, err := h.authorizeContextLifecycleSession(c)
	if err != nil {
		return err
	}
	var before int64
	if raw := strings.TrimSpace(c.QueryParam("before")); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before <= 0 {
			return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
		}
	}
	limit := int32(200)
	if raw := c.QueryParam("limit"); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 32)
		if parseErr != nil || parsed <= 0 {
			return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
		}
		if parsed < 200 {
			limit = int32(parsed)
		}
	}
	rows, err := h.queries.ListContextTrajectoryEvents(c.Request().Context(), sqlc.ListContextTrajectoryEventsParams{
		BotID: access.botID, SessionID: access.sessionID, BeforeID: before, RowLimit: limit + 1,
	})
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	response := ContextTrajectoryResponse{Events: []ContextTrajectoryEntry{}, HasMore: len(rows) > int(limit)}
	if response.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		var event ContextTrajectoryEntry
		if err := json.Unmarshal(row.Summary, &event); err != nil {
			return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
		}
		event.ID, event.RunID, event.Sequence = strconv.FormatInt(row.ID, 10), row.RunID.String(), row.Sequence
		response.Events = append(response.Events, event)
	}
	if response.HasMore {
		response.NextCursor = response.Events[len(response.Events)-1].ID
	}
	return c.JSON(http.StatusOK, response)
}

// GetSessionContextTrajectoryEvent godoc
// @Summary Read the complete content of a context assembly stage
// @Description Reassemble full captured blocks in their original order, verifying byte counts and hashes. Requires workspace_read in addition to session access. Missing or corrupt blocks are explicitly unavailable; complete is false when any block cannot be restored. Non-UTF-8 bytes use base64 encoding
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param event_id path string true "Event ID"
// @Success 200 {object} ContextTrajectoryEventResponse
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/context-trajectory/{event_id} [get].
func (h *SessionInfoHandler) GetSessionContextTrajectoryEvent(c echo.Context) error {
	access, err := h.authorizeContextLifecycleSession(c)
	if err != nil {
		return err
	}
	if !access.canReadFragmentTexts() {
		return apperror.New(apperror.CodeContextLifecycleAccessDenied, nil)
	}
	id, err := strconv.ParseInt(c.Param("event_id"), 10, 64)
	if err != nil || id <= 0 {
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}
	ctx := c.Request().Context()
	raw, err := h.queries.GetContextTrajectoryEvent(ctx, sqlc.GetContextTrajectoryEventParams{BotID: access.botID, SessionID: access.sessionID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	}
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	var event trajectory.Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	contents, err := h.queries.GetContextTrajectoryEventContents(ctx, sqlc.GetContextTrajectoryEventContentsParams{BotID: access.botID, SessionID: access.sessionID, ID: id})
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	byHash := make(map[string][]byte, len(contents))
	for _, content := range contents {
		if trajectory.Hash(content.Content) == content.ContentHash {
			byHash[content.ContentHash] = content.Content
		}
	}
	response := ContextTrajectoryEventResponse{
		Event: ContextTrajectoryEntry{ID: strconv.FormatInt(id, 10), RunID: event.RunID, CaptureID: event.CaptureID,
			Sequence: event.Sequence, Stage: event.Stage, StepIndex: event.StepIndex, Request: event.Request,
			RecordedAt: event.RecordedAt, CaptureErrors: event.CaptureErrors, BlockCount: len(event.Blocks)},
		Blocks: []ContextTrajectoryBlock{}, Complete: true,
	}
	for _, ref := range event.Blocks {
		block := ContextTrajectoryBlock{Kind: ref.Kind, Label: ref.Label, Format: ref.Format, Hash: ref.Hash, Bytes: ref.Bytes, Available: true}
		var body bytes.Buffer
		for _, hash := range ref.Chunks {
			data, ok := byHash[hash]
			if !ok {
				block.Available = false
				break
			}
			_, _ = body.Write(data)
		}
		block.Available = block.Available && body.Len() == ref.Bytes && trajectory.Hash(body.Bytes()) == ref.Hash
		if block.Available {
			block.Content = body.String()
			if !utf8.Valid(body.Bytes()) {
				block.Encoding, block.Content = "base64", base64.StdEncoding.EncodeToString(body.Bytes())
			}
		} else {
			response.Complete = false
		}
		response.Blocks = append(response.Blocks, block)
	}
	return c.JSON(http.StatusOK, response)
}
