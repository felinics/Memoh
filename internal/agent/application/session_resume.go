package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/accounts"
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
	Version           int               `json:"version"`
	Query             string            `json:"query"`
	ChatID            string            `json:"chat_id"`
	UserID            string            `json:"user_id"`
	ChannelIdentityID string            `json:"channel_identity_id,omitempty"`
	CurrentChannel    string            `json:"current_channel,omitempty"`
	ReplyTarget       string            `json:"reply_target,omitempty"`
	ConversationType  string            `json:"conversation_type,omitempty"`
	Model             string            `json:"model,omitempty"`
	ReasoningEffort   string            `json:"reasoning_effort,omitempty"`
	WorkspaceTargetID string            `json:"workspace_target_id,omitempty"`
	SessionType       string            `json:"session_type,omitempty"`
	ToolHTTPURL       string            `json:"tool_http_url,omitempty"`
	TokenClaims       map[string]string `json:"token_claims,omitempty"`
	ChatClaims        map[string]string `json:"chat_claims,omitempty"`
	DeadlineAt        *time.Time        `json:"deadline_at,omitempty"`
}

// errResumeUnrecoverable marks an interrupted intent that no later scan can
// continue. The worker retires it instead of retrying it every pass; failures
// that may clear (workspace readiness, database errors) stay unwrapped.
var errResumeUnrecoverable = errors.New("interrupted session cannot resume")

func (s *Service) SetResumeSecret(secret string) { s.resumeSecret = secret }

// SetResumeReadiness checks workspace transport before claiming a continuation.
func (s *Service) SetResumeReadiness(check func(context.Context, string, string) error) {
	s.resumeReady = check
}

func (s *Service) recordRunResumeContext(ctx context.Context, req ChatRequest) error {
	if s.resumeSecret == "" || s.queries == nil || req.RunID == "" || req.ShutdownResume {
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
	}
	var err error
	if data.TokenClaims, err = s.resumeClaims(req.Token); err != nil {
		return err
	}
	if data.ChatClaims, err = s.resumeClaims(req.ChatToken); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		data.DeadlineAt = &deadline
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

// Persist only an allowlisted scope after authenticating the original token.
// Expiration and signatures are renewed after restart, never copied to storage.
func (s *Service) resumeClaims(bearer string) (map[string]string, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(bearer, "Bearer "))
	if raw == "" {
		return nil, nil
	}
	token, err := jwt.Parse(raw, func(_ *jwt.Token) (any, error) { return []byte(s.resumeSecret), nil }, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return nil, errors.New("invalid session credential for resume")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("invalid session credential claims")
	}
	out := map[string]string{}
	for _, key := range []string{"sub", "user_id", "typ", "bot_id", "chat_id", "route_id", "channel_identity_id"} {
		if value, ok := claims[key].(string); ok && value != "" {
			out[key] = value
		}
	}
	return out, nil
}

func (s *Service) renewResumeCredential(ctx context.Context, botID string, claims map[string]string) (string, error) {
	if len(claims) == 0 {
		return "", nil
	}
	if claims["typ"] == "chat_route" {
		if claims["bot_id"] != botID || claims["chat_id"] == "" {
			return "", fmt.Errorf("%w: resume credential scope mismatch", errResumeUnrecoverable)
		}
	} else {
		accountID := claims["user_id"]
		if accountID == "" {
			accountID = claims["sub"]
		}
		if s.accountService == nil || s.botPermissions == nil {
			return "", errors.New("resume authorization unavailable")
		}
		if err := s.accountService.ValidateSession(ctx, accountID); err != nil {
			if errors.Is(err, accounts.ErrInactiveAccount) {
				return "", fmt.Errorf("%w: %w", errResumeUnrecoverable, err)
			}
			return "", err
		}
		allowed, err := s.botPermissions.HasBotPermission(ctx, botID, accountID, bots.PermissionChat)
		if err != nil {
			return "", err
		}
		if !allowed {
			return "", fmt.Errorf("%w: resume permission revoked", errResumeUnrecoverable)
		}
	}
	renewed := jwt.MapClaims{}
	for key, value := range claims {
		renewed[key] = value
	}
	renewed["iat"] = time.Now().Unix()
	renewed["exp"] = time.Now().Add(24 * time.Hour).Unix()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, renewed).SignedString([]byte(s.resumeSecret))
	if err != nil {
		return "", err
	}
	return "Bearer " + token, nil
}

