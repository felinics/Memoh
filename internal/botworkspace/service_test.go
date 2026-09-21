package botworkspace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── fakes ───────────────────────────────────────────────────────────────────

type memRepo struct {
	mu   sync.Mutex
	rows map[string]*Workspace
	now  func() time.Time
	// beforeWrite runs (under the lock) before every observed write; tests
	// use it to simulate a concurrent takeover.
	beforeWrite func(w *Workspace)
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
		w.Desired == DesiredAbsent && w.Observed != ObservedAbsent:
		return true
	}
	return false
}

func (r *memRepo) Claim(_ context.Context, owner string, lease time.Duration, limit int32) ([]Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Workspace
	for _, w := range r.rows {
		if len(out) >= int(limit) {
			break
		}
		if !r.due(w) {
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

func TestFailureMessageRedactsCredentials(t *testing.T) {
	backend := &fakeBackend{provisionErr: errors.New(
		"pull https://admin:s3cr3t@registry.example.com/v2/memoh?token=abc123: unauthorized")}
	svc, repo, _, _ := newTestService(t, backend)
	ctx := context.Background()
	_, _ = svc.EnsurePresent(ctx, bot, "img:1")
	_, _ = svc.ReconcileOnce(ctx)

	// last_error reaches the user through the bot's runtime checks, so the
	// credentials the upstream error quoted must not survive the write.
	got := repo.get(bot).LastError
	for _, secret := range []string{"s3cr3t", "abc123"} {
		if strings.Contains(got, secret) {
			t.Fatalf("last_error leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("last_error was not redacted: %s", got)
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
