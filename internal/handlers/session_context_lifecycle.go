package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	contextLifecycleDefaultLimit = 50
	contextLifecycleMaxLimit     = 200
)

type ContextLifecycleResponse struct {
	Turns      []ContextLifecycleTurn     `json:"turns"`
	Aggregates ContextLifecycleAggregates `json:"aggregates"`
	// Limit is the page bound the turns and aggregates were computed over.
	Limit int `json:"limit"`
	// HasMore reports whether older lifecycle turns exist beyond this page.
	HasMore bool `json:"has_more"`
	// NextCursor is the opaque `before` value that continues past this page's
	// oldest run; absent when the page is complete or served from legacy rows.
	NextCursor string `json:"next_cursor,omitempty"`
	// LegacySource reports that turns were recovered from pre-run-table
	// assistant metadata instead of the run-keyed lifecycle table.
	LegacySource bool `json:"legacy_source,omitempty"`
	// LegacyHistoryMayExist reports that pre-run-table assistant metadata also
	// exists for this session while the run-keyed table served the page, so
	// this response does not cover the session's full history era.
	LegacyHistoryMayExist bool `json:"legacy_history_may_exist,omitempty"`
	// AggregateScope is always "returned_page": aggregates cover the returned
	// turns, never the whole session.
	AggregateScope string `json:"aggregate_scope"`
	// FragmentPreviews maps a text hash referenced by the page's fragment
	// refs and tool definitions to the head of its stored text. Present only
	// for callers who may read the bot's workspace, because the texts include
	// workspace files and hook output.
	FragmentPreviews map[string]ContextFragmentPreview `json:"fragment_previews,omitempty"`
}

// ContextFragmentPreview is the head of one stored fragment text.
type ContextFragmentPreview struct {
	Kind      contextfrag.Kind `json:"kind"`
	Label     string           `json:"label,omitempty"`
	Preview   string           `json:"preview"`
	TextBytes int              `json:"text_bytes"`
	Truncated bool             `json:"truncated,omitempty"`
}

const contextFragmentPreviewChars = 240

const contextLifecycleAggregateScope = "returned_page"

// ContextLifecycleTurn is one persisted lifecycle snapshot, newest first.
type ContextLifecycleTurn struct {
	RunID string `json:"run_id"`
	// TurnID is the durable turn the run wrote into, joined from the run
	// ledger; absent for runs the ledger never recorded.
	TurnID             string                        `json:"turn_id,omitempty"`
	Status             string                        `json:"status,omitempty"`
	ErrorCode          string                        `json:"error_code,omitempty"`
	AssistantMessageID string                        `json:"assistant_message_id,omitempty"`
	CreatedAt          time.Time                     `json:"created_at"`
	Snapshot           contextfrag.LifecycleSnapshot `json:"snapshot"`
}

// ContextLifecycleAggregates sums facts observed on the returned page at
// Memoh's own boundary: native runs report SDK/provider usage, while ACP runs
// expose only protocol-level input, so an ACP zero means "not observable
// here", not "measured zero". Derived cache-comparison ratios and tool-roster
// churn are intentionally absent until a durable comparator exists.
type ContextLifecycleAggregates struct {
	Turns                 int            `json:"turns"`
	TotalCacheReadTokens  int            `json:"total_cache_read_tokens"`
	TotalCacheWriteTokens int            `json:"total_cache_write_tokens"`
	DropReasons           map[string]int `json:"drop_reasons,omitempty"`
	MutationKinds         map[string]int `json:"mutation_kinds,omitempty"`
}