// StartSessionResume probes the scope catalog before launching a bounded worker.
// The worker retains a cursor per current tenant instead of querying tenant data
// from the startup context. Run contexts retain the same binding after detaching.
func (s *Service) StartSessionResume(ctx context.Context) error {
	if s.queries == nil || s.sessionManager == nil || s.resumeSecret == "" {
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
	var input struct {
		Resume *resumeContext `json:"resume"`
	}
	if err := json.Unmarshal(row.InputJson, &input); err != nil {
		return nil, fmt.Errorf("%w: %w", errResumeUnrecoverable, err)
	}
	data := input.Resume
	if data == nil || data.Version != 1 || data.ChatID == "" {
		return nil, fmt.Errorf("%w: unsupported resume context", errResumeUnrecoverable)
	}
	if data.DeadlineAt != nil && !time.Now().Before(*data.DeadlineAt) {
		return nil, fmt.Errorf("%w: execution budget expired", errResumeUnrecoverable)
	}
	if s.resumeReady != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := s.resumeReady(probeCtx, row.BotID.String(), data.WorkspaceTargetID)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	token, err := s.renewResumeCredential(ctx, row.BotID.String(), data.TokenClaims)
	if err != nil {
		return nil, err
	}
	chatToken, err := s.renewResumeCredential(ctx, row.BotID.String(), data.ChatClaims)
	if err != nil {
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
	payload, err := json.Marshal(map[string]any{"kind": "resume", "interrupted_run_id": row.RunID.String(), "resume": data})
	if err != nil {
		cancel()
		return nil, err
	}
	runCtx, admission, _, err := s.admitTriggeredRun(runBase, row.BotID.String(), row.SessionID.String(), "resume:"+row.RunID.String(), payload, nil, row.RunID.String())
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
		Token: token, ChatToken: chatToken, CurrentChannel: data.CurrentChannel, ReplyTarget: data.ReplyTarget,
		ConversationType: data.ConversationType, Model: data.Model, ReasoningEffort: data.ReasoningEffort,
		WorkspaceTargetID: data.WorkspaceTargetID, SessionType: data.SessionType, ToolHTTPURL: data.ToolHTTPURL,
		UserMessagePersisted: true, SkipMemoryExtraction: true, SkipTitleGeneration: true, ShutdownResume: true,
	}
	chunks, errs := s.streamTurnChat(runCtx, req)
	h := &runHandle{
		id: admission.RunID, ctx: runCtx, cancel: cancel, events: make(chan turn.Event, 16), errs: make(chan error, 1), inject: make(chan turn.InjectMessage),
		finishRun:         s.turnRunFinisher(runCtx, admission),
		publishAgentEvent: s.turnAgentEventPublisher(admission.Handle),
	}
	done := make(chan struct{})
	go h.pump(turn.StartTurnCommand{TeamID: row.TeamID.String(), BotID: req.BotID, ThreadID: req.ThreadID}, chunks, errs)
	go func() { defer close(done); drainDeferredTurn(h) }()
	s.logger.InfoContext(runCtx, "interrupted session resumed", slog.String("previous_run_id", row.RunID.String()), slog.String("run_id", admission.RunID), slog.String("session_id", req.ThreadID))
	return done, nil
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
		ChatToken: session.SessionToken, CurrentChannel: session.CurrentPlatform, ReplyTarget: session.ReplyTarget,
		ConversationType: session.ConversationType, WorkspaceTargetID: session.WorkspaceTargetID, Query: query, Model: model, SessionType: "subagent",
	})
}
