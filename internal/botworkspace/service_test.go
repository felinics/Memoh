package botworkspace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/apperror"
)

// ─── fakes ───────────────────────────────────────────────────────────────────

type memRepo struct {
	mu   sync.Mutex
	rows map[string]*Workspace
	now  func() time.Time
	// beforeWrite runs (under the lock) before every observed write; tests
	// use it to simulate a concurrent takeover.
	beforeWrite func(w *Workspace)
	claims      int // Claim calls
}

func (r *memRepo) claimCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claims
}

func newMemRepo(now func() time.Time) *memRepo {
	return &memRepo{rows: map[string]*Workspace{}, now: now}
}

func (r *memRepo) get(botID string) Workspace {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.rows[botID]; ok {
		return *w
	}
	return Workspace{}
}

func (r *memRepo) Upsert(_ context.Context, botID, desired, image string, preserve bool) (Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.rows[botID]
	if !ok {
		w = &Workspace{BotID: botID, DesiredGeneration: 0, Observed: ObservedAbsent, Version: 0}
		r.rows[botID] = w
	}
	w.Desired = desired
	w.DesiredGeneration++
	if image != "" {
		w.Image = image
	}
	w.PreserveData = preserve
	w.Attempts = 0
	w.NextAttemptAt = r.now()
	w.Version++
	w.UpdatedAt = r.now()
	return *w, nil
}

func (r *memRepo) CancelRemoval(_ context.Context, botID, observed string) (Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.rows[botID]
	if !ok || w.RemovalState() != RemovalFailed {
		return Workspace{}, ErrNotFound
	}
	w.Desired = DesiredPresent
	w.DesiredGeneration++
	w.PreserveData = false
	w.Observed = observed
	w.ObservedGeneration = w.DesiredGeneration
	w.EverReady = w.EverReady || observed == ObservedRunning
	w.LastError, w.LastErrorPhase = "", ""
	w.Attempts = 0
	w.NextAttemptAt = r.now()
	w.Version++
	w.UpdatedAt = r.now()
	return *w, nil
}

func (r *memRepo) Get(_ context.Context, botID string) (Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.rows[botID]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	return *w, nil
}

func (r *memRepo) due(w *Workspace) bool {
	now := r.now()
	if w.NextAttemptAt.After(now) {
		return false
	}
	if !w.LeaseUntil.IsZero() && w.LeaseUntil.After(now) {
		return false
	}
	switch {
	case w.ObservedGeneration < w.DesiredGeneration,
		w.Observed == ObservedProvisioning, w.Observed == ObservedRemoving,
		w.Desired == DesiredPresent && (w.Observed == ObservedAbsent || w.Observed == ObservedFailed),
		w.Desired == DesiredAbsent && w.Observed != ObservedAbsent && w.Observed != ObservedFailed:
		return true
	}
	return false
}

func (r *memRepo) Claim(_ context.Context, owner string, lease time.Duration, desired string, limit int32) ([]Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims++
	var out []Workspace
	for _, w := range r.rows {
		if len(out) >= int(limit) {
			break
		}
		if !r.due(w) || (desired != "" && w.Desired != desired) {
			continue
		}
		w.LeaseOwner = owner
		w.LeaseUntil = r.now().Add(lease)
		w.Version++
		out = append(out, *w)
	}
	return out, nil
}

func (r *memRepo) ClaimOne(_ context.Context, botID, owner string, lease time.Duration) (Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.rows[botID]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	if !w.LeaseUntil.IsZero() && w.LeaseUntil.After(r.now()) {
		return Workspace{}, ErrNotFound
	}
	w.LeaseOwner = owner
	w.LeaseUntil = r.now().Add(lease)
	w.Version++
	return *w, nil
}

func (r *memRepo) Renew(_ context.Context, botID, owner string, lease time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.rows[botID]
	if !ok || w.LeaseOwner != owner {
		return ErrVersionConflict
	}
	w.LeaseUntil = r.now().Add(lease)
	return nil
}

func (r *memRepo) WriteObserved(_ context.Context, u ObservedWrite) (Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.rows[u.BotID]
	if ok && r.beforeWrite != nil {
		r.beforeWrite(w)
	}
	if !ok || w.LeaseOwner != u.Owner || w.Version != u.ExpectedVersion {
		return Workspace{}, ErrVersionConflict
	}
	w.Observed = u.Observed
	w.ObservedGeneration = u.ObservedGeneration
	w.EverReady = w.EverReady || u.MarkReady
	w.LastError = u.LastError
	w.LastErrorCode = u.LastErrorCode
	w.LastErrorPhase = u.LastErrorPhase
	w.Attempts = u.Attempts
	w.NextAttemptAt = u.NextAttemptAt
	if u.ReleaseLease {
		w.LeaseOwner = ""
		w.LeaseUntil = time.Time{}
	}
	w.Version++
	w.UpdatedAt = r.now()
	return *w, nil
}

func (r *memRepo) Release(_ context.Context, botID, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.rows[botID]; ok && w.LeaseOwner == owner {
		w.LeaseOwner = ""
		w.LeaseUntil = time.Time{}
	}
	return nil
}

func (r *memRepo) ListByObserved(_ context.Context, observed string, limit int32) ([]Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Workspace
	for _, w := range r.rows {
		if w.Observed == observed && len(out) < int(limit) {
			out = append(out, *w)
		}
	}
	return out, nil
}

type fakeBackend struct {
	mu            sync.Mutex
	provisionErr  error
	provisionErrs []error // consumed in order when non-empty
	teardownErr   error
	exists        bool
	running       bool
	image         string
	preserved     bool
	provisions    int
	teardowns     []bool // preserve flag per call
	inspects      int
}

func (b *fakeBackend) Provision(_ context.Context, _ string, image string, progress func(ProgressEvent)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.provisions++
	if progress != nil {
		progress(ProgressEvent{Type: "pulling"})
	}
	if len(b.provisionErrs) > 0 {
		err := b.provisionErrs[0]
		b.provisionErrs = b.provisionErrs[1:]
		if err != nil {
			return err
		}
	} else if b.provisionErr != nil {
		return b.provisionErr
	}
	b.exists, b.running = true, true
	b.image = image
	if progress != nil {
		progress(ProgressEvent{Type: "complete"})
	}
	return nil
}

