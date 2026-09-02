package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// DefaultUpdateCheckInterval is how often the update worker runs a round
// (WD-UPD-001).
const DefaultUpdateCheckInterval = 24 * time.Hour

// AlignmentScan re-discovers the native workspace of every given bot and
// returns how many agent dependencies need aligning with their pin (design
// §10.1, WD-UPD-A01). Bots whose workspace is not running are skipped; a
// bot that fails to scan does not stop the others, and the joined errors
// are returned next to the count.
func (s *Service) AlignmentScan(ctx context.Context, bots []string) (int, error) {
	pending := 0
	var errs []error
	for _, botID := range bots {
		result, err := s.Refresh(ctx, botID, TargetNative)
		if err != nil {
			errs = append(errs, fmt.Errorf("workspacedeps: alignment scan of bot %s: %w", botID, err))
			continue
		}
		if result.Workspace != WorkspaceRunning {
			continue
		}
		for _, entry := range result.Entries {
			if entry.NeedsAlignment {
				pending++
			}
		}
	}
	return pending, errors.Join(errs...)
}

// UpdateWorker periodically runs check_update for installed tool
// dependencies (design §10.2). Only running native workspaces are checked;
// remote targets are never woken (WD-UPD-002, WD-PLAT-003).
type UpdateWorker struct {
	service  *Service
	interval time.Duration
	logger   *slog.Logger
	// ctxFactory derives the context each round runs under; the default is
	// the identity. Team scoping needs nothing here: the PostgreSQL store
	// binds the team per pooled connection (see store_pg.go), so a plain
	// background context behaves like a request context. The seam exists for
	// per-round deadlines or tracing.
	ctxFactory func(context.Context) context.Context
	// newTicker is replaced by tests to drive rounds manually.
	newTicker func(time.Duration) (<-chan time.Time, func())

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewUpdateWorker returns a stopped worker. A non-positive interval selects
// DefaultUpdateCheckInterval.
func NewUpdateWorker(s *Service, interval time.Duration, logger *slog.Logger) *UpdateWorker {
	if s == nil {
		panic("workspacedeps: update worker needs a service")
	}
	if interval <= 0 {
		interval = DefaultUpdateCheckInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &UpdateWorker{
		service:    s,
		interval:   interval,
		logger:     logger.With(slog.String("component", "workspacedeps_update_worker")),
		ctxFactory: func(ctx context.Context) context.Context { return ctx },
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(d)
			return ticker.C, ticker.Stop
		},
	}
}

// SetContextFactory installs the function that binds each round's context
// (typically to the team). It must be called before Start.
func (w *UpdateWorker) SetContextFactory(fn func(context.Context) context.Context) {
	if fn == nil {
		return
	}
	w.ctxFactory = fn
}

// Start begins ticking in the background. The loop detaches from ctx's
// cancellation and runs until Stop; starting twice is a no-op.
func (w *UpdateWorker) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel = cancel
	w.done = make(chan struct{})
	go w.loop(loopCtx, w.done)
}

// Stop ends the loop and waits for a round in flight to finish.
func (w *UpdateWorker) Stop() {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	w.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (w *UpdateWorker) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticks, stop := w.newTicker(w.interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			checks, err := w.RunOnce(ctx)
			if err != nil {
				w.logger.Warn("dependency update check round finished with errors",
					slog.Int("checks", checks),
					slog.Any("error", err),
				)
			}
		}
	}
}

// checkGroup is one upstream query shared by every record with the same
// dependency and platform (WD-UPD-003).
type checkGroup struct {
	dep      groupKey
	client   *bridge.Client
	dataRoot string
	platform Platform
	members  []Installation
}

type groupKey struct {
	depID string
	os    string
	arch  string
	libc  string
}

// RunOnce performs one round: installed, unpinned tool dependencies on
// running native workspaces are grouped by (dependency, platform), each group
// runs check_update once, and the result fans out to every member. It
// returns how many upstream checks ran.
func (w *UpdateWorker) RunOnce(ctx context.Context) (int, error) {
	ctx = w.ctxFactory(ctx)
	s := w.service
	records, err := s.store.ListByStatus(ctx, StatusInstalled)
	if err != nil {
		return 0, fmt.Errorf("workspacedeps: list installed dependencies: %w", err)
	}
	groups, order, errs := w.groupRecords(ctx, records)
	checks := 0
	for _, key := range order {
		group := groups[key]
		dep, _ := s.catalog.Get(key.depID)
		check, checkErr := s.checkUpdate(ctx, group.client, group.dataRoot, group.platform, dep, group.members[0].InstalledVersion)
		checks++
		for _, rec := range group.members {
			recKey := InstallationKey{BotID: rec.BotID, WorkspaceTargetID: rec.WorkspaceTargetID, DependencyID: rec.DependencyID}
			if err := s.recordCheck(ctx, recKey, check, checkErr); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return checks, errors.Join(errs...)
}

// groupRecords selects the records a round must check and buckets them.
// Workspace state and platform are resolved once per bot.
func (w *UpdateWorker) groupRecords(ctx context.Context, records []Installation) (map[groupKey]*checkGroup, []groupKey, []error) {
	s := w.service
	type botTarget struct {
		client   *bridge.Client
		dataRoot string
		platform Platform
		running  bool
	}
	targets := make(map[string]botTarget)
	groups := make(map[groupKey]*checkGroup)
	var order []groupKey
	var errs []error

	for _, rec := range records {
		dep, ok := s.catalog.Get(rec.DependencyID)
		if !ok || !upstreamCheckable(dep) || !isNativeTarget(rec.WorkspaceTargetID) {
			continue
		}
		target, seen := targets[rec.BotID]
		if !seen {
			resolved, err := w.resolveNative(ctx, rec.BotID)
			if err != nil {
				errs = append(errs, err)
				targets[rec.BotID] = botTarget{}
				continue
			}
			if resolved != nil {
				target = botTarget{client: resolved.client, dataRoot: resolved.dataRoot, platform: resolved.platform, running: true}
			}
			targets[rec.BotID] = target
		}
		if !target.running {
			continue
		}
		key := groupKey{depID: dep.ID, os: target.platform.OS, arch: target.platform.Arch, libc: target.platform.Libc}
		group, exists := groups[key]
		if !exists {
			group = &checkGroup{dep: key, client: target.client, dataRoot: target.dataRoot, platform: target.platform}
			groups[key] = group
			order = append(order, key)
		}
		group.members = append(group.members, rec)
	}
	return groups, order, errs
}

type resolvedNative struct {
	client   *bridge.Client
	dataRoot string
	platform Platform
}

// resolveNative returns the bot's native workspace when it is running, nil
// when it is stopped or missing.
func (w *UpdateWorker) resolveNative(ctx context.Context, botID string) (*resolvedNative, error) {
	s := w.service
	state, err := s.workspace.State(ctx, botID, TargetNative)
	if err != nil {
		return nil, fmt.Errorf("workspacedeps: workspace state of bot %s: %w", botID, err)
	}
	if state != WorkspaceRunning {
		return nil, nil
	}
	client, dataRoot, err := s.target(ctx, botID, TargetNative)
	if err != nil {
		return nil, fmt.Errorf("workspacedeps: bot %s: %w", botID, err)
	}
	platform, err := s.platformFor(ctx, botID, TargetNative, client)
	if err != nil {
		return nil, fmt.Errorf("workspacedeps: probe platform of bot %s: %w", botID, err)
	}
	return &resolvedNative{client: client, dataRoot: dataRoot, platform: platform}, nil
}
