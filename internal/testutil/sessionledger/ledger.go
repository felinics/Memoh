// Package sessionledger supplies a shared in-memory ledger for runtime and
// application tests. No production composition root uses this fixture.
package sessionledger

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// Store is an in-memory ledger with the same guarantees the PostgreSQL
// adapter provides: one active run per session, fenced idempotent transitions,
// and a monotonic token sequence. It exists so admission ordering can be tested
// without a database; the adapter's own SQL is covered by its integration test.
type Store struct {
	Mu   sync.Mutex
	Runs map[string]*ledger.Run
	// bySession preserves insertion order so ActiveRun is deterministic.
	Order []string
	Token int64

	AdmitErr    error
	ClaimErr    error
	TokenErr    error
	PrepareErr  error
	FinalizeErr error
	ClaimHook   func(runID string)

	Admits    int
	Claims    int
	Finalized []ledger.FinalizeParams
}

func New() *Store {
	return &Store{Runs: map[string]*ledger.Run{}}
}

func (f *Store) Admit(_ context.Context, params ledger.AdmitParams) (ledger.Run, bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Admits++
	if f.AdmitErr != nil {
		return ledger.Run{}, false, f.AdmitErr
	}
	for _, id := range f.Order {
		run := f.Runs[id]
		if run.SessionID != params.SessionID {
			continue
		}
		if run.InvocationID == params.InvocationID {
			return *run, false, nil
		}
		if run.State.Active() {
			return ledger.Run{}, false, ledger.ErrSessionBusy
		}
	}
	if params.ResumeRunID != "" {
		source, ok := f.Runs[params.ResumeRunID]
		if !ok || source.BotID != params.BotID || source.SessionID != params.SessionID || source.State != ledger.StateLost || source.ErrorCode != "session_runtime.interrupted" || !source.AbortRequestedAt.IsZero() {
			return ledger.Run{}, false, ledger.ErrResumeSuperseded
		}
		for _, run := range f.Runs {
			if run.SessionID == params.SessionID && run.TurnPosition > source.TurnPosition {
				return ledger.Run{}, false, ledger.ErrResumeSuperseded
			}
		}
	}
	position := int64(1)
	for _, id := range f.Order {
		if f.Runs[id].SessionID == params.SessionID {
			position++
		}
	}
	run := &ledger.Run{
		RunID:            params.RunID,
		BotID:            params.BotID,
		SessionID:        params.SessionID,
		InvocationID:     params.InvocationID,
		TurnID:           params.TurnID,
		TurnPosition:     position,
		State:            ledger.StateAccepted,
		Input:            params.Input,
		InputFingerprint: params.InputFingerprint,
		CreatedAt:        time.Now(),
	}
	f.Runs[run.RunID] = run
	f.Order = append(f.Order, run.RunID)
	return *run, true, nil
}

func (f *Store) Get(_ context.Context, runID string) (ledger.Run, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	run, ok := f.Runs[runID]
	if !ok {
		return ledger.Run{}, ledger.ErrRunNotFound
	}
	return *run, nil
}

func (f *Store) GetByInvocation(_ context.Context, sessionID, invocationID string) (ledger.Run, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for _, id := range f.Order {
		if run := f.Runs[id]; run.SessionID == sessionID && run.InvocationID == invocationID {
			return *run, nil
		}
	}
	return ledger.Run{}, ledger.ErrRunNotFound
}

func (f *Store) ActiveRun(_ context.Context, sessionID string) (ledger.Run, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for _, id := range f.Order {
		if run := f.Runs[id]; run.SessionID == sessionID && run.State.Active() {
			return *run, nil
		}
	}
	return ledger.Run{}, ledger.ErrRunNotFound
}

func (f *Store) LatestRun(_ context.Context, sessionID string) (ledger.Run, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for i := len(f.Order) - 1; i >= 0; i-- {
		if run := f.Runs[f.Order[i]]; run.SessionID == sessionID {
			return *run, nil
		}
	}
	return ledger.Run{}, ledger.ErrRunNotFound
}