func (b *fakeBackend) Teardown(_ context.Context, _ string, preserve bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.teardowns = append(b.teardowns, preserve)
	if b.teardownErr != nil {
		return b.teardownErr
	}
	b.exists, b.running = false, false
	return nil
}

func (b *fakeBackend) Inspect(context.Context, string) (Inspection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inspects++
	return Inspection{Exists: b.exists, Running: b.running, Image: b.image}, nil
}

func (b *fakeBackend) HasPreservedData(string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.preserved
}

// gatedBackend holds the provisioning of one bot until the test opens its
// gate, keeping a slot busy for as long as the test needs.
type gatedBackend struct {
	*fakeBackend
	gated     string
	entered   chan struct{}
	gate      chan struct{}
	enterOnce sync.Once
	openOnce  sync.Once
}

func newGatedBackend(gated string) *gatedBackend {
	return &gatedBackend{fakeBackend: &fakeBackend{}, gated: gated, entered: make(chan struct{}), gate: make(chan struct{})}
}

func (b *gatedBackend) Provision(ctx context.Context, botID, image string, progress func(ProgressEvent)) error {
	if botID == b.gated {
		b.enterOnce.Do(func() { close(b.entered) })
		<-b.gate
	}
	return b.fakeBackend.Provision(ctx, botID, image, progress)
}

func (b *gatedBackend) open() { b.openOnce.Do(func() { close(b.gate) }) }

// awaitEntered waits until the gated provisioning holds its slot.
func (b *gatedBackend) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the gated provisioning never started")
	}
}

// awaitKickTaken waits until the loop has taken the pending kick, then gives
// it a moment to finish the pass that kick started.
func awaitKickTaken(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(svc.kick) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the loop never took the kick")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
}

func awaitObserved(t *testing.T, repo *memRepo, botID, observed string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if repo.get(botID).Observed == observed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never reached %s: %+v", botID, observed, repo.get(botID))
}

func stopService(t *testing.T, svc *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

type statusRecorder struct {
	mu     sync.Mutex
	status map[string]string
}

func (s *statusRecorder) SetBotStatusFromWorkspace(_ context.Context, botID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil {
		s.status = map[string]string{}
	}
	s.status[botID] = status
	return nil
}

func (s *statusRecorder) get(botID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[botID]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestService(t *testing.T, backend *fakeBackend) (*Service, *memRepo, *statusRecorder, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	repo := newMemRepo(clk.now)
	svc := New(repo, backend, nil, Options{Owner: "test", MaxAttempts: 3, BackoffBase: 30 * time.Second, BackoffCap: 10 * time.Minute})
	svc.now = clk.now
	svc.rnd = func() float64 { return 0.5 } // jitter factor exactly 1.0
	status := &statusRecorder{}
	svc.SetBotStatusWriter(status)
	return svc, repo, status, clk
}

const bot = "00000000-0000-0000-0000-00000000aaaa"

// ─── tests ───────────────────────────────────────────────────────────────────

func TestProvisionHappyPath(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, status, _ := newTestService(t, backend)
	ctx := context.Background()

	if _, err := svc.EnsurePresent(ctx, bot, "img:1"); err != nil {
		t.Fatal(err)
	}
	if got := repo.get(bot); got.Desired != DesiredPresent || got.DesiredGeneration != 1 || got.Image != "img:1" {
		t.Fatalf("intent not recorded: %+v", got)
	}
	if n, err := svc.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("ReconcileOnce() = %d, %v", n, err)
	}
	w := repo.get(bot)
	if w.Observed != ObservedRunning || !w.EverReady || w.ObservedGeneration != 1 || w.LeaseOwner != "" {
		t.Fatalf("after provision: %+v", w)
	}
	if status.get(bot) != BotStatusReady {
		t.Fatalf("bot status = %q, want ready", status.get(bot))
	}
	if !w.Settled() {
		t.Fatal("running workspace must be settled")
	}
}

func TestProvisionRetryableFailureBacksOffThenRecovers(t *testing.T) {
	backend := &fakeBackend{provisionErrs: []error{
		&StepError{Phase: PhaseBridge, Retryable: true, Err: errors.New("bridge timeout")},
		nil,
	}}
	svc, repo, status, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")

	if _, err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	w := repo.get(bot)
	if w.Observed != ObservedFailed || w.Attempts != 1 || w.LastErrorPhase != PhaseBridge {
		t.Fatalf("after first failure: %+v", w)
	}
	if !w.NextAttemptAt.Equal(clk.now().Add(30 * time.Second)) {
		t.Fatalf("backoff = %s, want +30s", w.NextAttemptAt.Sub(clk.now()))
	}
	if status.get(bot) != BotStatusFailed {
		t.Fatalf("bot status = %q, want failed while never ready", status.get(bot))
	}
	// Not due yet: nothing claimed.
	if n, _ := svc.ReconcileOnce(ctx); n != 0 {
		t.Fatalf("claimed %d rows during backoff, want 0", n)
	}
	clk.advance(31 * time.Second)
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("claimed %d rows after backoff, want 1", n)
	}
	w = repo.get(bot)
	if w.Observed != ObservedRunning || w.Attempts != 0 || !w.EverReady {
		t.Fatalf("after retry: %+v", w)
	}
	if status.get(bot) != BotStatusReady {
		t.Fatalf("bot status = %q, want ready", status.get(bot))
	}
	// The retry reuses whatever the previous attempt left behind; nothing is
	// deleted unless the requested image changed.
	if len(backend.teardowns) != 0 {
		t.Fatalf("teardowns before retry = %v, want none", backend.teardowns)
	}
}