// GetSessionContextLifecycle godoc
// @Summary Get session context lifecycle
// @Description List run-keyed context lifecycle snapshots for a chat session, newest first, with page-scoped aggregate totals (cache read/write tokens, drop reasons, mutation kinds). Aggregates cover only the returned page; has_more reports older turns. Sessions predating run lifecycle persistence fall back to legacy assistant metadata (legacy_source). Per-fragment selection_decisions are never returned; each turn's selection trace carries their rolled-up counts and token costs
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param limit query int false "Maximum number of turns to return (default 50, max 200)"
// @Param before query string false "Opaque next_cursor from a previous page; returns run-keyed turns older than it"
// @Success 200 {object} ContextLifecycleResponse
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/context-lifecycle [get].
func (h *SessionInfoHandler) GetSessionContextLifecycle(c echo.Context) error {
	access, err := h.authorizeContextLifecycleSession(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()

	limit := contextLifecycleLimit(c)
	var before *contextLifecycleCursor
	if raw := strings.TrimSpace(c.QueryParam("before")); raw != "" {
		cursor, ok := decodeContextLifecycleCursor(raw)
		if !ok {
			return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
		}
		before = &cursor
	}
	load, err := loadContextLifecycleTurns(ctx, h.queries, access.sessionID, limit, before)
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	var previews map[string]ContextFragmentPreview
	if access.canReadFragmentTexts() {
		previews, err = contextFragmentPreviews(ctx, h.queries, access.botID, load.Turns)
		if err != nil {
			return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
		}
	}
	return c.JSON(http.StatusOK, ContextLifecycleResponse{
		Turns:                 load.Turns,
		Aggregates:            aggregateContextLifecycle(load.Turns),
		Limit:                 limit,
		HasMore:               load.HasMore,
		NextCursor:            load.NextCursor,
		LegacySource:          load.LegacySource,
		LegacyHistoryMayExist: load.LegacyHistoryMayExist,
		AggregateScope:        contextLifecycleAggregateScope,
		FragmentPreviews:      previews,
	})
}

// contextFragmentHashes collects every stored-text hash a page of turns
// references, deduplicated, in first-seen order. A fragment ref without a
// text hash stored no text, so it is not looked up.
func contextFragmentHashes(turns []ContextLifecycleTurn) []string {
	seen := make(map[string]struct{})
	var hashes []string
	add := func(hash string) {
		hash = strings.TrimSpace(hash)
		if hash == "" {
			return
		}
		if _, ok := seen[hash]; ok {
			return
		}
		seen[hash] = struct{}{}
		hashes = append(hashes, hash)
	}
	for _, turn := range turns {
		for _, ref := range turn.Snapshot.Fragments {
			add(ref.TextHash)
		}
		for _, def := range turn.Snapshot.ToolDefs {
			add(def.ContentHash)
		}
	}
	return hashes
}

func contextFragmentPreviews(ctx context.Context, queries contextLifecycleQueries, botID pgtype.UUID, turns []ContextLifecycleTurn) (map[string]ContextFragmentPreview, error) {
	hashes := contextFragmentHashes(turns)
	if len(hashes) == 0 {
		return nil, nil
	}
	rows, err := queries.ListContextFragmentPreviews(ctx, sqlc.ListContextFragmentPreviewsParams{
		PreviewChars:  contextFragmentPreviewChars,
		BotID:         botID,
		ContentHashes: hashes,
	})
	if err != nil {
		return nil, fmt.Errorf("list context fragment previews: %w", err)
	}
	previews := make(map[string]ContextFragmentPreview, len(rows))
	for _, row := range rows {
		previews[row.ContentHash] = ContextFragmentPreview{
			Kind:      contextfrag.Kind(row.Kind),
			Label:     row.Label,
			Preview:   row.Preview,
			TextBytes: int(row.TextBytes),
			Truncated: row.Truncated,
		}
	}
	return previews, nil
}

// ContextFragmentText is one injected fragment of a run with its stored text.
type ContextFragmentText struct {
	// Label names the fragment as the assembler did; empty when no text was
	// stored, because the snapshot itself never carries names.
	Label       string           `json:"label,omitempty"`
	Kind        contextfrag.Kind `json:"kind"`
	Slot        contextfrag.Slot `json:"slot,omitempty"`
	ContentHash string           `json:"content_hash,omitempty"`
	// TextHash is the store key of the fragment's text; tool definitions use
	// their serialized hash for both.
	TextHash      string `json:"text_hash,omitempty"`
	TokenEstimate int    `json:"token_estimate,omitempty"`
	TextBytes     int    `json:"text_bytes,omitempty"`
	Text          string `json:"text"`
	Truncated     bool   `json:"truncated,omitempty"`
	// Available is false when the text was never stored for this fragment,
	// such as runs older than the text store.
	Available bool `json:"available"`
}

type ContextLifecycleFragmentsResponse struct {
	RunID     string                `json:"run_id"`
	Fragments []ContextFragmentText `json:"fragments"`
}

// GetSessionContextLifecycleFragments godoc
// @Summary Get the injected context texts of a run
// @Description Return every fragment the run put in front of the model outside the conversation (system prompt pieces, workspace rules, tool usage, skills, recalled memory, tool definitions) with the text that was stored for it. Conversation messages are not included; the history holds them. The texts include workspace files and hook output, so the caller needs workspace_read on the bot besides access to the session
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param run_id path string true "Run ID"
// @Success 200 {object} ContextLifecycleFragmentsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/context-lifecycle/{run_id}/fragments [get].
func (h *SessionInfoHandler) GetSessionContextLifecycleFragments(c echo.Context) error {
	access, err := h.authorizeContextLifecycleSession(c)
	if err != nil {
		return err
	}
	if !access.canReadFragmentTexts() {
		return apperror.New(apperror.CodeContextLifecycleAccessDenied, nil)
	}
	runID, err := db.ParseUUID(strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}
	fragments, err := loadContextLifecycleFragments(c.Request().Context(), h.queries, access.sessionID, runID)
	if errors.Is(err, errContextLifecycleRunNotFound) {
		return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	}
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	return c.JSON(http.StatusOK, ContextLifecycleFragmentsResponse{RunID: runID.String(), Fragments: fragments})
}

type contextLifecycleFragmentQueries interface {
	GetContextLifecycleByRunID(ctx context.Context, runID pgtype.UUID) (sqlc.GetContextLifecycleByRunIDRow, error)
	ListContextFragmentTexts(ctx context.Context, arg sqlc.ListContextFragmentTextsParams) ([]sqlc.ListContextFragmentTextsRow, error)
}

func loadContextLifecycleFragments(
	ctx context.Context,
	queries contextLifecycleFragmentQueries,
	sessionID pgtype.UUID,
	runID pgtype.UUID,
) ([]ContextFragmentText, error) {
	run, err := queries.GetContextLifecycleByRunID(ctx, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errContextLifecycleRunNotFound
		}
		return nil, fmt.Errorf("get run lifecycle: %w", err)
	}
	if run.SessionID != sessionID {
		return nil, errContextLifecycleRunNotFound
	}
	snapshot, err := contextfrag.DecodeLifecycleSnapshot(run.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("decode lifecycle snapshot for run %s: %w", runID.String(), err)
	}
	fragments := make([]ContextFragmentText, 0, len(snapshot.Fragments)+len(snapshot.ToolDefs))
	for _, ref := range snapshot.Fragments {
		fragments = append(fragments, ContextFragmentText{Kind: ref.Kind, Slot: ref.Slot, ContentHash: ref.ContentHash, TextHash: ref.TextHash, TokenEstimate: ref.TokenEstimate, TextBytes: ref.TextBytes})
	}
	for _, def := range snapshot.ToolDefs {
		fragments = append(fragments, ContextFragmentText{Label: def.Provider + "/" + def.Name, Kind: contextfrag.KindToolDefinition, ContentHash: def.ContentHash, TextHash: def.ContentHash, TokenEstimate: def.TokenEstimate, TextBytes: def.Bytes})
	}
	hashes := contextFragmentHashes([]ContextLifecycleTurn{{Snapshot: snapshot}})
	if len(hashes) == 0 {
		return fragments, nil
	}
	rows, err := queries.ListContextFragmentTexts(ctx, sqlc.ListContextFragmentTextsParams{BotID: run.BotID, ContentHashes: hashes})
	if err != nil {
		return nil, fmt.Errorf("list context fragment texts: %w", err)
	}
	texts := make(map[string]sqlc.ListContextFragmentTextsRow, len(rows))
	for _, row := range rows {
		texts[row.ContentHash] = row
	}
	for i := range fragments {
		row, ok := texts[fragments[i].TextHash]
		if !ok {
			continue
		}
		fragments[i].Text = row.Text
		fragments[i].Truncated = row.Truncated
		fragments[i].Available = true
		if fragments[i].Label == "" {
			fragments[i].Label = row.Label
		}
		if fragments[i].TextBytes == 0 {
			fragments[i].TextBytes = int(row.TextBytes)
		}
	}
	return fragments, nil
}

