package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/decision"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/runtimefence"
)

// resumeContext is server-authored execution intent, stored alongside the
// original admission input. It contains no JWTs, provider keys or tool outputs.
// The admission fingerprint still names the original request, unchanged.
type resumeContext struct {
	Version           int    `json:"version"`
	Query             string `json:"query"`
	ChatID            string `json:"chat_id"`
	UserID            string `json:"user_id"`
	ChannelIdentityID string `json:"channel_identity_id,omitempty"`
	CurrentChannel    string `json:"current_channel,omitempty"`
	ReplyTarget       string `json:"reply_target,omitempty"`
	ConversationType  string `json:"conversation_type,omitempty"`
	Model             string `json:"model,omitempty"`
	ReasoningEffort   string `json:"reasoning_effort,omitempty"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
	SessionType       string `json:"session_type,omitempty"`
	ToolHTTPURL       string `json:"tool_http_url,omitempty"`
	// ActorAccountID is the account the run acted for; a resume authorizes
	// it again before it runs.
	ActorAccountID string `json:"actor_account_id,omitempty"`
	// TokenClaims and ChatClaims are what earlier releases recorded instead:
	// the scope of the bearer the run carried. They are read, never written.
	TokenClaims      map[string]string `json:"token_claims,omitempty"`
	ChatClaims       map[string]string `json:"chat_claims,omitempty"`
	DeadlineAt       *time.Time        `json:"deadline_at,omitempty"`
	RequestMessageID string            `json:"request_message_id,omitempty"`
	// Version 2 carries an External Discuss batch instead of a query: the
	// resume admits it again behind its instructions.
	DiscussMessages       []turn.DiscussMessage       `json:"discuss_messages,omitempty"`
	DiscussOmittedSources []turn.ContextMessageSource `json:"discuss_omitted_sources,omitempty"`
}

// runAdmission is the server-authored part of a run's stored admission input:
// the execution intent recorded when the run started and the decisions it
// accepted since.
type runAdmission struct {
	Resume                *resumeContext                             `json:"resume"`
	DecisionContinuations map[string]decision.ContinuationCheckpoint `json:"decision_continuations"`
}

func decodeRunAdmission(raw []byte) (runAdmission, error) {
	var admitted runAdmission
	err := json.Unmarshal(raw, &admitted)
	return admitted, err
}

// errResumeUnrecoverable marks an interrupted intent that no later scan can
// continue. The worker retires it instead of retrying it every pass; failures
// that may clear (workspace readiness, database errors) stay unwrapped.
var errResumeUnrecoverable = errors.New("interrupted session cannot resume")

// SetResumeReadiness checks workspace transport before claiming a continuation.
func (s *Service) SetResumeReadiness(check func(context.Context, string, string) error) {
	s.resumeReady = check
}

func (s *Service) recordRunResumeContext(ctx context.Context, req ChatRequest) error {
	if s.queries == nil || req.RunID == "" || req.ShutdownResume {
		return nil
	}
	fence, ok := runtimefence.FromContext(ctx)
	if !ok || fence.Token <= 0 {
		return nil
	}
	data := resumeContext{
		Version: 1, Query: modelQueryText(req), ChatID: req.ChatID,
		UserID: req.UserID, ChannelIdentityID: req.SourceChannelIdentityID,
		CurrentChannel: req.CurrentChannel, ReplyTarget: req.ReplyTarget,
		ConversationType: req.ConversationType, Model: req.Model, ReasoningEffort: req.ReasoningEffort,
		WorkspaceTargetID: req.WorkspaceTargetID, SessionType: req.SessionType, ToolHTTPURL: req.ToolHTTPURL,
		ActorAccountID: strings.TrimSpace(req.ActorAccountID),
	}
	if req.UserMessagePersisted || req.ReusePersistedUserMessage {
		data.RequestMessageID = strings.TrimSpace(req.PersistedUserMessageID)
	}
	if deadline, ok := ctx.Deadline(); ok {
		data.DeadlineAt = &deadline
	}
	if req.discussMessages != nil {
		data.Version, data.Query = 2, ""
		data.DiscussMessages, data.DiscussOmittedSources = req.discussMessages, req.discussOmittedSources
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	count, err := s.queries.SaveSessionRunResumeContext(ctx, sqlc.SaveSessionRunResumeContextParams{
		RunID: db.ParseUUIDOrEmpty(req.RunID), FencingToken: fence.Token, ResumeContext: raw,
	})
	if err != nil {
		return fmt.Errorf("save session resume context: %w", err)
	}
	if count != 1 {
		return sessionruntime.ErrRunOwnershipLost
	}
	return nil
}

// actorAccountID is the account a resume authorizes: the one the run
// recorded, or for a run recorded by an earlier release the account of the
// bearer it carried.
func (data resumeContext) actorAccountID() string {
	if actor := strings.TrimSpace(data.ActorAccountID); actor != "" {
		return actor
	}
	if data.TokenClaims["typ"] != "chat_route" {
		if actor := strings.TrimSpace(data.TokenClaims["user_id"]); actor != "" {
			return actor
		}
		return strings.TrimSpace(data.TokenClaims["sub"])
	}
	return ""
}

// authorizeResume checks again, before a resume runs, that the account the
// run acted for is active and may still chat with the bot. A run of an
// earlier release that carried only a chat-route scope must name this bot.
func (s *Service) authorizeResume(ctx context.Context, botID string, data resumeContext) error {
	actor := data.actorAccountID()
	if actor == "" {
		if claims := data.ChatClaims; len(claims) > 0 && (claims["bot_id"] != botID || claims["chat_id"] == "") {
			return fmt.Errorf("%w: resume credential scope mismatch", errResumeUnrecoverable)
		}
		return nil
	}
	if s.accountService == nil || s.botPermissions == nil {
		return errors.New("resume authorization unavailable")
	}
	if err := s.accountService.ValidateSession(ctx, actor); err != nil {
		if errors.Is(err, accounts.ErrInactiveAccount) {
			return fmt.Errorf("%w: %w", errResumeUnrecoverable, err)
		}
		return err
	}
	allowed, err := s.botPermissions.HasBotPermission(ctx, botID, actor, bots.PermissionChat)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: resume permission revoked", errResumeUnrecoverable)
	}
	return nil
}

// StartSessionResume probes the scope catalog before launching a bounded worker.
// The worker retains a cursor per current tenant instead of querying tenant data
// from the startup context. Run contexts retain the same binding after detaching.
func (s *Service) StartSessionResume(ctx context.Context) error {
	if s.queries == nil || s.sessionManager == nil {
		return nil
	}
	scopes := s.sessionResumeScopes()
	first, err := scopes.ListScopePage(ctx, "", 16)
	if err != nil {
		return fmt.Errorf("list session resume scopes: %w", err)
	}
	if err := validateResumeScopePage(first, ""); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.resumeStop = cancel
	s.resumeDone = make(chan struct{})
	go func() {
		defer close(s.resumeDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		scan := sessionResumeScan{page: first, loaded: true}
		slots := make(chan struct{}, 4)
		for {
			if workerCtx.Err() != nil {
				return
			}
			if err := s.scanInterruptedSessions(workerCtx, scopes, &scan, slots); err != nil && workerCtx.Err() == nil {
				s.logger.WarnContext(workerCtx, "scan interrupted sessions failed", slog.Any("error", err))
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

type sessionResumeScan struct {
	page        SessionResumeScopePage
	loaded      bool
	scopeCursor string
	scopeIndex  int
	runCursor   pgtype.UUID
}

func validateResumeScopePage(page SessionResumeScopePage, after string) error {
	if len(page.Scopes) > 16 || (!page.Complete && (page.NextCursor == "" || page.NextCursor == after)) {
		return errors.New("invalid session resume scope page")
	}
	for _, scope := range page.Scopes {
		if strings.TrimSpace(scope) == "" {
			return errResumeScopeMismatch
		}
	}
	return nil
}

func (s *Service) scanInterruptedSessions(ctx context.Context, scopes SessionResumeScopeProvider, scan *sessionResumeScan, slots chan struct{}) error {
	if !scan.loaded {
		page, err := scopes.ListScopePage(ctx, scan.scopeCursor, 16)
		if err != nil {
			return err
		}
		if err := validateResumeScopePage(page, scan.scopeCursor); err != nil {
			return err
		}
		scan.page, scan.loaded = page, true
		scan.scopeIndex = 0
	}
	for scan.scopeIndex < len(scan.page.Scopes) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		scopeCtx, err := bindSessionResumeScope(ctx, scopes, scan.page.Scopes[scan.scopeIndex])
		if err != nil {
			scan.scopeIndex++
			scan.runCursor = pgtype.UUID{}
			return err
		}
		if !scan.runCursor.Valid {
			// Each pass over a scope starts by retiring intents a later turn or
			// a session deletion has made moot, so the pending-resume index and
			// the listing below only carry work that can still continue.
			if retired, err := s.queries.RetireSupersededInterruptedSessionRuns(scopeCtx); err != nil {
				s.logger.WarnContext(scopeCtx, "retire superseded interrupted sessions failed", slog.Any("error", err))
			} else if retired > 0 {
				s.logger.InfoContext(scopeCtx, "retired superseded interrupted sessions", slog.Int64("count", retired))
			}
			scan.runCursor = pgtype.UUID{Valid: true}
		}
		rows, err := s.queries.ListInterruptedSessionRuns(scopeCtx, scan.runCursor)
		if err != nil {
			scan.scopeIndex++
			scan.runCursor = pgtype.UUID{}
			return err
		}
		for _, row := range rows {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if row.TeamID.String() != scopes.CurrentScope(scopeCtx) {
				return errResumeScopeMismatch
			}
			select {
			case slots <- struct{}{}:
				done, startErr := s.resumeInterruptedSession(scopeCtx, row)
				switch {
				case errors.Is(startErr, errResumeUnrecoverable):
					s.retireInterruptedSession(scopeCtx, row, startErr)
				case startErr != nil:
					s.logger.WarnContext(scopeCtx, "resume interrupted session deferred", slog.String("run_id", row.RunID.String()), slog.Any("error", startErr))
				}
				if done == nil {
					<-slots
				} else {
					go func() { <-done; <-slots }()
				}
			default:
				return nil
			}
			scan.runCursor = row.RunID
		}
		if len(rows) == 100 {
			return nil
		}
		scan.scopeIndex++
		scan.runCursor = pgtype.UUID{}
	}
	scan.scopeCursor = scan.page.NextCursor
	if scan.page.Complete {
		scan.scopeCursor = ""
	}
	scan.loaded = false
	return nil
}

// StopSessionResume stops discovery only. Active runs are interrupted by the
// manager after it persists their marker, not by cancellation of this worker.
func (s *Service) StopSessionResume() {
	if s.resumeStop != nil {
		s.resumeStop()
	}
	if s.resumeDone != nil {
		<-s.resumeDone
	}
}

// retireInterruptedSession drops a resume intent that can never continue, so
// later scans neither list nor retry it. The run itself stays lost.
func (s *Service) retireInterruptedSession(ctx context.Context, row sqlc.SessionRun, cause error) {
	if _, err := s.queries.RetireInterruptedSessionRun(ctx, row.RunID); err != nil {
		s.logger.WarnContext(ctx, "retire interrupted session failed", slog.String("run_id", row.RunID.String()), slog.Any("error", err))
		return
	}
	s.logger.InfoContext(ctx, "interrupted session will not resume", slog.String("run_id", row.RunID.String()), slog.Any("reason", cause))
}

const resumeInstruction = "The server gracefully shut down during the previous run. Continue the unfinished user task using the saved session history and current workspace. Do not repeat completed work. A tool interrupted before its result was saved may already have produced side effects: inspect files, process/job status and saved output before deciding what to do; do not blindly replay it. Background task handles from the previous process are no longer live; consult subagent session histories and external job receipts. If the task is already complete, report that. Original task: "

func (s *Service) resumeInterruptedSession(ctx context.Context, row sqlc.SessionRun) (<-chan struct{}, error) {
	if row.TeamID.String() != s.sessionResumeScopes().CurrentScope(ctx) {
		return nil, errResumeScopeMismatch
	}
	input, err := decodeRunAdmission(row.InputJson)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errResumeUnrecoverable, err)
	}
	data := input.Resume
	if data == nil || data.ChatID == "" || !data.resumable() {
		return nil, fmt.Errorf("%w: unsupported resume context", errResumeUnrecoverable)
	}
	if data.DeadlineAt != nil && !time.Now().Before(*data.DeadlineAt) {
		return nil, fmt.Errorf("%w: execution budget expired", errResumeUnrecoverable)
	}
	decisionContext, err := s.interruptedDecisionContext(ctx, row, input.DecisionContinuations)
	if err != nil {
		return nil, err
	}
	request, err := s.interruptedRunRequest(ctx, row, data)
	if err != nil {
		return nil, err
	}
	resumeTurn, err := s.resumedHistoryTurn(ctx, row.SessionID.String(), request.messageID)
	if err != nil {
		return nil, err
	}
	if s.resumeReady != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := s.resumeReady(probeCtx, row.BotID.String(), data.WorkspaceTargetID)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	if err := s.authorizeResume(ctx, row.BotID.String(), *data); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	runBase, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if data.DeadlineAt != nil {
		var cancelDeadline context.CancelFunc
		runBase, cancelDeadline = context.WithDeadline(runBase, *data.DeadlineAt)
		oldCancel := cancel
		cancel = func() { cancelDeadline(); oldCancel() }
	}
	data.RequestMessageID = request.messageID
	payload, err := json.Marshal(map[string]any{"kind": "resume", "interrupted_run_id": row.RunID.String(), "resume": data})
	if err != nil {
		cancel()
		return nil, err
	}
	runCtx, admission, _, err := s.admitTriggeredRun(runBase, data.SessionType, row.BotID.String(), row.SessionID.String(), "resume:"+row.RunID.String(), payload, func(sessionruntime.RunHandle) *sessionruntime.RunAdmissionView {
		return &sessionruntime.RunAdmissionView{ResumeTurn: resumeTurn}
	}, row.RunID.String())
	if err != nil {
		cancel()
		if errors.Is(err, ledger.ErrResumeSuperseded) || errors.Is(err, sessionruntime.ErrInvocationConflict) {
			// A newer turn answered the session, or another worker already
			// admitted this continuation.
			return nil, fmt.Errorf("%w: %w", errResumeUnrecoverable, err)
		}
		return nil, err
	}
	req := ChatRequest{
		BotID: row.BotID.String(), ThreadID: row.SessionID.String(), ChatID: data.ChatID,
		RunID: admission.RunID, RunHandle: admission.Handle, TurnID: admission.TurnID, TurnPosition: &admission.TurnPosition,
		Query: resumeInstruction + data.Query, UserID: data.UserID, SourceChannelIdentityID: data.ChannelIdentityID,
		ActorAccountID: data.actorAccountID(), CurrentChannel: data.CurrentChannel, ReplyTarget: data.ReplyTarget,
		ConversationType: data.ConversationType, Model: data.Model, ReasoningEffort: data.ReasoningEffort,
		WorkspaceTargetID: data.WorkspaceTargetID, SessionType: data.SessionType, ToolHTTPURL: data.ToolHTTPURL,
		UserMessagePersisted: true, SkipMemoryExtraction: true, SkipTitleGeneration: true, ShutdownResume: true,
	}
	if decisionContext != "" {
		req.discussPromptSuffix = "\n\n" + decisionContext
		req.Query += req.discussPromptSuffix
	}
	if data.Version == 2 {
		req.discussMessages, req.discussOmittedSources, req.discussRecoveryExhausted = data.DiscussMessages, data.DiscussOmittedSources, true
		req.discussPromptPrefix = resumeInstruction
		req.Query = resumeInstruction + discussAgentFullContextPrompt(data.DiscussMessages) + req.discussPromptSuffix
	}
	chunks, errs := s.streamResumedTurn(runCtx, req, data, request)
	h := &runHandle{
		id: admission.RunID, ctx: runCtx, cancel: cancel, events: make(chan turn.Event, 16), errs: make(chan error, 1), inject: make(chan turn.InjectMessage),
		finishRun:         s.turnRunFinisher(runCtx, admission, data.SessionType),
		publishAgentEvent: s.turnAgentEventPublisher(admission.Handle),
	}
	done := make(chan struct{})
	go h.pump(turn.StartTurnCommand{TeamID: row.TeamID.String(), BotID: req.BotID, ThreadID: req.ThreadID}, chunks, errs)
	go func() { defer close(done); drainDeferredTurn(h) }()
	s.logger.InfoContext(runCtx, "interrupted session resumed", slog.String("previous_run_id", row.RunID.String()), slog.String("run_id", admission.RunID), slog.String("session_id", req.ThreadID))
	return done, nil
}

// resumable reports whether the context names a task to continue. A Discuss
// run's admission-time context has none; its batch arrives later.
func (data resumeContext) resumable() bool {
	switch data.Version {
	case 1:
		return data.SessionType != string(turn.ModeDiscuss) || strings.TrimSpace(data.Query) != ""
	case 2:
		return len(data.DiscussMessages) > 0
	default:
		return false
	}
}

// RecordSubagentResume is called after admission and before the first child tool.
func (s *Service) RecordSubagentResume(ctx context.Context, session tools.SessionContext, sessionID, query, model string) error {
	handle, ok := SubagentRunHandleFromContext(ctx)
	if !ok {
		return nil
	}
	return s.recordRunResumeContext(ctx, ChatRequest{
		BotID: session.BotID, ThreadID: sessionID, RunID: handle.RunID,
		ChatID: session.ChatID, UserID: session.UserID, SourceChannelIdentityID: session.ChannelIdentityID,
		ActorAccountID: session.ActorAccountID, CurrentChannel: session.CurrentPlatform, ReplyTarget: session.ReplyTarget,
		ConversationType: session.ConversationType, WorkspaceTargetID: session.WorkspaceTargetID, Query: query, Model: model, SessionType: "subagent",
	})
}