func TestProvisionNonRetryableFailureSpendsBudgetAndSlowRetries(t *testing.T) {
	backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseImagePrepare, Retryable: false, Err: errors.New("pull access denied")}}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "bad:image")
	_, _ = svc.ReconcileOnce(ctx)
	w := repo.get(bot)
	// The whole fast budget is consumed at once so Await answers now, and the
	// next attempt is on the slow cadence.
	if w.Observed != ObservedFailed || w.Attempts != 3 || w.RetryPending(3) {
		t.Fatalf("non-retryable failure must consume the fast budget: %+v", w)
	}
	if !w.NextAttemptAt.Equal(clk.now().Add(15 * time.Minute)) {
		t.Fatalf("slow retry = %s after failure, want +15m", w.NextAttemptAt.Sub(clk.now()))
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 0 {
		t.Fatalf("row claimed before its slow retry (%d)", n)
	}
	// The image shows up later; the slow retry picks it up without a new intent.
	clk.advance(16 * time.Minute)
	backend.provisionErr = nil
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("slow retry not claimed (%d)", n)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning || w.Attempts != 0 || !w.EverReady {
		t.Fatalf("after slow retry: %+v", w)
	}
}

func TestNewIntentResetsRetryBudget(t *testing.T) {
	backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseImagePrepare, Retryable: false, Err: errors.New("pull access denied")}}
	svc, repo, _, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "bad:image")
	_, _ = svc.ReconcileOnce(ctx)
	// A new intent (user changed the image) is due immediately with a fresh budget.
	backend.provisionErr = nil
	if _, err := svc.EnsurePresent(ctx, bot, "good:image"); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("new intent not claimed (%d)", n)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning || w.DesiredGeneration != 2 || w.ObservedGeneration != 2 {
		t.Fatalf("after new intent: %+v", w)
	}
}

func TestProvisionExhaustsAttempts(t *testing.T) {
	backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseStart, Retryable: true, Err: errors.New("start failed")}}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	for i := 0; i < 3; i++ {
		if i > 0 {
			clk.advance(time.Hour)
		}
		_, _ = svc.ReconcileOnce(ctx)
	}
	w := repo.get(bot)
	if w.Attempts != 3 || w.RetryPending(3) {
		t.Fatalf("after max attempts: %+v", w)
	}
	// Fast retries were 30s, 1m; the third failure falls back to the slow cadence.
	if !w.NextAttemptAt.Equal(clk.now().Add(15 * time.Minute)) {
		t.Fatalf("slow retry = %s after the last fast attempt, want +15m", w.NextAttemptAt.Sub(clk.now()))
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 0 {
		t.Fatalf("row claimed before its slow retry (%d)", n)
	}
	clk.advance(16 * time.Minute)
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("slow retry not claimed (%d)", n)
	}
	if w := repo.get(bot); w.Attempts != 4 || !w.NextAttemptAt.Equal(clk.now().Add(15*time.Minute)) {
		t.Fatalf("after a failed slow retry: %+v", w)
	}
}

