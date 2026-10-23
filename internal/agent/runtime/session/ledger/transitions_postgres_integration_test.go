package ledger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// allRunStates is every value session_runs.state may hold.
var allRunStates = []ledger.State{
	ledger.StateAccepted, ledger.StateRunning, ledger.StateWaitingDecision, ledger.StateFinishing,
	ledger.StateCompleted, ledger.StateAborted, ledger.StateFailed, ledger.StateLost,
}

// The finishing seed proposes failed, so a Finalize that honors the proposal is
// distinguishable from one that writes what the caller asked for.
const seededProposalState = ledger.StateFailed

type transitionEnv struct {
	pool    *pgxpool.Pool
	store   ledger.Store
	queries *dbsqlc.Queries
	botID   string
}

type seededRun struct {
	ledger.Run
	session string
}

// transitionOp is one durable write. It reports whether the statement applied;
// a fenced statement that matches no row reports false, never an error.
type transitionOp struct {
	name  string
	apply func(ctx context.Context, t *testing.T, env transitionEnv, run seededRun) (bool, error)
	// legal maps each state the write may leave to the state it produces.
	// Every other state must be left untouched.
	legal map[ledger.State]ledger.State
}

// The durable run state machine, one row per write the runtime issues
// (admission has no source state and is covered by the admission tests). A
// state missing from a row's legal map is an illegal source: the write must
// not apply and the row must keep its state. No write leaves a terminal state.
func TestPostgresLedgerRunStateTransitionTable(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, _ := createLedgerResetFixture(t, ctx, pool)
	env := transitionEnv{pool: pool, store: ledger.NewPostgres(dbsqlc.New(pool), pool), queries: dbsqlc.New(pool), botID: botID}

	same := func(states ...ledger.State) map[ledger.State]ledger.State {
		legal := make(map[ledger.State]ledger.State, len(states))
		for _, state := range states {
			legal[state] = state
		}
		return legal
	}
	to := func(target ledger.State, states ...ledger.State) map[ledger.State]ledger.State {
		legal := make(map[ledger.State]ledger.State, len(states))
		for _, state := range states {
			legal[state] = target
		}
		return legal
	}
	finalize := func(state ledger.State, expected ledger.State) func(context.Context, *testing.T, transitionEnv, seededRun) (bool, error) {
		return func(ctx context.Context, _ *testing.T, env transitionEnv, run seededRun) (bool, error) {
			_, applied, err := env.store.Finalize(ctx, ledger.FinalizeParams{
				RunID: run.RunID, FencingToken: run.FencingToken, State: state,
				ErrorCode: "runtime_owner_lease_expired", ExpectedState: expected,
			})
			return applied, err
		}
	}
	withProposal := func(legal map[ledger.State]ledger.State) map[ledger.State]ledger.State {
		legal[ledger.StateFinishing] = seededProposalState
		return legal
	}

	ops := []transitionOp{
		{
			name: "Claim",
			apply: func(ctx context.Context, t *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, applied, err := env.store.Claim(ctx, ledger.ClaimParams{
					RunID: run.RunID, OwnerID: "owner-claimer", FencingToken: nextTransitionToken(ctx, t, env), LiveGeneration: "gen-claimer",
				})
				return applied, err
			},
			legal: to(ledger.StateRunning, ledger.StateAccepted),
		},
		{
			name: "SetWaitingDecision",
			apply: func(ctx context.Context, _ *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, applied, err := env.store.SetWaitingDecision(ctx, run.RunID, run.FencingToken)
				return applied, err
			},
			legal: to(ledger.StateWaitingDecision, ledger.StateRunning, ledger.StateWaitingDecision),
		},
		{
			name: "Resume",
			apply: func(ctx context.Context, _ *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, applied, err := env.store.Resume(ctx, run.RunID, run.FencingToken)
				return applied, err
			},
			legal: to(ledger.StateRunning, ledger.StateRunning, ledger.StateWaitingDecision),
		},
		{
			name: "AcceptSessionRunDecisionContinuation",
			apply: func(ctx context.Context, _ *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, err := env.queries.AcceptSessionRunDecisionContinuation(ctx, dbsqlc.AcceptSessionRunDecisionContinuationParams{
					DecisionID: "decision-transition", DecisionKind: "tool_approval",
					BotID: dbpkg.ParseUUIDOrEmpty(env.botID), SessionID: dbpkg.ParseUUIDOrEmpty(run.session),
					RunID: dbpkg.ParseUUIDOrEmpty(run.RunID), FencingToken: run.FencingToken,
				})
				return rowApplied(err)
			},
			legal: to(ledger.StateRunning, ledger.StateRunning, ledger.StateWaitingDecision),
		},
		{
			name: "ReclaimWaitingDecisionSessionRun",
			apply: func(ctx context.Context, t *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, err := env.queries.ReclaimWaitingDecisionSessionRun(ctx, dbsqlc.ReclaimWaitingDecisionSessionRunParams{
					OwnerID: textArg("owner-recovered"), NewFencingToken: nextTransitionToken(ctx, t, env),
					LiveGeneration: textArg("gen-recovered"), RunID: dbpkg.ParseUUIDOrEmpty(run.RunID),
					PreviousFencingToken: run.FencingToken,
				})
				return rowApplied(err)
			},
			legal: same(ledger.StateWaitingDecision),
		},
		{
			name: "PrepareFinish",
			apply: func(ctx context.Context, _ *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, applied, err := env.store.PrepareFinish(ctx, ledger.PrepareFinishParams{
					RunID: run.RunID, FencingToken: run.FencingToken, State: ledger.StateCompleted,
				})
				return applied, err
			},
			legal: to(ledger.StateFinishing, ledger.StateRunning, ledger.StateFinishing),
		},
		{
			name: "PrepareFinish allowing waiting_decision",
			apply: func(ctx context.Context, _ *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, applied, err := env.store.PrepareFinish(ctx, ledger.PrepareFinishParams{
					RunID: run.RunID, FencingToken: run.FencingToken, State: ledger.StateAborted, AllowWaitingDecision: true,
				})
				return applied, err
			},
			legal: to(ledger.StateFinishing, ledger.StateRunning, ledger.StateWaitingDecision, ledger.StateFinishing),
		},
		{
			name:  "Finalize completed",
			apply: finalize(ledger.StateCompleted, ""),
			legal: withProposal(to(ledger.StateCompleted, ledger.StateAccepted, ledger.StateRunning, ledger.StateWaitingDecision)),
		},
		{
			name:  "Finalize lost",
			apply: finalize(ledger.StateLost, ""),
			legal: withProposal(to(ledger.StateLost, ledger.StateAccepted, ledger.StateRunning, ledger.StateWaitingDecision)),
		},
		{
			// The unreserved-claim pass and graceful shutdown both pin the state
			// they classified, so a run that moved on is not decided for it.
			name:  "Finalize lost expecting running",
			apply: finalize(ledger.StateLost, ledger.StateRunning),
			legal: to(ledger.StateLost, ledger.StateRunning),
		},
		{
			name: "Finalize lost after abort intent",
			apply: func(ctx context.Context, t *testing.T, env transitionEnv, run seededRun) (bool, error) {
				if _, _, err := env.store.RequestAbort(ctx, run.RunID); err != nil {
					t.Fatalf("request abort: %v", err)
				}
				return finalize(ledger.StateLost, "")(ctx, t, env, run)
			},
			legal: withProposal(to(ledger.StateAborted, ledger.StateAccepted, ledger.StateRunning, ledger.StateWaitingDecision)),
		},
		{
			name: "RequestAbort",
			apply: func(ctx context.Context, _ *testing.T, env transitionEnv, run seededRun) (bool, error) {
				_, applied, err := env.store.RequestAbort(ctx, run.RunID)
				return applied, err
			},
			legal: same(ledger.StateAccepted, ledger.StateRunning, ledger.StateWaitingDecision),
		},
		{
			name: "FenceAndFinalizeOrphan",
			apply: func(ctx context.Context, t *testing.T, env transitionEnv, run seededRun) (bool, error) {
				resets := env.store.(ledger.ResetStore)
				lease, acquired, err := resets.AcquireReset(ctx, ledger.ResetLease{
					Scope: ledger.ResetScopeSession, BotID: env.botID, SessionID: run.session, Token: uuid.NewString(),
				}, time.Minute)
				if err != nil || !acquired {
					t.Fatalf("acquire reset lease = (%v, %v)", acquired, err)
				}
				defer func() { _, _ = resets.ReleaseReset(context.WithoutCancel(ctx), lease) }()
				_, applied, err := env.store.(ledger.OrphanResetStore).FenceAndFinalizeOrphan(ctx, lease, run.Run)
				return applied, err
			},
			// A durable finish proposal outlives the reset like any other
			// terminal write: the finishing row keeps its proposed outcome.
			legal: withProposal(to(ledger.StateAborted, ledger.StateAccepted, ledger.StateRunning, ledger.StateWaitingDecision)),
		},
	}

	for _, op := range ops {
		for _, from := range allRunStates {
			want, legal := op.legal[from]
			t.Run(op.name+" from "+string(from), func(t *testing.T) {
				run := seedTransitionRun(ctx, t, env, from)
				applied, err := op.apply(ctx, t, env, run)
				if err != nil {
					t.Fatalf("%s from %s returned %v; a rejected transition is applied=false, not an error", op.name, from, err)
				}
				got := durableRunState(ctx, t, env, run.RunID)
				if !legal {
					if applied || got != from {
						t.Fatalf("%s from %s = applied:%v state:%q, want rejected and state unchanged", op.name, from, applied, got)
					}
					return
				}
				if !applied || got != want {
					t.Fatalf("%s from %s = applied:%v state:%q, want applied and %q", op.name, from, applied, got, want)
				}
			})
		}
	}
}