func (f *Store) NextFencingToken(context.Context) (int64, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.TokenErr != nil {
		return 0, f.TokenErr
	}
	f.Token++
	return f.Token, nil
}

func (f *Store) Claim(_ context.Context, params ledger.ClaimParams) (ledger.Run, bool, error) {
	if f.ClaimHook != nil {
		f.ClaimHook(params.RunID)
	}
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Claims++
	if f.ClaimErr != nil {
		return ledger.Run{}, false, f.ClaimErr
	}
	run, ok := f.Runs[params.RunID]
	if !ok {
		return ledger.Run{}, false, ledger.ErrRunNotFound
	}
	if run.State != ledger.StateAccepted || run.FencingToken >= params.FencingToken {
		return ledger.Run{}, false, nil
	}
	run.State = ledger.StateRunning
	run.OwnerID = params.OwnerID
	run.FencingToken = params.FencingToken
	run.LiveGeneration = params.LiveGeneration
	run.OwnerSince = time.Now()
	return *run, true, nil
}

func (f *Store) SetWaitingDecision(_ context.Context, runID string, token int64) (ledger.Run, bool, error) {
	return f.transition(runID, token, ledger.StateWaitingDecision)
}

func (f *Store) Resume(_ context.Context, runID string, token int64) (ledger.Run, bool, error) {
	return f.transition(runID, token, ledger.StateRunning)
}

func (f *Store) transition(runID string, token int64, state ledger.State) (ledger.Run, bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	run, ok := f.Runs[runID]
	if !ok || run.FencingToken != token || run.State.Terminal() || run.State == ledger.StateFinishing {
		return ledger.Run{}, false, nil
	}
	run.State = state
	return *run, true, nil
}

func (f *Store) PrepareFinish(_ context.Context, params ledger.PrepareFinishParams) (ledger.Run, bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.PrepareErr != nil {
		return ledger.Run{}, false, f.PrepareErr
	}
	run, ok := f.Runs[params.RunID]
	if !ok || run.FencingToken != params.FencingToken || run.State.Terminal() ||
		(run.State == ledger.StateWaitingDecision && !params.AllowWaitingDecision) {
		return ledger.Run{}, false, nil
	}
	if run.State != ledger.StateFinishing {
		run.State = ledger.StateFinishing
		run.ProposedState = params.State
		run.ProposedErrorCode = params.ErrorCode
		run.ProposedErrorMessage = params.ErrorMessage
		run.FinishProposedAt = time.Now()
	}
	return *run, true, nil
}

func (f *Store) Finalize(_ context.Context, params ledger.FinalizeParams) (ledger.Run, bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.FinalizeErr != nil {
		return ledger.Run{}, false, f.FinalizeErr
	}
	run, ok := f.Runs[params.RunID]
	if !ok || run.FencingToken != params.FencingToken || run.State.Terminal() {
		return ledger.Run{}, false, nil
	}
	state := params.State
	errorCode := params.ErrorCode
	errorMessage := params.ErrorMessage
	if run.State == ledger.StateFinishing {
		state = run.ProposedState
		errorCode = run.ProposedErrorCode
		errorMessage = run.ProposedErrorMessage
	} else if state == ledger.StateLost && !run.AbortRequestedAt.IsZero() {
		state = ledger.StateAborted
		errorCode = ""
		errorMessage = ""
	}
	run.State = state
	run.ErrorCode = errorCode
	run.ErrorMessage = errorMessage
	f.Finalized = append(f.Finalized, params)
	return *run, true, nil
}

func (f *Store) RequestAbort(_ context.Context, runID string) (ledger.Run, bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	run, ok := f.Runs[runID]
	if !ok || run.State.Terminal() || run.State == ledger.StateFinishing {
		return ledger.Run{}, false, nil
	}
	run.AbortRequestedAt = time.Now()
	return *run, true, nil
}