func TestReadyWorkspaceIsNeverReplacedOnRetry(t *testing.T) {
	// A workspace that was ready once and then fails to start again must be
	// reused, never torn down, regardless of the failure.
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	if !repo.get(bot).EverReady {
		t.Fatal("precondition: workspace should be ready")
	}
	backend.provisionErrs = []error{&StepError{Phase: PhaseStart, Retryable: true, Err: errors.New("restart failed")}, nil}
	// User asks for a rebuild (new generation) on the same workspace.
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	clk.advance(time.Minute)
	_, _ = svc.ReconcileOnce(ctx)
	if len(backend.teardowns) != 0 {
		t.Fatalf("a ready workspace was torn down during retry: %v", backend.teardowns)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning {
		t.Fatalf("after retry: %+v", w)
	}
}

func TestPreservedArchiveBlocksReplacement(t *testing.T) {
	backend := &fakeBackend{preserved: true, provisionErrs: []error{
		&StepError{Phase: PhaseStart, Retryable: true, Err: errors.New("boom")}, nil,
	}}
	svc, _, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	clk.advance(time.Minute)
	_, _ = svc.ReconcileOnce(ctx)
	if len(backend.teardowns) != 0 {
		t.Fatalf("archive present: teardown must not run, got %v", backend.teardowns)
	}
}

func TestTeardownOnAbsentIntent(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, status, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	if _, err := svc.RequestAbsent(ctx, bot, true); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.ReconcileOnce(ctx)
	w := repo.get(bot)
	if w.Observed != ObservedAbsent || w.ObservedGeneration != 2 || !w.Settled() {
		t.Fatalf("after teardown: %+v", w)
	}
	if len(backend.teardowns) != 1 || !backend.teardowns[0] {
		t.Fatalf("teardown should honour preserve=true: %v", backend.teardowns)
	}
	// Absent intent never rewrites the bot's own status.
	if status.get(bot) != BotStatusReady {
		t.Fatalf("bot status changed by teardown: %q", status.get(bot))
	}
}

func TestTeardownRetriesConflicts(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	backend.teardownErr = errors.New("operation in flight")
	_, _ = svc.RequestAbsent(ctx, bot, false)
	_, _ = svc.ReconcileOnce(ctx)
	w := repo.get(bot)
	if w.Observed != ObservedRemoving || w.Attempts != 1 || w.LastErrorPhase != PhaseTeardown {
		t.Fatalf("after failed teardown: %+v", w)
	}
	backend.teardownErr = nil
	clk.advance(time.Minute)
	_, _ = svc.ReconcileOnce(ctx)
	if w := repo.get(bot); w.Observed != ObservedAbsent {
		t.Fatalf("teardown did not converge: %+v", w)
	}
}

func TestExpiredLeaseIsTakenOver(t *testing.T) {
	// Simulate a crashed instance: the row is stuck in provisioning with an
	// expired lease owned by someone else.
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	repo.mu.Lock()
	row := repo.rows[bot]
	row.Observed = ObservedProvisioning
	row.ObservedGeneration = 1
	row.LeaseOwner = "dead-instance"
	row.LeaseUntil = clk.now().Add(-time.Second)
	repo.mu.Unlock()

	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("expired lease not taken over (%d)", n)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning || w.LeaseOwner != "" {
		t.Fatalf("after takeover: %+v", w)
	}
}

func TestObserveDetectsDrift(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, _, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	// The container vanishes behind our back.
	backend.exists, backend.running = false, false
	if _, err := svc.Observe(ctx, bot); err != nil {
		t.Fatal(err)
	}
	w := repo.get(bot)
	if w.Observed != ObservedAbsent || !w.EverReady {
		t.Fatalf("drift not recorded: %+v", w)
	}
	// The next pass re-provisions without ever deleting (ever_ready).
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("drifted row not claimed (%d)", n)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning {
		t.Fatalf("drift not repaired: %+v", w)
	}
	if len(backend.teardowns) != 0 {
		t.Fatalf("drift repair must not tear down: %v", backend.teardowns)
	}
}

func TestObserveRecordsUserStop(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, status, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	backend.running = false
	if _, err := svc.Observe(ctx, bot); err != nil {
		t.Fatal(err)
	}
	if w := repo.get(bot); w.Observed != ObservedStopped {
		t.Fatalf("stop not observed: %+v", w)
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 0 {
		t.Fatalf("stopped workspace must not be auto-started (%d)", n)
	}
	if status.get(bot) != BotStatusReady {
		t.Fatalf("stopped bot status = %q", status.get(bot))
	}
}

func TestDriftScanRepairsVanishedStoppedWorkspace(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)

	// The user stops the workspace; the row settles as stopped and no pass
	// touches it again.
	backend.running = false
	if _, err := svc.Observe(ctx, bot); err != nil {
		t.Fatal(err)
	}
	if w := repo.get(bot); w.Observed != ObservedStopped {
		t.Fatalf("stop not observed: %+v", w)
	}

	// A stopped workspace that is still there stays stopped: the scan must not
	// undo the user's stop.
	clk.advance(svc.opts.DriftInterval + time.Minute)
	svc.detectDrift(ctx)
	if w := repo.get(bot); w.Observed != ObservedStopped {
		t.Fatalf("scan disturbed a healthy stopped workspace: %+v", w)
	}

	// It vanishes behind our back. Without the scan the row would stay stopped
	// forever while the bot reports ready.
	backend.exists = false
	clk.advance(svc.opts.DriftInterval + time.Minute)
	svc.detectDrift(ctx)
	if w := repo.get(bot); w.Observed != ObservedAbsent {
		t.Fatalf("drift on a stopped workspace not recorded: %+v", w)
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("drifted row not claimed (%d)", n)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning {
		t.Fatalf("drift not repaired: %+v", w)
	}
	if len(backend.teardowns) != 0 {
		t.Fatalf("drift repair must not tear down: %v", backend.teardowns)
	}
}

// A failed step stores its catalog code and no text: the upstream error, with
// whatever credentials it quoted, stays with the unit's result record.
func TestFailureStoresCodeNotText(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"unknown failure", errors.New("pull https://admin:s3cr3t@registry.example.com/v2/memoh?token=abc123: unauthorized"), "workspace_setup_failed"},
		{"image missing", fmt.Errorf("%w: not found", ErrImageNotFound), "workspace.image_not_found"},
		{"registry down", fmt.Errorf("%w: dial tcp", ErrImageRegistryUnavailable), "workspace.image_registry_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseImagePrepare, Err: tt.err}}
			svc, repo, _, _ := newTestService(t, backend)
			ctx := context.Background()
			_, _ = svc.EnsurePresent(ctx, bot, "img:1")
			_, _ = svc.ReconcileOnce(ctx)

			got := repo.get(bot)
			if got.LastErrorCode != tt.want || got.LastErrorPhase != PhaseImagePrepare {
				t.Fatalf("code/phase = %q/%q, want %q/%q", got.LastErrorCode, got.LastErrorPhase, tt.want, PhaseImagePrepare)
			}
			if got.LastError != "" {
				t.Fatalf("last_error = %q, want empty", got.LastError)
			}
		})
	}
}