// Every fenced write is rejected under a token that is not the row's.
func TestPostgresLedgerRunStateTransitionsRequireCurrentToken(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, _ := createLedgerResetFixture(t, ctx, pool)
	env := transitionEnv{pool: pool, store: ledger.NewPostgres(dbsqlc.New(pool), pool), queries: dbsqlc.New(pool), botID: botID}

	for _, tt := range []struct {
		name  string
		from  ledger.State
		apply func(context.Context, seededRun, int64) (bool, error)
	}{
		{name: "Claim", from: ledger.StateAccepted, apply: func(ctx context.Context, run seededRun, _ int64) (bool, error) {
			// Claims must carry a strictly newer token than the stored one.
			_, applied, err := env.store.Claim(ctx, ledger.ClaimParams{RunID: run.RunID, OwnerID: "owner-stale", FencingToken: run.FencingToken, LiveGeneration: "gen-stale"})
			return applied, err
		}},
		{name: "SetWaitingDecision", from: ledger.StateRunning, apply: func(ctx context.Context, run seededRun, stale int64) (bool, error) {
			_, applied, err := env.store.SetWaitingDecision(ctx, run.RunID, stale)
			return applied, err
		}},
		{name: "Resume", from: ledger.StateWaitingDecision, apply: func(ctx context.Context, run seededRun, stale int64) (bool, error) {
			_, applied, err := env.store.Resume(ctx, run.RunID, stale)
			return applied, err
		}},
		{name: "AcceptSessionRunDecisionContinuation", from: ledger.StateWaitingDecision, apply: func(ctx context.Context, run seededRun, stale int64) (bool, error) {
			_, err := env.queries.AcceptSessionRunDecisionContinuation(ctx, dbsqlc.AcceptSessionRunDecisionContinuationParams{
				DecisionID: "decision-stale", DecisionKind: "tool_approval",
				BotID: dbpkg.ParseUUIDOrEmpty(env.botID), SessionID: dbpkg.ParseUUIDOrEmpty(run.session),
				RunID: dbpkg.ParseUUIDOrEmpty(run.RunID), FencingToken: stale,
			})
			return rowApplied(err)
		}},
		{name: "ReclaimWaitingDecisionSessionRun", from: ledger.StateWaitingDecision, apply: func(ctx context.Context, run seededRun, stale int64) (bool, error) {
			_, err := env.queries.ReclaimWaitingDecisionSessionRun(ctx, dbsqlc.ReclaimWaitingDecisionSessionRunParams{
				OwnerID: textArg("owner-stale"), NewFencingToken: stale + 1, LiveGeneration: textArg("gen-stale"),
				RunID: dbpkg.ParseUUIDOrEmpty(run.RunID), PreviousFencingToken: stale,
			})
			return rowApplied(err)
		}},
		{name: "PrepareFinish", from: ledger.StateRunning, apply: func(ctx context.Context, run seededRun, stale int64) (bool, error) {
			_, applied, err := env.store.PrepareFinish(ctx, ledger.PrepareFinishParams{RunID: run.RunID, FencingToken: stale, State: ledger.StateCompleted})
			return applied, err
		}},
		{name: "Finalize", from: ledger.StateRunning, apply: func(ctx context.Context, run seededRun, stale int64) (bool, error) {
			_, applied, err := env.store.Finalize(ctx, ledger.FinalizeParams{RunID: run.RunID, FencingToken: stale, State: ledger.StateLost})
			return applied, err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run := seedTransitionRun(ctx, t, env, tt.from)
			applied, err := tt.apply(ctx, run, run.FencingToken+1000)
			if err != nil || applied {
				t.Fatalf("%s under a stale token = applied:%v err:%v, want rejected", tt.name, applied, err)
			}
			if got := durableRunState(ctx, t, env, run.RunID); got != tt.from {
				t.Fatalf("state = %q, want %q", got, tt.from)
			}
		})
	}
}

