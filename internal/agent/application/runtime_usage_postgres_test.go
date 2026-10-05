package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/bots"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

type externalUsageHarness struct {
	pool      *pgxpool.Pool
	queries   *dbsqlc.Queries
	manager   *sessionruntime.Manager
	messages  *messagepkg.DBService
	service   *Service
	runtime   string
	botID     string
	sessionID string
}

func newExternalUsageHarness(t *testing.T, ctx context.Context, runtimeType string) externalUsageHarness {
	t.Helper()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_type = $2 WHERE id = $1`, sessionID, runtimeType); err != nil {
		t.Fatalf("set session runtime: %v", err)
	}
	queries := dbsqlc.New(pool)
	store := postgresstore.NewQueriesWithPool(pool, queries)
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-external-usage",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Minute,
		Ledger:        ledger.NewPostgres(queries, pool),
		Fence:         runtimefence.NewActivator(store),
	})
	t.Cleanup(func() { _ = manager.Close() }) //nolint:contextcheck // test cleanup outlives the test context
	logger := slog.New(slog.DiscardHandler)
	messages := messagepkg.NewService(logger, store)
	service := &Service{messageService: messages, queries: store, sessionService: session.NewService(logger, store, nil), logger: logger}
	service.SetSessionRuntime(manager)
	return externalUsageHarness{
		pool: pool, queries: queries, manager: manager, messages: messages, service: service,
		runtime: runtimeType, botID: botID, sessionID: sessionID,
	}
}

// persist admits one run and writes its External Agent round the way the
// runtime turn path does, then finishes the run.
func (h externalUsageHarness) persist(t *testing.T, ctx context.Context, query string, result external.PromptResult, promptErr error) []messagepkg.Message {
	t.Helper()
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	admission, err := h.manager.Admit(runCtx, sessionruntime.AdmitInput{
		BotID: h.botID, SessionID: h.sessionID, InvocationID: uuid.NewString(),
		Payload: []byte(`{"kind":"message","text":"` + query + `"}`),
		Execution: sessionruntime.Execution{
			Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
			AbortCh:         make(chan struct{}, 1),
			Cancel:          func() { cancel(context.Canceled) },
			OwnershipCancel: cancel,
		},
	})
	if err != nil || !admission.Started {
		t.Fatalf("Admit() = %+v, %v", admission, err)
	}
	handle := admission.Handle
	fenced := runtimefence.WithContext(runCtx, runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
	position := admission.TurnPosition
	req := ChatRequest{
		BotID: h.botID, ChatID: h.botID, ThreadID: h.sessionID,
		RunID: admission.RunID, TurnID: admission.TurnID, TurnPosition: &position,
		Query: query, RawQuery: query, UserVisibleText: query,
		RunHandle: handle, InjectCh: make(chan turn.InjectMessage),
	}
	if err := h.service.persistRuntimeRound(fenced, req, h.runtime, "/data", result, promptErr, promptErr == nil && result.TurnCompleted, nil, nil); err != nil {
		t.Fatalf("persistRuntimeRound() error = %v", err)
	}
	if _, err := h.manager.FinishRun(context.WithoutCancel(ctx), handle, sessionruntime.RunStatusCompleted); err != nil {
		t.Fatalf("FinishRun() error = %v", err)
	}
	rows, err := h.messages.ListBySession(ctx, h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (h externalUsageHarness) latest(t *testing.T, ctx context.Context) messagepkg.ContextObservation {
	t.Helper()
	sessionID, _ := dbpkg.ParseUUID(h.sessionID)
	row, err := h.queries.GetLatestContextUsage(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return messagepkg.NoContextObservation(h.runtime)
	}
	if err != nil {
		t.Fatal(err)
	}
	return messagepkg.ResolveContextObservation(row.RuntimeType, row.Usage, row.ContextUsage)
}

func (h externalUsageHarness) expectLatest(t *testing.T, ctx context.Context, step string, want messagepkg.ContextObservation) {
	t.Helper()
	if got := h.latest(t, ctx); got != want {
		t.Fatalf("%s: latest context = %+v, want %+v", step, got, want)
	}
}

func assistantOutput(texts ...string) []sdk.Message {
	out := make([]sdk.Message, 0, len(texts))
	for _, text := range texts {
		out = append(out, sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: text}}})
	}
	return out
}

func externalTurnResult(usage sdk.Usage, observed *external.ContextUsage) external.PromptResult {
	return external.PromptResult{Output: assistantOutput("done"), Text: "done", Usage: &usage, Context: observed, TurnCompleted: true}
}

func known(used, window int64, source string) messagepkg.ContextObservation {
	return messagepkg.ContextObservation{Basis: messagepkg.ContextBasisRuntime, Known: true, UsedTokens: used, WindowTokens: window, Source: source}
}

var unknownRuntimeContext = messagepkg.ContextObservation{Basis: messagepkg.ContextBasisRuntime}

// The per-runtime turns carry the usage and context their drivers derive from
// the recorded wire fixtures (see the driver tests): accounting reads each
// turn total once; context reads the latest request.
func TestPostgresProviderUsageExternalRuntimes(t *testing.T) {
	claudeTurn := externalTurnResult(sdk.Usage{
		InputTokens: 3080, OutputTokens: 35, TotalTokens: 3115, CachedInputTokens: 2500,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 40, CacheReadTokens: 2500, CacheWriteTokens: 540},
	}, &external.ContextUsage{UsedTokens: 1585, WindowTokens: 200000, Source: "claude_code_request"})
	codexFirst := externalTurnResult(sdk.Usage{
		InputTokens: 2700, OutputTokens: 50, TotalTokens: 2750, ReasoningTokens: 15, CachedInputTokens: 2100,
		InputTokenDetails:  sdk.InputTokenDetail{NoCacheTokens: 600, CacheReadTokens: 2100},
		OutputTokenDetails: sdk.OutputTokenDetail{ReasoningTokens: 15},
	}, &external.ContextUsage{UsedTokens: 1520, WindowTokens: 258400, Source: "codex_last_request"})
	codexSecond := externalTurnResult(sdk.Usage{
		InputTokens: 1800, OutputTokens: 10, TotalTokens: 1810, CachedInputTokens: 1400,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 400, CacheReadTokens: 1400},
	}, &external.ContextUsage{UsedTokens: 1810, WindowTokens: 258400, Source: "codex_last_request"})
	acpUsage := sdk.Usage{
		InputTokens: 3080, OutputTokens: 35, TotalTokens: 3115, CachedInputTokens: 2500,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 40, CacheReadTokens: 2500, CacheWriteTokens: 540},
	}
	acpTurn := acpagent.DriverPromptResult(acpclient.PromptResult{
		StopReason: "end_turn", Text: "done", Usage: &acpUsage,
		Context: &external.ContextUsage{UsedTokens: 1585, WindowTokens: 200000, Source: "acp_usage_update"},
		Output:  []sdk.Message{{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "done"}}, Usage: &acpUsage}},
	}, "agent-1")
	acpTurn.TurnCompleted = true

	for _, tc := range []struct {
		runtime string
		turns   []external.PromptResult
		want    []messagepkg.ContextObservation
	}{
		{runtime: "claude-code", turns: []external.PromptResult{claudeTurn}, want: []messagepkg.ContextObservation{known(1585, 200000, "claude_code_request")}},
		{runtime: "codex", turns: []external.PromptResult{codexFirst, codexSecond}, want: []messagepkg.ContextObservation{known(1520, 258400, "codex_last_request"), known(1810, 258400, "codex_last_request")}},
		{runtime: "acp_agent", turns: []external.PromptResult{acpTurn}, want: []messagepkg.ContextObservation{known(1585, 200000, "acp_usage_update")}},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			h := newExternalUsageHarness(t, ctx, tc.runtime)
			h.expectLatest(t, ctx, "empty session", unknownRuntimeContext)
			var stored []usageTokens
			for i, result := range tc.turns {
				h.persist(t, ctx, "turn", result, nil)
				stored = append(stored, usageTokensOf(*result.Usage))
				h.expectLatest(t, ctx, fmt.Sprintf("turn %d", i+1), tc.want[i])
			}
			assertExternalUsageAccounting(t, ctx, h, stored)
		})
	}
}

// The newest state wins even when it is unknown; older measurements are never
// shown in its place.
func TestPostgresProviderUsageExternalContextStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	usage := sdk.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 100}}
	measured := func(used int) *external.ContextUsage {
		return &external.ContextUsage{UsedTokens: used, WindowTokens: 200000, Source: "acp_usage_update"}
	}

	t.Run("unknown after known", func(t *testing.T) {
		h := newExternalUsageHarness(t, ctx, "acp_agent")
		h.persist(t, ctx, "a", externalTurnResult(usage, measured(80000)), nil)
		h.expectLatest(t, ctx, "known", known(80000, 200000, "acp_usage_update"))
		h.persist(t, ctx, "b", external.PromptResult{Output: assistantOutput("partial")}, nil)
		h.expectLatest(t, ctx, "aborted without usage", unknownRuntimeContext)
		h.persist(t, ctx, "c", externalTurnResult(usage, measured(60000)), nil)
		h.persist(t, ctx, "d", externalTurnResult(usage, nil), nil)
		h.expectLatest(t, ctx, "no observation", unknownRuntimeContext)
		h.persist(t, ctx, "e", externalTurnResult(usage, measured(0)), nil)
		h.expectLatest(t, ctx, "explicit zero", known(0, 200000, "acp_usage_update"))
	})

	t.Run("failed round keeps its observation", func(t *testing.T) {
		h := newExternalUsageHarness(t, ctx, "codex")
		failed := externalTurnResult(usage, &external.ContextUsage{UsedTokens: 90000, WindowTokens: 258400, Source: "codex_last_request"})
		failed.TurnCompleted = false
		failed.AgentTurnID = "codex-turn"
		failed.SteerInputIDs = []string{"steer-1"}
		failed.Output = []sdk.Message{assistantOutput("first")[0], sdk.UserMessage("steer"), assistantOutput("second")[0]}
		h.persist(t, ctx, "a", failed, errors.New("codex turn failed"))
		h.expectLatest(t, ctx, "failed", known(90000, 258400, "codex_last_request"))
	})

	t.Run("steered round and deleted tail", func(t *testing.T) {
		h := newExternalUsageHarness(t, ctx, "claude-code")
		h.persist(t, ctx, "a", externalTurnResult(usage, &external.ContextUsage{UsedTokens: 70000, Source: "claude_code_request"}), nil)
		steered := externalTurnResult(usage, &external.ContextUsage{UsedTokens: 120000, WindowTokens: 1000000, Source: "claude_code_request"})
		steered.SteerInputIDs = []string{"steer-1"}
		steered.Output = []sdk.Message{assistantOutput("first")[0], sdk.UserMessage("steer"), assistantOutput("second")[0]}
		rows := h.persist(t, ctx, "b", steered, nil)
		h.expectLatest(t, ctx, "steered", known(120000, 1000000, "claude_code_request"))
		var tail string
		for _, row := range rows {
			if row.Role == "assistant" {
				tail = row.ID
			}
		}
		// History order, not wall-clock order, decides the newest state.
		if _, err := h.pool.Exec(ctx, `UPDATE bot_history_messages SET created_at = created_at - interval '1 hour' WHERE id = $1`, tail); err != nil {
			t.Fatal(err)
		}
		h.expectLatest(t, ctx, "steered tail with an older clock", known(120000, 1000000, "claude_code_request"))
		if err := h.messages.DeleteByIDs(ctx, []string{tail}); err != nil {
			t.Fatal(err)
		}
		h.expectLatest(t, ctx, "tail deleted", unknownRuntimeContext)
	})

	t.Run("legacy row", func(t *testing.T) {
		h := newExternalUsageHarness(t, ctx, "codex")
		if _, err := h.messages.Persist(ctx, messagepkg.PersistInput{
			BotID: h.botID, SessionID: h.sessionID, Role: "assistant", RuntimeType: "codex",
			Content: []byte(`{"role":"assistant","content":"legacy"}`), Usage: []byte(`{"inputTokens":1500000}`),
		}); err != nil {
			t.Fatal(err)
		}
		h.expectLatest(t, ctx, "legacy", unknownRuntimeContext)
	})
}

// Manual compaction replaces the measured context: the newest state turns
// unknown at once, stays unknown when read anew, and the next turn measures
// again. Accounting is untouched.
func TestPostgresProviderUsageCompactionInvalidatesContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newExternalUsageHarness(t, ctx, "codex")
	actor := uuid.NewString()
	driver := &controlDriver{commands: []external.Command{{Name: "compact", Kind: external.CommandOperation}}}
	h.service.externalDrivers = map[string]external.Driver{"codex": driver}
	h.service.botPermissions = &fakeBotPermissionChecker{values: map[string]bool{h.botID + ":" + actor + ":" + bots.PermissionManage: true}}

	usage := sdk.Usage{InputTokens: 180000, OutputTokens: 10, TotalTokens: 180010, InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 180000}}
	h.persist(t, ctx, "a", externalTurnResult(usage, &external.ContextUsage{UsedTokens: 180010, WindowTokens: 258400, Source: "codex_last_request"}), nil)
	h.expectLatest(t, ctx, "before compaction", known(180010, 258400, "codex_last_request"))

	if _, err := h.service.ExecuteRuntimeCommand(ctx, RuntimeControlRequest{BotID: h.botID, ThreadID: h.sessionID, ActorID: actor, Command: "compact"}); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if driver.compactCalls != 1 {
		t.Fatalf("compact calls = %d", driver.compactCalls)
	}
	h.expectLatest(t, ctx, "after compaction", unknownRuntimeContext)
	reopened := externalUsageHarness{queries: dbsqlc.New(openTurnAdmissionPostgres(t, ctx)), runtime: h.runtime, sessionID: h.sessionID}
	reopened.expectLatest(t, ctx, "after reconnect", unknownRuntimeContext)

	h.persist(t, ctx, "b", externalTurnResult(usage, &external.ContextUsage{UsedTokens: 20000, WindowTokens: 258400, Source: "codex_last_request"}), nil)
	h.expectLatest(t, ctx, "next turn", known(20000, 258400, "codex_last_request"))
	assertExternalUsageAccounting(t, ctx, h, []usageTokens{usageTokensOf(usage), usageTokensOf(usage)})
}

// Native sessions keep their reading: the newest step's provider input.
func TestPostgresProviderUsageNativeContextUnchanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newExternalUsageHarness(t, ctx, "model")
	for _, input := range []int{1000, 4000, 2500} {
		usage := []byte(fmt.Sprintf(`{"inputTokens":%d,"outputTokens":5,"totalTokens":%d}`, input, input+5))
		if _, err := h.messages.Persist(ctx, messagepkg.PersistInput{
			BotID: h.botID, SessionID: h.sessionID, Role: "assistant", RuntimeType: "model",
			Content: []byte(`{"role":"assistant","content":"step"}`), Usage: usage,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.messages.Persist(ctx, messagepkg.PersistInput{
		BotID: h.botID, SessionID: h.sessionID, Role: "assistant", RuntimeType: "model",
		Content: []byte(`{"role":"assistant","content":"no usage"}`),
	}); err != nil {
		t.Fatal(err)
	}
	h.expectLatest(t, ctx, "native", messagepkg.ContextObservation{Basis: messagepkg.ContextBasisProviderInput, Known: true, UsedTokens: 2500})
}

// Accounting reads each turn's total exactly once, whatever occupancy says.
func assertExternalUsageAccounting(t *testing.T, ctx context.Context, h externalUsageHarness, stored []usageTokens) {
	t.Helper()
	want := sumUsageTokens(stored)
	if want.noCache+want.cacheRead+want.cacheWrite != want.input {
		t.Fatalf("stored usage does not partition input: %+v", want)
	}
	sessionID, _ := dbpkg.ParseUUID(h.sessionID)
	stats, err := h.queries.GetSessionCacheStats(ctx, sessionID)
	if err != nil || stats.TotalInputTokens != int64(want.input) || stats.CacheReadTokens != int64(want.cacheRead) {
		t.Fatalf("session cache stats = %+v, err = %v, want input %d read %d", stats, err, want.input, want.cacheRead)
	}
	botID, _ := dbpkg.ParseUUID(h.botID)
	from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	records, err := h.queries.ListTokenUsageRecords(ctx, dbsqlc.ListTokenUsageRecordsParams{BotID: botID, FromTime: from, ToTime: to, PageLimit: 10})
	if err != nil || len(records) != len(stored) {
		t.Fatalf("records = %+v, err = %v, want %d", records, err, len(stored))
	}
	var input, read, output int64
	for _, r := range records {
		input += r.InputTokens
		read += r.CacheReadTokens
		output += r.OutputTokens
	}
	if input != int64(want.input) || read != int64(want.cacheRead) || output != int64(want.output) {
		t.Fatalf("records input %d read %d output %d, want %+v", input, read, output, want)
	}
}