func TestFailureCode(t *testing.T) {
	tests := []struct {
		name string
		step *StepError
		want apperror.Code
	}{
		{"image missing", &StepError{Phase: PhaseImagePrepare, Err: fmt.Errorf("pull: %w", ErrImageNotFound)}, apperror.CodeWorkspaceImageNotFound},
		{"registry down", &StepError{Phase: PhaseImagePrepare, Err: fmt.Errorf("pull: %w", ErrImageRegistryUnavailable)}, apperror.CodeWorkspaceImageRegistryUnavailable},
		{"template", &StepError{Phase: PhaseBootstrap, Err: errors.New("write AGENTS.md")}, apperror.CodeWorkspaceTemplateBootstrapFailed},
		{"other pull failure", &StepError{Phase: PhaseImagePrepare, Err: errors.New("pull access denied")}, apperror.CodeWorkspaceSetupFailed},
		{"start", &StepError{Phase: PhaseStart, Err: errors.New("boom")}, apperror.CodeWorkspaceSetupFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FailureCode(tt.step); got != tt.want {
				t.Fatalf("FailureCode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAwaitAndSubscribe(t *testing.T) {
	backend := &fakeBackend{}
	svc, _, _, _ := newTestService(t, backend)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, unsub := svc.Subscribe(bot)
	defer unsub()
	w, _ := svc.EnsurePresent(ctx, bot, "")
	go func() { _, _ = svc.ReconcileOnce(ctx) }()
	final, err := svc.Await(ctx, bot, w.DesiredGeneration)
	if err != nil || final.Observed != ObservedRunning {
		t.Fatalf("Await() = %+v, %v", final, err)
	}
	var types []string
	deadline := time.After(2 * time.Second)
	for len(types) < 3 {
		select {
		case ev := <-events:
			types = append(types, ev.Type)
		case <-deadline:
			t.Fatalf("events = %v, want pulling, complete, ready", types)
		}
	}
	if types[0] != "pulling" || types[1] != "complete" || types[2] != EventReady {
		t.Fatalf("events = %v", types)
	}
}

func TestDefaultRetrySchedule(t *testing.T) {
	o := Options{}.withDefaults()
	now := time.Unix(0, 0)
	var got []time.Duration
	for attempt := int32(1); attempt <= o.MaxAttempts; attempt++ {
		got = append(got, NextBackoff(now, attempt, o.BackoffBase, o.BackoffCap).Sub(now))
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 90 * time.Second, 90 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("schedule length = %d, want %d", len(got), len(want))
	}
	var total time.Duration
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt %d backoff = %s, want %s", i+1, got[i], want[i])
		}
		total += got[i]
	}
	if total > 6*time.Minute {
		t.Fatalf("fast retry window = %s, want under 6 minutes", total)
	}
	if o.SlowRetryInterval != 15*time.Minute {
		t.Fatalf("slow retry interval = %s, want 15m", o.SlowRetryInterval)
	}
}

func TestBackoffJitterStaysWithinBounds(t *testing.T) {
	svc, _, _, clk := newTestService(t, &fakeBackend{})
	base := 30 * time.Second
	svc.rnd = func() float64 { return 0 }
	if got := svc.nextAttempt(1).Sub(clk.now()); got != time.Duration(float64(base)*0.8) {
		t.Fatalf("min jitter = %s, want %s", got, time.Duration(float64(base)*0.8))
	}
	svc.rnd = func() float64 { return 1 }
	if got := svc.nextAttempt(1).Sub(clk.now()); got != time.Duration(float64(base)*1.2) {
		t.Fatalf("max jitter = %s, want %s", got, time.Duration(float64(base)*1.2))
	}
}

func TestFinalDistinguishesFastRetryFromSpentBudget(t *testing.T) {
	const budget = int32(3)
	pending := Workspace{Desired: DesiredPresent, DesiredGeneration: 1, Observed: ObservedFailed, ObservedGeneration: 1, Attempts: 1}
	if !pending.Settled() || pending.Final(budget) {
		t.Fatalf("a failure inside the fast budget is settled but not final: settled=%v final=%v", pending.Settled(), pending.Final(budget))
	}
	spent := pending
	spent.Attempts = budget
	if !spent.Final(budget) {
		t.Fatal("a failure past the fast budget is final even though slow retries continue")
	}
	running := Workspace{Desired: DesiredPresent, DesiredGeneration: 1, Observed: ObservedRunning, ObservedGeneration: 1}
	if !running.Final(budget) {
		t.Fatal("running is final")
	}
	absentSpent := Workspace{Desired: DesiredAbsent, DesiredGeneration: 1, Observed: ObservedFailed, ObservedGeneration: 1, Attempts: budget}
	if absentSpent.RetryPending(budget) || !absentSpent.Final(budget) {
		t.Fatal("a teardown failure past the fast budget has no fast retry pending and is final")
	}
}

func TestAwaitWaitsThroughRetryableFailure(t *testing.T) {
	// The caller relaying the outcome must not see the transient failure: the
	// first attempt fails retryably, the second succeeds, Await returns running.
	backend := &fakeBackend{provisionErrs: []error{
		&StepError{Phase: PhaseBridge, Retryable: true, Err: errors.New("bridge timeout")},
		nil,
	}}
	svc, repo, _, clk := newTestService(t, backend)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, _ := svc.EnsurePresent(ctx, bot, "")

	_, _ = svc.ReconcileOnce(ctx)
	if got := repo.get(bot); !got.RetryPending(3) {
		t.Fatalf("precondition: expected a pending retry, got %+v", got)
	}
	awaited := make(chan Workspace, 1)
	go func() {
		final, err := svc.Await(ctx, bot, w.DesiredGeneration)
		if err != nil {
			t.Errorf("Await() error = %v", err)
		}
		awaited <- final
	}()
	select {
	case final := <-awaited:
		t.Fatalf("Await returned during a pending retry: %+v", final)
	case <-time.After(700 * time.Millisecond):
	}
	clk.advance(time.Minute)
	_, _ = svc.ReconcileOnce(ctx)
	select {
	case final := <-awaited:
		if final.Observed != ObservedRunning {
			t.Fatalf("Await() = %+v, want running", final)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Await did not return after the retry succeeded")
	}
}

func TestAwaitReturnsNonRetryableFailure(t *testing.T) {
	backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseImagePrepare, Retryable: false, Err: errors.New("pull access denied")}}
	svc, _, _, _ := newTestService(t, backend)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, _ := svc.EnsurePresent(ctx, bot, "bad:image")
	_, _ = svc.ReconcileOnce(ctx)
	final, err := svc.Await(ctx, bot, w.DesiredGeneration)
	if err != nil || final.Observed != ObservedFailed || final.RetryPending(3) {
		t.Fatalf("Await() = %+v, %v; want the failure without a fast retry pending", final, err)
	}
}

func TestRetryReplacesContainerBuiltFromOtherImagePreservingData(t *testing.T) {
	// A never-ready workspace failed once and left a container built from the
	// old image; the user retries with a new image. The old container may hold
	// data restored from a consumed archive, so it is exported before removal.
	backend := &fakeBackend{provisionErrs: []error{
		&StepError{Phase: PhaseBridge, Retryable: true, Err: errors.New("bridge timeout")},
		nil,
	}}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "img:old")
	_, _ = svc.ReconcileOnce(ctx)
	// Simulate the container the failed attempt left behind.
	backend.exists, backend.image = true, "img:old"
	if repo.get(bot).EverReady {
		t.Fatal("precondition: workspace must never have been ready")
	}
	if _, err := svc.EnsurePresent(ctx, bot, "img:new"); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	_, _ = svc.ReconcileOnce(ctx)
	if len(backend.teardowns) != 1 || !backend.teardowns[0] {
		t.Fatalf("teardowns = %v, want exactly one preserving teardown", backend.teardowns)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning || backend.image != "img:new" {
		t.Fatalf("after retry: %+v image=%s", w, backend.image)
	}
}

func TestRetryReusesContainerBuiltFromSameImage(t *testing.T) {
	backend := &fakeBackend{provisionErrs: []error{
		&StepError{Phase: PhaseBridge, Retryable: true, Err: errors.New("bridge timeout")},
		nil,
	}}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "img:same")
	_, _ = svc.ReconcileOnce(ctx)
	backend.exists, backend.image = true, "img:same"
	clk.advance(time.Minute)
	_, _ = svc.ReconcileOnce(ctx)
	if len(backend.teardowns) != 0 {
		t.Fatalf("same image must be reused, got teardowns %v", backend.teardowns)
	}
	if w := repo.get(bot); w.Observed != ObservedRunning {
		t.Fatalf("after retry: %+v", w)
	}
}