// ClaimedRuns lists only what the unreserved-claim pass may ask the live
// backend about: running rows with an owner, claimed under the named
// generation before the grace cutoff, in run ID order after the cursor.
func TestPostgresLedgerClaimedRunsSelectsOldRunningClaimsOfOneGeneration(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, _ := createLedgerResetFixture(t, ctx, pool)
	env := transitionEnv{pool: pool, store: ledger.NewPostgres(dbsqlc.New(pool), pool), queries: dbsqlc.New(pool), botID: botID}
	generation := "gen-claimed-" + uuid.NewString()

	seed := func(state ledger.State, gen string, age time.Duration) string {
		t.Helper()
		run := seedTransitionRun(ctx, t, env, state)
		if _, err := pool.Exec(ctx, `
			UPDATE session_runs SET live_generation = $2, owner_since = now() - make_interval(secs => $3)
			WHERE run_id = $1 AND owner_id IS NOT NULL
		`, run.RunID, gen, age.Seconds()); err != nil {
			t.Fatalf("age seeded run: %v", err)
		}
		return run.RunID
	}
	listed := map[string]bool{
		seed(ledger.StateRunning, generation, time.Hour): true,
		seed(ledger.StateRunning, generation, time.Hour): true,
		seed(ledger.StateRunning, generation, time.Hour): true,
	}
	seed(ledger.StateRunning, generation, 0)
	seed(ledger.StateRunning, "gen-other-"+uuid.NewString(), time.Hour)
	seed(ledger.StateWaitingDecision, generation, time.Hour)
	seed(ledger.StateFinishing, generation, time.Hour)
	seed(ledger.StateLost, generation, time.Hour)
	seed(ledger.StateAccepted, generation, time.Hour)

	query := ledger.ClaimedRunQuery{CurrentGeneration: generation, MinAge: time.Minute, Limit: 2}
	first, err := env.store.ClaimedRuns(ctx, query)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 || first[0].RunID >= first[1].RunID {
		t.Fatalf("first page = %+v, want two runs in run ID order", first)
	}
	query.After = first[1].RunID
	second, err := env.store.ClaimedRuns(ctx, query)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 1 || second[0].RunID <= query.After {
		t.Fatalf("second page = %+v, want the one run after the cursor", second)
	}
	for _, run := range append(first, second...) {
		if !listed[run.RunID] {
			t.Fatalf("listed run %s (state %q) is not an old running claim of the generation", run.RunID, run.State)
		}
		delete(listed, run.RunID)
	}
	if len(listed) != 0 {
		t.Fatalf("old running claims not listed: %v", listed)
	}
}

