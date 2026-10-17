package hooks

import (
	"context"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

type turnSnapshotKey struct{}

type turnSnapshot struct {
	mu         sync.Mutex
	entries    map[string]*snapshotEntry
	workspaces map[string]*workspaceEntry
}

type snapshotEntry struct {
	once   sync.Once
	cfg    Config
	exists bool
	err    error
}

type workspaceEntry struct {
	once sync.Once
	info bridge.WorkspaceInfo
	err  error
}

// WithTurnSnapshot scopes hooks configuration loading to one chat turn: the
// first load for a bot and workspace target is reused by every later hook
// event of the turn, including a load failure. It is idempotent.
func WithTurnSnapshot(ctx context.Context) context.Context {
	if turnSnapshotFrom(ctx) != nil {
		return ctx
	}
	return withNewTurnSnapshot(ctx)
}

func withNewTurnSnapshot(ctx context.Context) context.Context {
	return context.WithValue(ctx, turnSnapshotKey{}, &turnSnapshot{
		entries:    map[string]*snapshotEntry{},
		workspaces: map[string]*workspaceEntry{},
	})
}

// WithAuxiliaryTurn scopes auxiliary loading to one chat turn: the hooks
// configuration and the workspace description are read once, and a workspace
// found unavailable by one stage is skipped by the later ones instead of each
// waiting for it again. It is idempotent, so every stage of a turn can call it
// and still share the scope the turn opened first.
func WithAuxiliaryTurn(ctx context.Context) context.Context {
	return WithTurnSnapshot(bridge.WithWorkspaceAvailability(ctx))
}

// WithNewAuxiliaryTurn starts a turn scope of its own even inside an existing
// one. Work that outlives the turn it was started from, such as a background
// task, uses it so neither inherits the other's verdict or snapshot.
func WithNewAuxiliaryTurn(ctx context.Context) context.Context {
	return withNewTurnSnapshot(bridge.WithNewWorkspaceAvailability(ctx))
}

// TurnWorkspaceInfo describes the workspace a hook request reports. Within a
// turn the provider is asked at most once per bot and workspace target, the
// read is bounded by the auxiliary timeout, and a workspace the turn already
// found unavailable is not asked at all. Whatever cannot be read falls back
// to the default container workspace.
func TurnWorkspaceInfo(ctx context.Context, provider bridge.Provider, botID string) WorkspaceInfo {
	info := WorkspaceInfo{
		CWD:     DefaultWorkDir,
		Runtime: bridge.WorkspaceBackendContainer,
	}
	resolver, ok := provider.(bridge.WorkspaceInfoProvider)
	if !ok {
		return info
	}
	load := func() (bridge.WorkspaceInfo, error) {
		return loadWorkspaceInfo(ctx, resolver, botID)
	}
	var (
		workspaceInfo bridge.WorkspaceInfo
		err           error
	)
	if snapshot := turnSnapshotFrom(ctx); snapshot != nil {
		workspaceInfo, err = snapshot.loadWorkspace(strings.TrimSpace(botID)+"\x00"+bridge.WorkspaceTargetFromContext(ctx), load)
	} else {
		workspaceInfo, err = load()
	}
	if err != nil {
		return info
	}
	if strings.TrimSpace(workspaceInfo.DefaultWorkDir) != "" {
		info.CWD = workspaceInfo.DefaultWorkDir
	}
	if strings.TrimSpace(workspaceInfo.Backend) != "" {
		info.Runtime = workspaceInfo.Backend
	}
	return info
}

func loadWorkspaceInfo(ctx context.Context, resolver bridge.WorkspaceInfoProvider, botID string) (bridge.WorkspaceInfo, error) {
	if bridge.WorkspaceUnavailableFromContext(ctx) {
		return bridge.WorkspaceInfo{}, bridge.ErrUnavailable
	}
	loadCtx, cancel := bridge.AuxiliaryContext(ctx)
	defer cancel()
	info, err := resolver.WorkspaceInfo(loadCtx, botID)
	if err != nil {
		bridge.MarkWorkspaceUnavailableOn(ctx, err)
	}
	return info, err
}

func turnSnapshotFrom(ctx context.Context) *turnSnapshot {
	if ctx == nil {
		return nil
	}
	snapshot, _ := ctx.Value(turnSnapshotKey{}).(*turnSnapshot)
	return snapshot
}

func (t *turnSnapshot) load(key string, load func() (Config, bool, error)) (Config, bool, error) {
	t.mu.Lock()
	entry := t.entries[key]
	if entry == nil {
		entry = &snapshotEntry{}
		t.entries[key] = entry
	}
	t.mu.Unlock()
	entry.once.Do(func() {
		entry.cfg, entry.exists, entry.err = load()
	})
	return entry.cfg.clone(), entry.exists, entry.err
}

func (t *turnSnapshot) loadWorkspace(key string, load func() (bridge.WorkspaceInfo, error)) (bridge.WorkspaceInfo, error) {
	t.mu.Lock()
	entry := t.workspaces[key]
	if entry == nil {
		entry = &workspaceEntry{}
		t.workspaces[key] = entry
	}
	t.mu.Unlock()
	entry.once.Do(func() {
		entry.info, entry.err = load()
	})
	return entry.info, entry.err
}

// clone copies what running a configuration mutates (applyDefaults fills in
// hook and action fields), so concurrent hook events of a turn never share it.
func (c Config) clone() Config {
	c.Env = cloneStringMap(c.Env)
	if c.Hooks != nil {
		hooks := make([]Hook, len(c.Hooks))
		for i, hook := range c.Hooks {
			if hook.Actions != nil {
				hook.Actions = append([]HookAction(nil), hook.Actions...)
			}
			hooks[i] = hook
		}
		c.Hooks = hooks
	}
	return c
}