// contextLifecycleAccess is what a lifecycle request resolved to: the
// session, its bot, and the caller's permissions on that bot.
type contextLifecycleAccess struct {
	sessionID   pgtype.UUID
	botID       pgtype.UUID
	permissions []string
}

// canReadFragmentTexts gates the stored texts: they carry workspace files
// and hook output, which the file endpoints only show to workspace readers.
func (a contextLifecycleAccess) canReadFragmentTexts() bool {
	return bots.HasPermission(a.permissions, bots.PermissionWorkspaceRead)
}

// authorizeContextLifecycleSession resolves the session a lifecycle request
// names and enforces the same visibility the chat itself has.
func (h *SessionInfoHandler) authorizeContextLifecycleSession(c echo.Context) (contextLifecycleAccess, error) {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return contextLifecycleAccess{}, mapContextLifecycleError(err)
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return contextLifecycleAccess{}, apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return contextLifecycleAccess{}, apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}
	pgSessionID, err := db.ParseUUID(sessionID)
	if err != nil {
		return contextLifecycleAccess{}, apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}

	ctx := c.Request().Context()
	sessionRow, err := h.queries.GetSessionByID(ctx, pgSessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contextLifecycleAccess{}, apperror.New(apperror.CodeContextLifecycleNotFound, nil)
		}
		return contextLifecycleAccess{}, apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	sessionMode, runtimeType := normalizedSessionDescriptor(session.Thread{
		Type:        sessionRow.Type,
		SessionMode: sessionRow.SessionMode,
		RuntimeType: sessionRow.RuntimeType,
	})
	bot, err := AuthorizeBotAccessWithPermission(
		ctx,
		h.botService,
		h.accountService,
		userID,
		botID,
		requiredReadPermissionForSessionRuntime(sessionMode, runtimeType),
	)
	if err != nil {
		return contextLifecycleAccess{}, mapContextLifecycleError(err)
	}
	if sessionRow.BotID.String() != bot.ID {
		return contextLifecycleAccess{}, apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	}
	perms, err := h.resolveCurrentUserPermissions(c, userID, bot.ID)
	if err != nil {
		return contextLifecycleAccess{}, mapContextLifecycleError(err)
	}
	sess := session.Thread{
		ID:          sessionRow.ID.String(),
		BotID:       sessionRow.BotID.String(),
		Type:        sessionRow.Type,
		SessionMode: sessionMode,
		RuntimeType: runtimeType,
	}
	if sessionRow.CreatedByUserID.Valid {
		sess.CreatedByUserID = sessionRow.CreatedByUserID.String()
	}
	if !canAccessSession(sess, userID, perms) {
		return contextLifecycleAccess{}, apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	}
	return contextLifecycleAccess{sessionID: pgSessionID, botID: sessionRow.BotID, permissions: perms}, nil
}