func TestAbsentIntentOnUnknownBotStillTearsDown(t *testing.T) {
	// A bot with no row (created before the table existed, or whose intent
	// write failed) must not be assumed absent from the column default: the
	// backend is asked, idempotently, and only then is the intent answered.
	backend := &fakeBackend{exists: true, running: true}
	svc, repo, _, _ := newTestService(t, backend)
	ctx := context.Background()
	w, err := svc.RequestAbsent(ctx, bot, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := repo.get(bot); got.Observed != ObservedAbsent || got.ObservedGeneration != 0 {
		t.Fatalf("precondition: fresh row should carry the default observation, got %+v", got)
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("fresh absent intent not claimed (%d)", n)
	}
	if len(backend.teardowns) != 1 {
		t.Fatalf("teardowns = %v, want one", backend.teardowns)
	}
	final := repo.get(bot)
	if final.Observed != ObservedAbsent || final.ObservedGeneration != w.DesiredGeneration || !final.Settled() {
		t.Fatalf("after teardown: %+v", final)
	}
	if backend.exists {
		t.Fatal("backend container survived an absent intent")
	}
}

func TestClaimNeverExceedsFreeConcurrency(t *testing.T) {
	backend := &fakeBackend{}
	clk := &clock{t: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)}
	repo := newMemRepo(clk.now)
	svc := New(repo, backend, nil, Options{Owner: "test", Concurrency: 2, Batch: 10})
	svc.now = clk.now
	svc.rnd = func() float64 { return 0.5 }
	ctx := context.Background()
	bots := []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000003"}
	for _, id := range bots {
		_, _ = svc.EnsurePresent(ctx, id, "")
	}
	n, _ := svc.ReconcileOnce(ctx)
	if n != 2 {
		t.Fatalf("first pass claimed %d rows, want the 2 free slots", n)
	}
	// Exactly one row is still waiting, and it must not be holding a lease it
	// cannot use.
	waiting := 0
	for _, id := range bots {
		w := repo.get(id)
		if w.Observed == ObservedRunning {
			continue
		}
		waiting++
		if w.LeaseOwner != "" {
			t.Fatalf("unprocessed row %s holds a lease: %+v", id, w)
		}
	}
	if waiting != 1 {
		t.Fatalf("waiting rows = %d, want 1", waiting)
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("second pass claimed %d rows, want 1", n)
	}
}

func TestFreedSlotStartsTheRowThatWaitedForIt(t *testing.T) {
	const first, second = "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"
	backend := newGatedBackend(first)
	clk := &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	repo := newMemRepo(clk.now)
	// One slot, and no tick within the test: only a kick starts a row.
	svc := New(repo, backend, nil, Options{Owner: "test", Concurrency: 1, Interval: time.Hour})
	svc.now = clk.now
	svc.rnd = func() float64 { return 0.5 }
	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer stopService(t, svc)
	defer backend.open()

	if _, err := svc.EnsurePresent(ctx, first, ""); err != nil {
		t.Fatal(err)
	}
	backend.awaitEntered(t)
	// The second intent's kick finds the only slot busy.
	if _, err := svc.EnsurePresent(ctx, second, ""); err != nil {
		t.Fatal(err)
	}
	awaitKickTaken(t, svc)
	if w := repo.get(second); w.LeaseOwner != "" || w.Observed != ObservedAbsent {
		t.Fatalf("second row started without a free slot: %+v", w)
	}

	// Freeing the slot starts the waiting row at once, not on the next tick.
	backend.open()
	awaitObserved(t, repo, first, ObservedRunning)
	awaitObserved(t, repo, second, ObservedRunning)
}

func TestRowReleasedUntouchedDoesNotSpinTheLoop(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	// The database clock runs ahead of the process clock: the failed row is
	// due to the claim while Decide still sees its backoff running.
	repo := newMemRepo(func() time.Time { return clk.now().Add(5 * time.Second) })
	repo.rows[bot] = &Workspace{
		BotID: bot, Desired: DesiredPresent, DesiredGeneration: 1,
		Observed: ObservedFailed, ObservedGeneration: 1, Attempts: 1,
		NextAttemptAt: clk.now().Add(2 * time.Second), Version: 1,
	}
	svc := New(repo, &fakeBackend{}, nil, Options{Owner: "test", Interval: time.Hour})
	svc.now = clk.now
	svc.rnd = func() float64 { return 0.5 }
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer stopService(t, svc)

	svc.Kick()
	time.Sleep(300 * time.Millisecond)
	if n := repo.claimCount(); n > 2 {
		t.Fatalf("a row released untouched kept the loop claiming: %d claims", n)
	}
}

// refusingRepo refuses the observed writes refuse matches, for a reason other
// than a concurrent change, while claims keep succeeding: the database takes
// the claim but not the write a pass needs to record its step.
type refusingRepo struct {
	*memRepo
	refuse func(ObservedWrite) bool
}

func (r refusingRepo) WriteObserved(ctx context.Context, u ObservedWrite) (Workspace, error) {
	if r.refuse(u) {
		return Workspace{}, errors.New("write refused")
	}
	return r.memRepo.WriteObserved(ctx, u)
}