// seedTransitionRun writes one row already in state, in its own session so the
// single-active index never couples two cases.
func seedTransitionRun(ctx context.Context, t *testing.T, env transitionEnv, state ledger.State) seededRun {
	t.Helper()
	sessionID := createLedgerResetSession(t, ctx, env.pool, env.botID)
	runID := uuid.NewString()
	var (
		owner, generation   *string
		token               int64
		proposed, errorCode *string
	)
	if state != ledger.StateAccepted {
		ownerID, gen := "owner-transition", "gen-transition"
		owner, generation = &ownerID, &gen
		token = nextTransitionToken(ctx, t, env)
	}
	if state == ledger.StateFinishing {
		value := string(seededProposalState)
		proposed = &value
	}
	switch state {
	case ledger.StateFailed:
		code := "runtime_run_failed"
		errorCode = &code
	case ledger.StateLost:
		code := "runtime_owner_lease_expired"
		errorCode = &code
	}
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, owner_id, owner_since,
			fencing_token, live_generation,
			proposed_terminal_state, finish_proposed_at, error_code
		) VALUES (
			$1, $2, $3, $4, $5, 1,
			$6, '{}'::jsonb, 'transition', $7::text, CASE WHEN $7::text IS NULL THEN NULL ELSE now() END,
			$8, $9::text,
			$10::text, CASE WHEN $10::text IS NULL THEN NULL ELSE now() END, $11::text
		)
	`, runID, env.botID, sessionID, uuid.NewString(), uuid.NewString(),
		string(state), owner, token, generation, proposed, errorCode); err != nil {
		t.Fatalf("seed %s run: %v", state, err)
	}
	run, err := env.store.Get(ctx, runID)
	if err != nil {
		t.Fatalf("load seeded run: %v", err)
	}
	return seededRun{Run: run, session: sessionID}
}

func nextTransitionToken(ctx context.Context, t *testing.T, env transitionEnv) int64 {
	t.Helper()
	token, err := env.queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate fencing token: %v", err)
	}
	return token
}

func durableRunState(ctx context.Context, t *testing.T, env transitionEnv, runID string) ledger.State {
	t.Helper()
	run, err := env.store.Get(ctx, runID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	return run.State
}

func rowApplied(err error) (bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func textArg(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}