// ContextLifecycleDecisionsResponse is the per-fragment selection audit of one
// run: every fragment the selector considered, with its slot, source, cost,
// and why it was kept, trimmed, or dropped. Prompt text is never part of it.
type ContextLifecycleDecisionsResponse struct {
	RunID     string                          `json:"run_id"`
	Decisions []contextfrag.SelectionDecision `json:"decisions"`
}

// GetSessionContextLifecycleDecisions godoc
// @Summary Get per-fragment selection decisions of a run
// @Description Return the content-light selection audit persisted for one run of a chat session: each fragment the context selector considered with its slot, source, token cost and decision. The list is read on demand because it grows with the history the run considered
// @Tags sessions
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Session ID"
// @Param run_id path string true "Run ID"
// @Success 200 {object} ContextLifecycleDecisionsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 401 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/sessions/{session_id}/context-lifecycle/{run_id}/decisions [get].
func (h *SessionInfoHandler) GetSessionContextLifecycleDecisions(c echo.Context) error {
	access, err := h.authorizeContextLifecycleSession(c)
	if err != nil {
		return err
	}
	runID, err := db.ParseUUID(strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	}
	decisions, err := loadContextLifecycleDecisions(c.Request().Context(), h.queries, access.sessionID, runID)
	if errors.Is(err, errContextLifecycleRunNotFound) {
		return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	}
	if err != nil {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	return c.JSON(http.StatusOK, ContextLifecycleDecisionsResponse{RunID: runID.String(), Decisions: decisions})
}

var errContextLifecycleRunNotFound = errors.New("context lifecycle run not found")

type contextLifecycleDecisionQueries interface {
	GetContextLifecycleByRunID(ctx context.Context, runID pgtype.UUID) (sqlc.GetContextLifecycleByRunIDRow, error)
	GetContextLifecycleSelectionDecisionsByRunID(ctx context.Context, runID pgtype.UUID) ([]byte, error)
}

// loadContextLifecycleDecisions reads a run's audit only after the run is
// confirmed to belong to the session the caller was authorized for.
func loadContextLifecycleDecisions(
	ctx context.Context,
	queries contextLifecycleDecisionQueries,
	sessionID pgtype.UUID,
	runID pgtype.UUID,
) ([]contextfrag.SelectionDecision, error) {
	run, err := queries.GetContextLifecycleByRunID(ctx, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errContextLifecycleRunNotFound
		}
		return nil, fmt.Errorf("get run lifecycle: %w", err)
	}
	if run.SessionID != sessionID {
		return nil, errContextLifecycleRunNotFound
	}
	raw, err := queries.GetContextLifecycleSelectionDecisionsByRunID(ctx, runID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get selection decisions: %w", err)
	}
	decisions := []contextfrag.SelectionDecision{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &decisions); err != nil {
			return nil, fmt.Errorf("decode selection decisions for run %s: %w", runID.String(), err)
		}
	}
	if decisions == nil {
		decisions = []contextfrag.SelectionDecision{}
	}
	return decisions, nil
}