func TestPassWhoseLastWriteFailedDoesNotSpinTheLoop(t *testing.T) {
	// Each pass ends on a refused write and leaves the row released and due:
	// waking the loop for it would claim it again at once and call the
	// backend each time, for as long as the database refuses the write.
	running := func() *Workspace {
		return &Workspace{
			BotID: bot, Desired: DesiredAbsent, DesiredGeneration: 2,
			Observed: ObservedRunning, ObservedGeneration: 1, EverReady: true, Version: 1,
		}
	}
	cases := []struct {
		name    string
		row     func() *Workspace
		backend *fakeBackend
		refuse  func(ObservedWrite) bool
	}{
		{
			name:    "entering provisioning",
			backend: &fakeBackend{},
			refuse:  func(ObservedWrite) bool { return true },
		},
		{
			name:    "recording a failed provisioning",
			backend: &fakeBackend{provisionErr: errors.New("registry down")},
			refuse:  func(u ObservedWrite) bool { return u.Observed == ObservedFailed },
		},
		{
			name:    "recording a running workspace",
			backend: &fakeBackend{},
			refuse:  func(u ObservedWrite) bool { return u.Observed == ObservedRunning },
		},
		{
			name:    "entering removing",
			row:     running,
			backend: &fakeBackend{exists: true, running: true},
			refuse:  func(u ObservedWrite) bool { return u.Observed == ObservedRemoving },
		},
		{
			name:    "recording a removed workspace",
			row:     running,
			backend: &fakeBackend{exists: true, running: true},
			refuse:  func(u ObservedWrite) bool { return u.Observed == ObservedAbsent },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := &clock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
			repo := newMemRepo(clk.now)
			row := &Workspace{BotID: bot, Desired: DesiredPresent, DesiredGeneration: 1, Observed: ObservedAbsent, Version: 1}
			if tc.row != nil {
				row = tc.row()
			}
			row.NextAttemptAt = clk.now()
			repo.rows[bot] = row
			svc := New(refusingRepo{repo, tc.refuse}, tc.backend, nil, Options{Owner: "test", Interval: time.Hour})
			svc.now = clk.now
			svc.rnd = func() float64 { return 0.5 }
			if err := svc.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer stopService(t, svc)

			svc.Kick()
			time.Sleep(300 * time.Millisecond)
			if n := repo.claimCount(); n > 2 {
				t.Fatalf("a pass that recorded nothing kept the loop claiming: %d claims", n)
			}
		})
	}
}

func TestCatchUpLostToARemovalReleasesTheRowAndWakesTheLoop(t *testing.T) {
	// The pass only had to catch up the observed generation of a running
	// workspace, and the bot was deleted before that write landed, so the
	// write loses the version check. The row must be released and the loop
	// woken so the removal starts now: the clock never advances here, so a
	// row left leased would wait forever, and the interval never ticks.
	clk := &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	repo := newMemRepo(clk.now)
	repo.rows[bot] = &Workspace{
		BotID: bot, Desired: DesiredPresent, DesiredGeneration: 2, Image: "img:1",
		Observed: ObservedRunning, ObservedGeneration: 1, EverReady: true,
		NextAttemptAt: clk.now(), Version: 1,
	}
	var removal sync.Once
	repo.beforeWrite = func(w *Workspace) {
		removal.Do(func() {
			// What Upsert records for the removal intent.
			w.Desired, w.DesiredGeneration = DesiredAbsent, w.DesiredGeneration+1
			w.NextAttemptAt = clk.now()
			w.Version++
		})
	}
	backend := &fakeBackend{exists: true, running: true, image: "img:1"}
	svc := New(repo, backend, nil, Options{Owner: "test", Interval: time.Hour})
	svc.now = clk.now
	svc.rnd = func() float64 { return 0.5 }
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer stopService(t, svc)

	svc.Kick()
	awaitObserved(t, repo, bot, ObservedAbsent)
}

