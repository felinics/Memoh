package claudecode

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	// A failed probe is retried after this long; one that succeeded holds
	// until /reload-skills, as skills on disk do in a Claude session.
	commandProbeRetry   = time.Minute
	commandProbeTimeout = 30 * time.Second
)

type observedCommands struct {
	commands []initializeCommand
	skills   []string
}

// commandCatalog keeps what the CLI last reported for each workspace project.
// Skills belong to the workspace and the project directory, not to a session,
// so a session before its first turn, or a draft with none, reads them here.
// Turns do not feed it: system/init counts more commands as skills than
// reload_skills, which is what a turn admits a command against.
type commandCatalog struct {
	probe  func(context.Context, external.PromptInput) (observedCommands, error)
	logger *slog.Logger

	mu      sync.Mutex
	entries map[commandCatalogKey]commandCatalogEntry
	flight  singleflight.Group
}

// An observation is only as good as the CLI that made it: the workspace
// target the context pins, the Agent's configuration, and the cwd that
// decides which project skills load.
type commandCatalogKey struct {
	target, botID, botAgentID, projectPath string
}

func (k commandCatalogKey) flightKey() string {
	return strings.Join([]string{k.target, k.botID, k.botAgentID, k.projectPath}, "\x00")
}

type commandCatalogEntry struct {
	observed   observedCommands
	ok         bool
	observedAt time.Time
}

func newCommandCatalog(probe func(context.Context, external.PromptInput) (observedCommands, error), logger *slog.Logger) *commandCatalog {
	return &commandCatalog{probe: probe, logger: logger, entries: map[commandCatalogKey]commandCatalogEntry{}}
}

func commandCatalogInputKey(ctx context.Context, input external.PromptInput) commandCatalogKey {
	return commandCatalogKey{
		target: bridge.WorkspaceTargetFromContext(ctx), botID: input.BotID, botAgentID: input.BotAgentID,
		projectPath: firstNonEmpty(strings.TrimSpace(metadataString(input.RuntimeMetadata, "project_path")), defaultProjectPath),
	}
}

// lookup never waits for a CLI: sending a message and reading permission modes
// list commands too. A missing observation starts a probe in the background,
// and the composer's polling picks up its result. A failed probe is retried
// only after a while so polling does not start a CLI every read.
func (c *commandCatalog) lookup(ctx context.Context, input external.PromptInput) (observedCommands, bool) {
	key := commandCatalogInputKey(ctx, input)
	entry := c.entry(key)
	if entry.observedAt.IsZero() || (!entry.ok && time.Since(entry.observedAt) >= commandProbeRetry) {
		c.flight.DoChan(key.flightKey(), func() (any, error) {
			c.refresh(context.WithoutCancel(ctx), key, input)
			return nil, nil
		})
	}
	return entry.observed, entry.ok
}

// forget drops every observation of the bot, so each workspace project it
// reads next is probed again.
func (c *commandCatalog) forget(botID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if key.botID == botID {
			delete(c.entries, key)
		}
	}
}

func (c *commandCatalog) refresh(ctx context.Context, key commandCatalogKey, input external.PromptInput) {
	ctx, cancel := context.WithTimeout(ctx, commandProbeTimeout)
	defer cancel()
	observed, err := c.probe(ctx, input)
	// A workspace that cannot run the CLI fails every probe; that is its state, not a fault.
	if err != nil && !errors.Is(err, external.ErrDependencyMissing) && !errors.Is(err, external.ErrContainerWorkspaceRequired) {
		c.logger.WarnContext(ctx, "claude command discovery failed", slog.String("bot_id", input.BotID), slog.Any("error", err))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// After a failure an older observation would outrank what the session's
	// own turns recorded since.
	c.entries[key] = commandCatalogEntry{observed: observed, ok: err == nil, observedAt: time.Now()}
}

func (c *commandCatalog) entry(key commandCatalogKey) commandCatalogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[key]
}

// discoverCommands asks a CLI that is never given a prompt: initialize lists
// the invocable commands and reload_skills tells which of them are skills.
func (d *Driver) discoverCommands(ctx context.Context, input external.PromptInput) (observedCommands, error) {
	_, turn, stop, err := d.startControlCLI(ctx, input.BotID, input.BotAgentID, metadataString(input.RuntimeMetadata, "project_path"))
	if err != nil {
		return observedCommands{}, err
	}
	defer stop()
	response, err := turn.initialize(ctx)
	if err != nil {
		return observedCommands{}, err
	}
	skills, err := turn.reloadSkillNames(ctx)
	if err != nil {
		return observedCommands{}, err
	}
	return observedCommands{commands: response.Commands, skills: skills}, nil
}