func mapContextLifecycleError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
	switch httpErr.Code {
	case http.StatusBadRequest:
		return apperror.New(apperror.CodeContextLifecycleRequestInvalid, nil)
	case http.StatusUnauthorized:
		return apperror.New(apperror.CodeContextLifecycleAuthenticationRequired, nil)
	case http.StatusForbidden:
		return apperror.New(apperror.CodeContextLifecycleAccessDenied, nil)
	case http.StatusNotFound:
		return apperror.New(apperror.CodeContextLifecycleNotFound, nil)
	default:
		return apperror.Wrap(apperror.CodeContextLifecycleLoadFailed, err, nil)
	}
}

func contextLifecycleLimit(c echo.Context) int {
	limit := contextLifecycleDefaultLimit
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > contextLifecycleMaxLimit {
		limit = contextLifecycleMaxLimit
	}
	return limit
}

type contextLifecycleQueries interface {
	ListRecentContextLifecyclesBySession(
		context.Context,
		sqlc.ListRecentContextLifecyclesBySessionParams,
	) ([]sqlc.ListRecentContextLifecyclesBySessionRow, error)
	ListRecentContextLifecyclesBySessionBefore(
		context.Context,
		sqlc.ListRecentContextLifecyclesBySessionBeforeParams,
	) ([]sqlc.ListRecentContextLifecyclesBySessionBeforeRow, error)
	ListRecentAssistantMessagesBySession(
		context.Context,
		sqlc.ListRecentAssistantMessagesBySessionParams,
	) ([]sqlc.ListRecentAssistantMessagesBySessionRow, error)
	ListContextFragmentPreviews(context.Context, sqlc.ListContextFragmentPreviewsParams) ([]sqlc.ListContextFragmentPreviewsRow, error)
	HasUnmaterializedContextLifecycleMetadataBySession(ctx context.Context, sessionID pgtype.UUID) (bool, error)
	GetLatestContextLifecycleBySession(ctx context.Context, sessionID pgtype.UUID) ([]byte, error)
}

type contextLifecycleLoad struct {
	Turns []ContextLifecycleTurn
	// LegacySource reports that every returned turn came from pre-run-table
	// assistant metadata.
	LegacySource bool
	// HasMore reports that the chosen source holds older rows beyond the page.
	HasMore bool
	// NextCursor continues a run-keyed page past its oldest turn; empty when
	// the page is complete or came from the legacy source.
	NextCursor string
	// LegacyHistoryMayExist reports that run-keyed rows were returned while
	// older pre-run-table metadata also exists for the session, so the page
	// does not cover the session's full history era.
	LegacyHistoryMayExist bool
}

// contextLifecycleCursor is the keyset position of the oldest returned run:
// the list orders by (created_at DESC, run_id DESC), so the next page is every
// row strictly before this pair.
type contextLifecycleCursor struct {
	createdAt time.Time
	runID     pgtype.UUID
}

func encodeContextLifecycleCursor(createdAt time.Time, runID pgtype.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + runID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeContextLifecycleCursor(cursor string) (contextLifecycleCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return contextLifecycleCursor{}, false
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return contextLifecycleCursor{}, false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return contextLifecycleCursor{}, false
	}
	runID, err := db.ParseUUID(id)
	if err != nil {
		return contextLifecycleCursor{}, false
	}
	return contextLifecycleCursor{createdAt: createdAt, runID: runID}, true
}