// busyProvisioningThenRemoval provisions a bot, keeps the only provisioning
// slot busy with another bot's creation, asks for the first bot's removal and
// runs one more pass.
func busyProvisioningThenRemoval(t *testing.T, teardownSlots int) (*memRepo, string) {
	t.Helper()
	const busy, ready = "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"
	backend := newGatedBackend(busy)
	clk := &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	repo := newMemRepo(clk.now)
	svc := New(repo, backend, nil, Options{Owner: "test", Concurrency: 1, TeardownConcurrency: teardownSlots})
	svc.now = clk.now
	svc.rnd = func() float64 { return 0.5 }
	ctx := context.Background()
	if _, err := svc.EnsurePresent(ctx, ready, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if w := repo.get(ready); w.Observed != ObservedRunning {
		t.Fatalf("setup: the bot to remove is not running: %+v", w)
	}

	var wg sync.WaitGroup
	t.Cleanup(func() {
		backend.open()
		wg.Wait()
	})
	if _, err := svc.EnsurePresent(ctx, busy, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.reconcileOnce(ctx, &wg); err != nil {
		t.Fatal(err)
	}
	backend.awaitEntered(t)
	if _, err := svc.RequestAbsent(ctx, ready, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.reconcileOnce(ctx, &wg); err != nil {
		t.Fatal(err)
	}
	return repo, ready
}

func TestRemovalsShareTheProvisioningSlotsByDefault(t *testing.T) {
	repo, ready := busyProvisioningThenRemoval(t, 0)
	if w := repo.get(ready); w.LeaseOwner != "" || w.Observed != ObservedRunning {
		t.Fatalf("removal claimed while the only shared slot was provisioning: %+v", w)
	}
}

func TestRemovalSlotsLetARemovalPassBusyProvisioning(t *testing.T) {
	repo, ready := busyProvisioningThenRemoval(t, 1)
	awaitObserved(t, repo, ready, ObservedAbsent)
}

func TestStaleClaimantNeverTouchesBackend(t *testing.T) {
	// Another instance took the row over between our claim and our first
	// write: the version check fails and no backend call is made.
	backend := &fakeBackend{exists: true, image: "img:old"}
	svc, repo, _, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "img:new")
	repo.beforeWrite = func(w *Workspace) {
		w.LeaseOwner = "other-instance"
		w.Version++
	}
	if n, _ := svc.ReconcileOnce(ctx); n != 1 {
		t.Fatalf("claimed %d, want 1", n)
	}
	if backend.provisions != 0 || len(backend.teardowns) != 0 {
		t.Fatalf("stale claimant touched the backend: provisions=%d teardowns=%v", backend.provisions, backend.teardowns)
	}
}

func TestObserveAfterManualStartRecoversFailedBot(t *testing.T) {
	// The bot failed to provision; the user starts the container by hand and
	// it comes up. Observe must record running and lift bots.status out of
	// failed, since nothing else will ever revisit this row.
	backend := &fakeBackend{provisionErr: &StepError{Phase: PhaseImagePrepare, Retryable: false, Err: errors.New("pull denied")}}
	svc, repo, status, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "bad:image")
	_, _ = svc.ReconcileOnce(ctx)
	if status.get(bot) != BotStatusFailed {
		t.Fatalf("precondition: bot status = %q, want failed", status.get(bot))
	}
	backend.exists, backend.running = true, true
	if _, err := svc.Observe(ctx, bot); err != nil {
		t.Fatal(err)
	}
	w := repo.get(bot)
	if w.Observed != ObservedRunning || !w.EverReady || w.Attempts != 0 {
		t.Fatalf("after observe: %+v", w)
	}
	if status.get(bot) != BotStatusReady {
		t.Fatalf("bot status = %q, want ready after the workspace came up", status.get(bot))
	}
}

func TestAwaitReturnsExhaustedTeardownFailure(t *testing.T) {
	// A teardown that keeps failing must eventually give Await a definite
	// answer instead of making callers wait out their whole budget.
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	backend.teardownErr = errors.New("provider refuses")
	w, _ := svc.RequestAbsent(ctx, bot, false)
	for i := 0; i < 3; i++ { // MaxAttempts in newTestService
		_, _ = svc.ReconcileOnce(ctx)
		clk.advance(time.Hour)
	}
	if got := repo.get(bot); got.Observed != ObservedFailed || got.LastErrorPhase != PhaseTeardown {
		t.Fatalf("precondition: teardown should be recorded as failed, got %+v", got)
	}
	final, err := svc.Await(ctx, bot, w.DesiredGeneration)
	if err != nil || final.Observed != ObservedFailed || final.Desired != DesiredAbsent {
		t.Fatalf("Await() = %+v, %v; want the teardown failure", final, err)
	}
}

// failRemoval provisions a workspace and drives a removal through its whole
// retry budget against a backend that keeps refusing.
func failRemoval(t *testing.T, svc *Service, backend *fakeBackend, clk *clock) {
	t.Helper()
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	backend.teardownErr = errors.New("provider refuses")
	_, _ = svc.RequestAbsent(ctx, bot, true)
	for i := 0; i < 3; i++ { // MaxAttempts in newTestService
		_, _ = svc.ReconcileOnce(ctx)
		clk.advance(time.Hour)
	}
}

func TestFailedRemovalWaitsForTheUser(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	failRemoval(t, svc, backend, clk)
	attempts := len(backend.teardowns)

	// Neither time nor a user-driven start brings the removal back.
	clk.advance(24 * time.Hour)
	_, _ = svc.ReconcileOnce(ctx)
	if _, err := svc.Observe(ctx, bot); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.ReconcileOnce(ctx)
	w := repo.get(bot)
	if len(backend.teardowns) != attempts || w.RemovalState() != RemovalFailed || w.LastErrorCode == "" {
		t.Fatalf("failed removal was touched: teardowns %d -> %d, %+v", attempts, len(backend.teardowns), w)
	}

	backend.teardownErr = nil
	_, _ = svc.RequestAbsent(ctx, bot, true)
	_, _ = svc.ReconcileOnce(ctx)
	if w := repo.get(bot); w.Observed != ObservedAbsent || w.RemovalState() != "" {
		t.Fatalf("a new intent should retry the removal: %+v", w)
	}
}

func TestRemovalOfVanishedWorkspaceKeepsCleaningUp(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	failRemoval(t, svc, backend, clk)
	backend.exists, backend.running = false, false
	_, _ = svc.RequestAbsent(context.Background(), bot, false)
	for i := 0; i < 5; i++ {
		_, _ = svc.ReconcileOnce(context.Background())
		clk.advance(time.Hour)
	}
	if w := repo.get(bot); w.RemovalState() != RemovalRemoving || len(backend.teardowns) != 8 {
		t.Fatalf("leftovers of a vanished workspace should be retried past the fast budget: %+v, %d teardowns", w, len(backend.teardowns))
	}
}

func TestNonRetryableRemovalFailsAtOnce(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, _, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "")
	_, _ = svc.ReconcileOnce(ctx)
	backend.teardownErr = &StepError{Phase: PhaseTeardown, Err: errors.New("too many files")}
	_, _ = svc.RequestAbsent(ctx, bot, true)
	_, _ = svc.ReconcileOnce(ctx)
	w := repo.get(bot)
	if w.RemovalState() != RemovalFailed || w.LastError != "" || w.LastErrorCode != string(apperror.CodeWorkspaceTeardownFailed) || !w.Final(svc.opts.MaxAttempts) {
		t.Fatalf("after one failed export: %+v", w)
	}
}

func TestCancelRemoval(t *testing.T) {
	backend := &fakeBackend{}
	svc, repo, _, clk := newTestService(t, backend)
	ctx := context.Background()
	failRemoval(t, svc, backend, clk)
	teardowns, provisions := len(backend.teardowns), backend.provisions
	if err := svc.CancelRemoval(ctx, bot); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.ReconcileOnce(ctx)
	clk.advance(24 * time.Hour)
	_, _ = svc.ReconcileOnce(ctx)
	w := repo.get(bot)
	if w.Desired != DesiredPresent || w.Observed != ObservedRunning || w.LastError != "" || !w.Settled() {
		t.Fatalf("after cancel: %+v", w)
	}
	// The workspace is kept as it stands: neither removed nor rebuilt.
	if len(backend.teardowns) != teardowns || backend.provisions != provisions {
		t.Fatalf("cancel touched the backend: teardowns %d -> %d, provisions %d -> %d",
			teardowns, len(backend.teardowns), provisions, backend.provisions)
	}

	// A removal still in flight is not withdrawn.
	_, _ = svc.RequestAbsent(ctx, bot, true)
	_, _ = svc.ReconcileOnce(ctx)
	if err := svc.CancelRemoval(ctx, bot); err != nil {
		t.Fatal(err)
	}
	if w := repo.get(bot); w.RemovalState() != RemovalRemoving {
		t.Fatalf("cancel during a removal: %+v", w)
	}
}