// StaleGenerationRuns mirrors the adapter's keyset sweep: active rows that were
// claimed by an incarnation other than the current one, ordered so a cursor can
// page through them.
func (f *Store) StaleGenerationRuns(_ context.Context, query ledger.StaleGenerationQuery) ([]ledger.Run, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	var matched []ledger.Run
	for _, id := range f.Order {
		run := *f.Runs[id]
		if !run.State.Active() || run.LiveGeneration == "" || run.LiveGeneration == query.CurrentGeneration {
			continue
		}
		if run.LiveGeneration < query.After.LiveGeneration ||
			(run.LiveGeneration == query.After.LiveGeneration && run.RunID <= query.After.RunID) {
			continue
		}
		matched = append(matched, run)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].LiveGeneration != matched[j].LiveGeneration {
			return matched[i].LiveGeneration < matched[j].LiveGeneration
		}
		return matched[i].RunID < matched[j].RunID
	})
	if query.Limit > 0 && len(matched) > int(query.Limit) {
		matched = matched[:query.Limit]
	}
	return matched, nil
}

func (f *Store) OrphanedRuns(_ context.Context, query ledger.OrphanQuery) ([]ledger.Run, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	cutoff := time.Now().Add(-query.MinAge)
	var matched []ledger.Run
	for _, id := range f.Order {
		run := *f.Runs[id]
		if run.State != ledger.StateAccepted || run.OwnerID != "" || !run.CreatedAt.Before(cutoff) {
			continue
		}
		matched = append(matched, run)
	}
	if query.Limit > 0 && len(matched) > int(query.Limit) {
		matched = matched[:query.Limit]
	}
	return matched, nil
}

// insertOrphan records an admission that committed under a process that died
// before it could claim anything.
func (f *Store) InsertOrphan(runID, sessionID, invocationID, fingerprint string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	run := &ledger.Run{
		RunID:            runID,
		BotID:            "bot-runtime",
		SessionID:        sessionID,
		InvocationID:     invocationID,
		TurnID:           runID + "-turn",
		TurnPosition:     1,
		State:            ledger.StateAccepted,
		InputFingerprint: fingerprint,
		CreatedAt:        time.Now().Add(-time.Hour),
	}
	f.Runs[run.RunID] = run
	f.Order = append(f.Order, run.RunID)
}

// insertClaimed records a run that some owner took and never finished, which is
// what the reaper finds after that owner disappears.
func (f *Store) InsertClaimed(runID, sessionID string, token int64, generation string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	run := &ledger.Run{
		RunID:          runID,
		BotID:          "bot-runtime",
		SessionID:      sessionID,
		InvocationID:   runID + "-inv",
		TurnID:         runID + "-turn",
		TurnPosition:   1,
		State:          ledger.StateRunning,
		OwnerID:        "owner-gone",
		FencingToken:   token,
		LiveGeneration: generation,
		OwnerSince:     time.Now().Add(-time.Minute),
		CreatedAt:      time.Now().Add(-time.Minute),
	}
	f.Runs[run.RunID] = run
	f.Order = append(f.Order, run.RunID)
}

func (f *Store) State(runID string) ledger.State {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	run, ok := f.Runs[runID]
	if !ok {
		return ""
	}
	return run.State
}

func (f *Store) ErrorCode(runID string) string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	run, ok := f.Runs[runID]
	if !ok {
		return ""
	}
	return run.ErrorCode
}

func (f *Store) SetFinalizeErr(err error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.FinalizeErr = err
}

func (f *Store) SetPrepareErr(err error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.PrepareErr = err
}

func (f *Store) Counts() (admits, claims int) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Admits, f.Claims
}

func (f *Store) TerminalWrites() []ledger.FinalizeParams {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return append([]ledger.FinalizeParams(nil), f.Finalized...)
}