func loadContextLifecycleTurns(
	ctx context.Context,
	queries contextLifecycleQueries,
	sessionID pgtype.UUID,
	limit int,
	before *contextLifecycleCursor,
) (contextLifecycleLoad, error) {
	probe := int32(limit + 1) //nolint:gosec // G115: limit is bounded to contextLifecycleMaxLimit
	rows, err := listContextLifecycleRows(ctx, queries, sessionID, probe, before)
	if err != nil {
		return contextLifecycleLoad{}, fmt.Errorf("list run lifecycles: %w", err)
	}
	if len(rows) > 0 {
		turns, err := lifecycleTurnsFromRunRows(rows, limit)
		if err != nil {
			return contextLifecycleLoad{}, err
		}
		unmaterialized, err := queries.HasUnmaterializedContextLifecycleMetadataBySession(ctx, sessionID)
		if err != nil {
			return contextLifecycleLoad{}, fmt.Errorf("probe unmaterialized legacy lifecycles: %w", err)
		}
		load := contextLifecycleLoad{
			Turns:                 turns,
			HasMore:               len(rows) > limit,
			LegacyHistoryMayExist: unmaterialized,
		}
		if load.HasMore && len(turns) > 0 {
			last := rows[len(turns)-1]
			load.NextCursor = encodeContextLifecycleCursor(last.CreatedAt.Time, last.RunID)
		}
		return load, nil
	}

	legacyRows, err := queries.ListRecentAssistantMessagesBySession(ctx, sqlc.ListRecentAssistantMessagesBySessionParams{
		SessionID: sessionID,
		MaxCount:  probe,
	})
	if err != nil {
		return contextLifecycleLoad{}, fmt.Errorf("list legacy assistant lifecycles: %w", err)
	}
	turns := legacyLifecycleTurnsFromRows(legacyRows, limit)
	return contextLifecycleLoad{
		Turns:        turns,
		LegacySource: len(turns) > 0,
		HasMore:      len(legacyRows) > limit,
	}, nil
}

// listContextLifecycleRows keeps the first page on the plain session index
// scan and only the continuation on the keyset predicate.
func listContextLifecycleRows(
	ctx context.Context,
	queries contextLifecycleQueries,
	sessionID pgtype.UUID,
	maxCount int32,
	before *contextLifecycleCursor,
) ([]sqlc.ListRecentContextLifecyclesBySessionRow, error) {
	if before == nil {
		return queries.ListRecentContextLifecyclesBySession(ctx, sqlc.ListRecentContextLifecyclesBySessionParams{
			SessionID: sessionID,
			MaxCount:  maxCount,
		})
	}
	older, err := queries.ListRecentContextLifecyclesBySessionBefore(ctx, sqlc.ListRecentContextLifecyclesBySessionBeforeParams{
		SessionID:       sessionID,
		BeforeCreatedAt: pgtype.Timestamptz{Time: before.createdAt, Valid: true},
		BeforeRunID:     before.runID,
		MaxCount:        maxCount,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]sqlc.ListRecentContextLifecyclesBySessionRow, len(older))
	for i, row := range older {
		rows[i] = sqlc.ListRecentContextLifecyclesBySessionRow(row)
	}
	return rows, nil
}

func lifecycleTurnsFromRunRows(
	rows []sqlc.ListRecentContextLifecyclesBySessionRow,
	limit int,
) ([]ContextLifecycleTurn, error) {
	turns := make([]ContextLifecycleTurn, 0, min(len(rows), limit))
	for _, row := range rows {
		if len(turns) >= limit {
			break
		}
		snapshot, err := contextfrag.DecodeLifecycleSnapshot(row.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("decode lifecycle snapshot for run %s: %w", row.RunID.String(), err)
		}
		errorCode := ""
		if row.ErrorCode.Valid {
			errorCode = row.ErrorCode.String
		}
		turnID := ""
		if row.TurnID.Valid {
			turnID = row.TurnID.String()
		}
		turns = append(turns, ContextLifecycleTurn{
			RunID:              row.RunID.String(),
			TurnID:             turnID,
			Status:             row.Status,
			ErrorCode:          errorCode,
			AssistantMessageID: snapshot.AssistantMessageID,
			CreatedAt:          row.CreatedAt.Time,
			Snapshot:           snapshot,
		})
	}
	return turns, nil
}

// latestContextLifecycleSnapshot reads the newest bounded summary only: one
// row, no page probe, and never the per-fragment audit.
func latestContextLifecycleSnapshot(
	ctx context.Context,
	queries contextLifecycleQueries,
	sessionID pgtype.UUID,
) (contextfrag.LifecycleSnapshot, bool, error) {
	raw, err := queries.GetLatestContextLifecycleBySession(ctx, sessionID)
	if err == nil {
		snapshot, err := contextfrag.DecodeLifecycleSnapshot(raw)
		if err != nil {
			return contextfrag.LifecycleSnapshot{}, false, fmt.Errorf("decode latest lifecycle snapshot: %w", err)
		}
		return snapshot, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contextfrag.LifecycleSnapshot{}, false, fmt.Errorf("get latest run lifecycle: %w", err)
	}
	legacyRows, err := queries.ListRecentAssistantMessagesBySession(ctx, sqlc.ListRecentAssistantMessagesBySessionParams{
		SessionID: sessionID,
		MaxCount:  1,
	})
	if err != nil {
		return contextfrag.LifecycleSnapshot{}, false, fmt.Errorf("list legacy assistant lifecycles: %w", err)
	}
	turns := legacyLifecycleTurnsFromRows(legacyRows, 1)
	if len(turns) == 0 {
		return contextfrag.LifecycleSnapshot{}, false, nil
	}
	return turns[0].Snapshot, true, nil
}

// legacyLifecycleTurnsFromRows extracts pre-run-table lifecycle snapshots from
// assistant message metadata, newest first, bounded by limit.
func legacyLifecycleTurnsFromRows(rows []sqlc.ListRecentAssistantMessagesBySessionRow, limit int) []ContextLifecycleTurn {
	turns := make([]ContextLifecycleTurn, 0, min(len(rows), limit))
	for _, row := range rows {
		if len(turns) >= limit {
			break
		}
		snapshot, ok := contextfrag.LifecycleSnapshotFromMetadata(row.Metadata)
		if !ok {
			continue
		}
		turns = append(turns, ContextLifecycleTurn{
			RunID:              row.RunID.String(),
			AssistantMessageID: row.ID.String(),
			CreatedAt:          row.CreatedAt.Time,
			Snapshot:           snapshot,
		})
	}
	return turns
}

func contextComposition(snapshot contextfrag.LifecycleSnapshot) ([]contextfrag.KindBreakdown, []ToolDefBucket, *contextfrag.ContextBudgetPlan) {
	var buckets []ToolDefBucket
	if len(snapshot.ToolDefs) > 0 {
		byProvider := make(map[string]*ToolDefBucket, 4)
		for _, def := range snapshot.ToolDefs {
			bucket := byProvider[def.Provider]
			if bucket == nil {
				bucket = &ToolDefBucket{Provider: def.Provider}
				byProvider[def.Provider] = bucket
			}
			bucket.Tools++
			bucket.TokenEstimate += def.TokenEstimate
		}
		buckets = make([]ToolDefBucket, 0, len(byProvider))
		for _, bucket := range byProvider {
			buckets = append(buckets, *bucket)
		}
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Provider < buckets[j].Provider
		})
	}
	return snapshot.Breakdown, buckets, snapshot.BudgetPlan
}

func aggregateContextLifecycle(turns []ContextLifecycleTurn) ContextLifecycleAggregates {
	agg := ContextLifecycleAggregates{Turns: len(turns)}
	for _, turn := range turns {
		agg.TotalCacheReadTokens += turn.Snapshot.CacheReadTokens
		agg.TotalCacheWriteTokens += turn.Snapshot.CacheWriteTokens
		for reason, count := range turn.Snapshot.Selection.DropReasons {
			if agg.DropReasons == nil {
				agg.DropReasons = make(map[string]int, 4)
			}
			agg.DropReasons[reason] += count
		}
		for _, record := range turn.Snapshot.Mutations {
			if agg.MutationKinds == nil {
				agg.MutationKinds = make(map[string]int, 4)
			}
			agg.MutationKinds[string(record.Kind)]++
		}
	}
	return agg
}
