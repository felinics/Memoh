// Package workspacecontext materializes workspace-owned Agent configuration in
// PostgreSQL so ordinary model turns do not need to open the workspace runtime.
package workspacecontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/config"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	"github.com/memohai/memoh/internal/hooks"
	pluginspkg "github.com/memohai/memoh/internal/plugins"
	"github.com/memohai/memoh/internal/skills"
	workspacepkg "github.com/memohai/memoh/internal/workspace"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

const (
	StatusEmpty         = "empty"
	StatusRefreshing    = "refreshing"
	StatusReady         = "ready"
	StatusSourceInvalid = "source_invalid"

	ReasonHydrate          = "hydrate"
	ReasonWorkspaceInit    = "workspace_init"
	ReasonRelevantFile     = "relevant_file"
	ReasonWorkspaceCommand = "workspace_command"
	ReasonSkillsChanged    = "skills_changed"
	ReasonPluginsChanged   = "plugins_changed"
	ReasonWorkspaceImport  = "workspace_import"
)

const defaultSnapshotMaxAge = time.Minute

var emptyHooksConfig = []byte("{\n  \"version\": 1,\n  \"enabled\": true,\n  \"hooks\": []\n}\n")

type HookDocument struct {
	Kind      string `json:"kind"`
	PluginID  string `json:"plugin_id,omitempty"`
	PluginDir string `json:"plugin_dir,omitempty"`
	Path      string `json:"path"`
	Raw       string `json:"raw"`
	Exists    bool   `json:"exists"`
}

type SystemFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

type ManifestEntry struct {
	Path        string `json:"path"`
	ContentHash string `json:"content_hash"`
}

type Payload struct {
	Version       int             `json:"version"`
	HookDocuments []HookDocument  `json:"hook_documents"`
	Skills        []skills.Entry  `json:"skills"`
	SystemFiles   []SystemFile    `json:"system_files"`
	Heartbeat     string          `json:"heartbeat"`
	Manifest      []ManifestEntry `json:"manifest"`
}

type Snapshot struct {
	BotID               string
	TargetID            string
	RequestedGeneration int64
	AppliedGeneration   int64
	Status              string
	Payload             Payload
	ContentHash         string
	LastRefreshError    string
	RefreshedAt         time.Time
}

type Store interface {
	GetBotWorkspaceContextSnapshot(ctx context.Context, arg sqlc.GetBotWorkspaceContextSnapshotParams) (sqlc.BotWorkspaceContextSnapshot, error)
	InvalidateBotWorkspaceContextSnapshots(ctx context.Context, botID pgtype.UUID) (int64, error)
	BeginBotWorkspaceContextRefresh(ctx context.Context, arg sqlc.BeginBotWorkspaceContextRefreshParams) (int64, error)
	CompleteBotWorkspaceContextRefresh(ctx context.Context, arg sqlc.CompleteBotWorkspaceContextRefreshParams) (sqlc.BotWorkspaceContextSnapshot, error)
	MarkBotWorkspaceContextSourceInvalid(ctx context.Context, arg sqlc.MarkBotWorkspaceContextSourceInvalidParams) (int64, error)
	FailBotWorkspaceContextRefresh(ctx context.Context, arg sqlc.FailBotWorkspaceContextRefreshParams) (int64, error)
}

type WorkspaceSource interface {
	bridge.Provider
	ResolveWorkspaceSkillDiscoveryRoots(ctx context.Context, botID string) ([]string, error)
	ResolveWorkspaceTargetDescriptor(ctx context.Context, botID, targetID string) (workspacepkg.WorkspaceTargetDescriptor, error)
}

type PluginInstallationLister interface {
	List(ctx context.Context, botID string) ([]pluginspkg.Installation, error)
}

type Service struct {
	logger    *slog.Logger
	store     Store
	workspace WorkspaceSource
	plugins   PluginInstallationLister

	locks sync.Map

	requestMu sync.Mutex
	requests  map[string]*refreshRequest

	now            func() time.Time
	snapshotMaxAge time.Duration
}

type refreshRequest struct {
	sequence uint64
	reason   string
}

type snapshotContextKey struct{}

type snapshotContextState struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func NewService(log *slog.Logger, store Store, workspace WorkspaceSource, plugins PluginInstallationLister) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		logger:         log.With(slog.String("service", "workspace_context")),
		store:          store,
		workspace:      workspace,
		plugins:        plugins,
		requests:       make(map[string]*refreshRequest),
		now:            time.Now,
		snapshotMaxAge: defaultSnapshotMaxAge,
	}
}

func WithSnapshot(ctx context.Context, snapshot Snapshot) context.Context {
	return context.WithValue(ctx, snapshotContextKey{}, &snapshotContextState{snapshot: snapshot})
}

func FromContext(ctx context.Context, botID, targetID string) (Snapshot, bool) {
	if ctx == nil {
		return Snapshot{}, false
	}
	state, ok := ctx.Value(snapshotContextKey{}).(*snapshotContextState)
	if !ok || state == nil {
		return Snapshot{}, false
	}
	state.mu.RLock()
	snapshot := state.snapshot
	state.mu.RUnlock()
	if !strings.EqualFold(strings.TrimSpace(snapshot.BotID), strings.TrimSpace(botID)) ||
		!strings.EqualFold(strings.TrimSpace(snapshot.TargetID), strings.TrimSpace(targetID)) {
		return Snapshot{}, false
	}
	return snapshot, true
}

func (s *Service) Attach(ctx context.Context, botID string) (context.Context, Snapshot, error) {
	snapshot, err := s.GetOrHydrate(ctx, botID)
	if err != nil {
		return ctx, Snapshot{}, err
	}
	return WithSnapshot(ctx, snapshot), snapshot, nil
}

func (s *Service) GetOrHydrate(ctx context.Context, botID string) (Snapshot, error) {
	targetID, err := s.resolveTargetID(ctx, botID)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot, ok := FromContext(ctx, botID, targetID); ok {
		usable, cachedErr := s.cachedSnapshotUsable(snapshot, true)
		if cachedErr != nil {
			return Snapshot{}, cachedErr
		}
		if usable {
			return snapshot, nil
		}
	}
	if s == nil || s.store == nil {
		return Snapshot{}, errors.New("workspace context store is not configured")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Snapshot{}, err
	}
	row, err := s.store.GetBotWorkspaceContextSnapshot(ctx, sqlc.GetBotWorkspaceContextSnapshotParams{
		BotID:    botUUID,
		TargetID: targetID,
	})
	if err == nil {
		snapshot, decodeErr := decodeSnapshot(row)
		if decodeErr != nil {
			return Snapshot{}, decodeErr
		}
		usable, cachedErr := s.cachedSnapshotUsable(snapshot, row.Payload != nil)
		if cachedErr != nil {
			return Snapshot{}, cachedErr
		}
		if usable {
			updateContextSnapshot(ctx, snapshot)
			return snapshot, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, err
	}
	return s.refreshTarget(ctx, botID, targetID, ReasonHydrate)
}

func (s *Service) cachedSnapshotUsable(snapshot Snapshot, hasPayload bool) (bool, error) {
	if snapshot.RequestedGeneration != snapshot.AppliedGeneration {
		return false, nil
	}
	if snapshot.Status == StatusReady || snapshot.Status == StatusSourceInvalid {
		now := time.Now()
		maxAge := defaultSnapshotMaxAge
		if s != nil {
			if s.now != nil {
				now = s.now()
			}
			if s.snapshotMaxAge > 0 {
				maxAge = s.snapshotMaxAge
			}
		}
		if snapshot.RefreshedAt.IsZero() || !snapshot.RefreshedAt.Add(maxAge).After(now) {
			return false, nil
		}
	}
	if err := snapshotSourceError(snapshot); err != nil {
		return false, err
	}
	return hasPayload && snapshot.Status == StatusReady, nil
}

func (s *Service) Refresh(ctx context.Context, botID, reason string) (Snapshot, error) {
	if s == nil || s.store == nil || s.workspace == nil {
		return Snapshot{}, errors.New("workspace context service is not configured")
	}
	botID = strings.TrimSpace(botID)
	targetID, err := s.resolveTargetID(ctx, botID)
	if err != nil {
		return Snapshot{}, err
	}
	return s.refreshTarget(ctx, botID, targetID, reason)
}

func (s *Service) RefreshNow(ctx context.Context, botID, reason string) error {
	_, err := s.Refresh(ctx, botID, reason)
	return err
}

func (s *Service) RefreshAllTargetsNow(ctx context.Context, botID, reason string) error {
	if s == nil || s.store == nil {
		return errors.New("workspace context service is not configured")
	}
	botUUID, err := db.ParseUUID(strings.TrimSpace(botID))
	if err != nil {
		return err
	}
	if _, err := s.store.InvalidateBotWorkspaceContextSnapshots(ctx, botUUID); err != nil {
		return err
	}
	return s.RefreshNow(ctx, botID, reason)
}

func (s *Service) RefreshIfRelevant(ctx context.Context, botID, reason string, paths ...string) error {
	for _, filePath := range paths {
		if IsRelevantPath(filePath) {
			return s.RefreshNow(ctx, botID, reason)
		}
	}
	return nil
}

func (s *Service) refreshTarget(ctx context.Context, botID, targetID, reason string) (Snapshot, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Snapshot{}, err
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return Snapshot{}, errors.New("workspace target id is required")
	}
	ctx = workspacepkg.WithWorkspaceTarget(ctx, targetID)

	lock := s.botLock(botID + "\x00" + targetID)
	lock.Lock()
	defer lock.Unlock()

	generation, err := s.store.BeginBotWorkspaceContextRefresh(ctx, sqlc.BeginBotWorkspaceContextRefreshParams{
		BotID:    botUUID,
		TargetID: targetID,
	})
	if err != nil {
		return Snapshot{}, err
	}
	markContextRefreshing(ctx, botID, targetID, generation)

	payload, scanErr := s.scan(ctx, botID)
	if scanErr != nil {
		if errors.Is(scanErr, errSourceInvalid) {
			updated, markErr := s.store.MarkBotWorkspaceContextSourceInvalid(ctx, sqlc.MarkBotWorkspaceContextSourceInvalidParams{
				BotID:             botUUID,
				TargetID:          targetID,
				AppliedGeneration: generation,
				LastRefreshError:  validText(scanErr.Error()),
			})
			if markErr != nil {
				return Snapshot{}, errors.Join(scanErr, markErr)
			}
			if updated == 0 {
				return s.awaitFreshSnapshot(ctx, botUUID, botID, targetID)
			}
			latest, latestErr := s.store.GetBotWorkspaceContextSnapshot(ctx, sqlc.GetBotWorkspaceContextSnapshotParams{
				BotID:    botUUID,
				TargetID: targetID,
			})
			if latestErr != nil {
				return Snapshot{}, errors.Join(scanErr, latestErr)
			}
			snapshot, decodeErr := decodeSnapshot(latest)
			if decodeErr != nil {
				return Snapshot{}, errors.Join(scanErr, decodeErr)
			}
			updateContextSnapshot(ctx, snapshot)
			return Snapshot{}, scanErr
		}
		updated, failErr := s.store.FailBotWorkspaceContextRefresh(ctx, sqlc.FailBotWorkspaceContextRefreshParams{
			BotID:               botUUID,
			TargetID:            targetID,
			RequestedGeneration: generation,
			LastRefreshError:    validText(scanErr.Error()),
		})
		if failErr != nil {
			return Snapshot{}, errors.Join(scanErr, failErr)
		}
		if updated == 0 {
			return s.awaitFreshSnapshot(ctx, botUUID, botID, targetID)
		}
		return Snapshot{}, scanErr
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return Snapshot{}, s.failRefresh(ctx, botUUID, targetID, generation, err)
	}
	sum := sha256.Sum256(raw)
	contentHash := hex.EncodeToString(sum[:])
	row, err := s.store.CompleteBotWorkspaceContextRefresh(ctx, sqlc.CompleteBotWorkspaceContextRefreshParams{
		BotID:             botUUID,
		TargetID:          targetID,
		AppliedGeneration: generation,
		Payload:           raw,
		ContentHash:       validText(contentHash),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.awaitFreshSnapshot(ctx, botUUID, botID, targetID)
		}
		return Snapshot{}, err
	}
	snapshot, err := decodeSnapshot(row)
	if err == nil {
		updateContextSnapshot(ctx, snapshot)
	}
	if err == nil && s.logger != nil {
		s.logger.Info("workspace context refreshed",
			slog.String("bot_id", botID),
			slog.String("target_id", targetID),
			slog.String("reason", strings.TrimSpace(reason)),
			slog.Int64("generation", generation),
			slog.Int("skills", len(payload.Skills)),
		)
	}
	return snapshot, err
}

func (s *Service) awaitFreshSnapshot(ctx context.Context, botUUID pgtype.UUID, botID, targetID string) (Snapshot, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		row, err := s.store.GetBotWorkspaceContextSnapshot(ctx, sqlc.GetBotWorkspaceContextSnapshotParams{
			BotID:    botUUID,
			TargetID: targetID,
		})
		if err != nil {
			return Snapshot{}, err
		}
		snapshot, err := decodeSnapshot(row)
		if err != nil {
			return Snapshot{}, err
		}
		updateContextSnapshot(ctx, snapshot)
		if err := snapshotSourceError(snapshot); err != nil {
			return Snapshot{}, err
		}
		if row.Payload != nil &&
			snapshot.Status == StatusReady &&
			snapshot.RequestedGeneration == snapshot.AppliedGeneration {
			return snapshot, nil
		}
		if snapshot.Status != StatusRefreshing {
			return Snapshot{}, fmt.Errorf(
				"workspace context refresh was superseded before generation %d was applied for bot %s target %s",
				snapshot.RequestedGeneration,
				strings.TrimSpace(botID),
				strings.TrimSpace(targetID),
			)
		}
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) RequestRefresh(ctx context.Context, botID, reason string) {
	if s == nil || s.workspace == nil {
		return
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return
	}
	targetID, err := s.resolveTargetID(ctx, botID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("workspace context refresh target resolution failed",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
		return
	}
	requestKey := botID + "\x00" + targetID
	s.requestMu.Lock()
	state, running := s.requests[requestKey]
	if !running {
		state = &refreshRequest{}
		s.requests[requestKey] = state
	}
	state.sequence++
	state.reason = strings.TrimSpace(reason)
	sequence := state.sequence
	s.requestMu.Unlock()
	if running {
		return
	}

	refreshCtx := context.WithoutCancel(ctx)
	go func() {
		for {
			s.requestMu.Lock()
			current := s.requests[requestKey]
			sequence = current.sequence
			reason = current.reason
			s.requestMu.Unlock()

			attemptCtx, cancel := context.WithTimeout(refreshCtx, 2*time.Minute)
			_, err := s.refreshTarget(attemptCtx, botID, targetID, reason)
			cancel()
			if err != nil && s.logger != nil {
				s.logger.Warn("workspace context refresh failed",
					slog.String("bot_id", botID),
					slog.String("target_id", targetID),
					slog.String("reason", reason),
					slog.Any("error", err),
				)
			}

			s.requestMu.Lock()
			current = s.requests[requestKey]
			if current.sequence == sequence {
				delete(s.requests, requestKey)
				s.requestMu.Unlock()
				return
			}
			s.requestMu.Unlock()
		}
	}()
}

func snapshotSourceError(snapshot Snapshot) error {
	if snapshot.Status != StatusSourceInvalid {
		return nil
	}
	detail := strings.TrimSpace(snapshot.LastRefreshError)
	if detail == "" {
		detail = "invalid workspace context source"
	}
	return fmt.Errorf("workspace context source is invalid: %s", detail)
}

func updateContextSnapshot(ctx context.Context, snapshot Snapshot) {
	if ctx == nil {
		return
	}
	state, ok := ctx.Value(snapshotContextKey{}).(*snapshotContextState)
	if !ok || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !strings.EqualFold(strings.TrimSpace(state.snapshot.BotID), strings.TrimSpace(snapshot.BotID)) ||
		!strings.EqualFold(strings.TrimSpace(state.snapshot.TargetID), strings.TrimSpace(snapshot.TargetID)) {
		return
	}
	state.snapshot = snapshot
}

func markContextRefreshing(ctx context.Context, botID, targetID string, generation int64) {
	if ctx == nil {
		return
	}
	state, ok := ctx.Value(snapshotContextKey{}).(*snapshotContextState)
	if !ok || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !strings.EqualFold(strings.TrimSpace(state.snapshot.BotID), strings.TrimSpace(botID)) ||
		!strings.EqualFold(strings.TrimSpace(state.snapshot.TargetID), strings.TrimSpace(targetID)) {
		return
	}
	state.snapshot.RequestedGeneration = generation
	state.snapshot.Status = StatusRefreshing
}

func (s *Service) LoadEffectiveHooks(ctx context.Context, botID string) (hooks.Config, bool, error) {
	snapshot, err := s.GetOrHydrate(ctx, botID)
	if err != nil {
		return hooks.Config{}, false, err
	}
	var user hooks.Config
	var userExists bool
	pluginHooks := make([]hooks.Hook, 0)
	for _, document := range snapshot.Payload.HookDocuments {
		cfg, parseErr := hooks.ParseConfig([]byte(document.Raw))
		if parseErr != nil {
			if document.Kind == "plugin" {
				if s.logger != nil {
					s.logger.Warn("skipping invalid cached plugin hooks",
						slog.String("bot_id", botID),
						slog.String("plugin_id", document.PluginID),
						slog.Any("error", parseErr),
					)
				}
				continue
			}
			return hooks.Config{}, document.Exists, parseErr
		}
		if document.Kind == "plugin" {
			pluginHooks = append(pluginHooks, hooks.BuildPluginHooks(document.PluginID, document.PluginDir, cfg)...)
			continue
		}
		user = cfg
		userExists = document.Exists
	}
	return hooks.BuildEffectiveConfig(user, pluginHooks), userExists, nil
}

func (s *Service) Skills(ctx context.Context, botID string, effectiveOnly bool) ([]skills.Entry, error) {
	snapshot, err := s.GetOrHydrate(ctx, botID)
	if err != nil {
		return nil, err
	}
	out := make([]skills.Entry, 0, len(snapshot.Payload.Skills))
	for _, entry := range snapshot.Payload.Skills {
		if effectiveOnly && entry.State != skills.StateEffective {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *Service) SystemFiles(ctx context.Context, botID string) ([]SystemFile, error) {
	snapshot, err := s.GetOrHydrate(ctx, botID)
	if err != nil {
		return nil, err
	}
	return append([]SystemFile(nil), snapshot.Payload.SystemFiles...), nil
}

func (s *Service) HeartbeatChecklist(ctx context.Context, botID string) (string, error) {
	snapshot, err := s.GetOrHydrate(ctx, botID)
	if err != nil {
		return "", err
	}
	return snapshot.Payload.Heartbeat, nil
}

func IsRelevantPath(filePath string) bool {
	filePath = strings.TrimSpace(strings.ReplaceAll(filePath, "\\", "/"))
	if filePath == "" {
		return false
	}
	if !path.IsAbs(filePath) {
		filePath = path.Join(config.DefaultDataMount, filePath)
	}
	filePath = path.Clean(filePath)
	for _, exactPath := range []string{
		hooks.DefaultConfigPath,
		path.Join(config.DefaultDataMount, "AGENTS.md"),
		path.Join(config.DefaultDataMount, "HEARTBEAT.md"),
		path.Join(config.DefaultDataMount, "MEMORY.md"),
		path.Join(config.DefaultDataMount, "PROFILES.md"),
	} {
		if filePath == exactPath || strings.HasPrefix(exactPath, strings.TrimRight(filePath, "/")+"/") {
			return true
		}
	}
	for _, root := range []string{
		skills.ManagedDirPath,
		skills.LegacyDirPath,
		skills.IndexDirPath,
		skills.PluginDirPath,
	} {
		root = strings.TrimRight(root, "/")
		if filePath == root ||
			strings.HasPrefix(filePath, root+"/") ||
			strings.HasPrefix(root, strings.TrimRight(filePath, "/")+"/") {
			return true
		}
	}
	return path.Base(filePath) == "SKILL.md"
}

var errSourceInvalid = errors.New("workspace context source is invalid")

func (s *Service) scan(ctx context.Context, botID string) (Payload, error) {
	installations, err := s.listPlugins(ctx, botID)
	if err != nil {
		return Payload{}, err
	}
	client, err := s.workspace.MCPClient(ctx, botID)
	if err != nil {
		return Payload{}, fmt.Errorf("open workspace for context refresh: %w", err)
	}

	payload := Payload{Version: 1}
	userRaw, userExists, err := readRawOptional(ctx, client, hooks.DefaultConfigPath)
	if err != nil {
		return Payload{}, err
	}
	if !userExists {
		userRaw = string(emptyHooksConfig)
	}
	if _, err := hooks.ParseConfig([]byte(userRaw)); err != nil {
		return Payload{}, fmt.Errorf("%w: %s: %w", errSourceInvalid, hooks.DefaultConfigPath, err)
	}
	payload.HookDocuments = append(payload.HookDocuments, HookDocument{
		Kind:   "user",
		Path:   hooks.DefaultConfigPath,
		Raw:    userRaw,
		Exists: userExists,
	})
	payload.Manifest = append(payload.Manifest, manifestEntry(hooks.DefaultConfigPath, userRaw))

	pluginRoots := make([]string, 0, len(installations))
	for _, installation := range installations {
		if !installation.Enabled || installation.Status == pluginspkg.StatusUninstalled {
			continue
		}
		root, rootErr := skills.PluginSkillsDirForID(installation.PluginID)
		if rootErr == nil {
			pluginRoots = append(pluginRoots, root)
		}
		if installation.Status != pluginspkg.StatusReady {
			continue
		}
		pluginDir, dirErr := skills.PluginDirForID(installation.PluginID)
		hooksPath, pathErr := skills.PluginHooksPathForID(installation.PluginID)
		if dirErr != nil || pathErr != nil {
			continue
		}
		raw, exists, readErr := readRawOptional(ctx, client, hooksPath)
		if readErr != nil {
			return Payload{}, readErr
		}
		if !exists {
			continue
		}
		payload.HookDocuments = append(payload.HookDocuments, HookDocument{
			Kind:      "plugin",
			PluginID:  installation.PluginID,
			PluginDir: pluginDir,
			Path:      hooksPath,
			Raw:       raw,
			Exists:    true,
		})
		payload.Manifest = append(payload.Manifest, manifestEntry(hooksPath, raw))
	}

	roots, err := s.workspace.ResolveWorkspaceSkillDiscoveryRoots(ctx, botID)
	if err != nil {
		return Payload{}, err
	}
	payload.Skills = skills.ScanWithPluginRoots(ctx, client, roots, pluginRoots)
	for _, entry := range payload.Skills {
		payload.Manifest = append(payload.Manifest, manifestEntry(entry.SourcePath, entry.Raw))
	}

	for _, name := range []string{"AGENTS.md", "MEMORY.md", "PROFILES.md"} {
		filePath := path.Join(config.DefaultDataMount, name)
		content, exists, readErr := readTextOptional(ctx, client, filePath)
		if readErr != nil {
			return Payload{}, readErr
		}
		if !exists {
			content = ""
		}
		content = strings.TrimSpace(content)
		payload.SystemFiles = append(payload.SystemFiles, SystemFile{Filename: name, Content: content})
		payload.Manifest = append(payload.Manifest, manifestEntry(filePath, content))
	}
	heartbeatPath := path.Join(config.DefaultDataMount, "HEARTBEAT.md")
	heartbeat, exists, err := readTextOptional(ctx, client, heartbeatPath)
	if err != nil {
		return Payload{}, err
	}
	if !exists {
		heartbeat = ""
	}
	payload.Heartbeat = strings.TrimSpace(heartbeat)
	payload.Manifest = append(payload.Manifest, manifestEntry(heartbeatPath, payload.Heartbeat))
	return payload, nil
}

func (s *Service) listPlugins(ctx context.Context, botID string) ([]pluginspkg.Installation, error) {
	if s.plugins == nil {
		return nil, nil
	}
	return s.plugins.List(ctx, botID)
}

func (s *Service) botLock(botID string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(botID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) failRefresh(ctx context.Context, botID pgtype.UUID, targetID string, generation int64, cause error) error {
	_, err := s.store.FailBotWorkspaceContextRefresh(ctx, sqlc.FailBotWorkspaceContextRefreshParams{
		BotID:               botID,
		TargetID:            targetID,
		RequestedGeneration: generation,
		LastRefreshError:    validText(cause.Error()),
	})
	return errors.Join(cause, err)
}

func decodeSnapshot(row sqlc.BotWorkspaceContextSnapshot) (Snapshot, error) {
	var payload Payload
	if len(row.Payload) > 0 {
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return Snapshot{}, fmt.Errorf("decode workspace context snapshot: %w", err)
		}
	}
	return Snapshot{
		BotID:               uuidString(row.BotID),
		TargetID:            row.TargetID,
		RequestedGeneration: row.RequestedGeneration,
		AppliedGeneration:   row.AppliedGeneration,
		Status:              row.Status,
		Payload:             payload,
		ContentHash:         db.TextToString(row.ContentHash),
		LastRefreshError:    db.TextToString(row.LastRefreshError),
		RefreshedAt:         db.TimeFromPg(row.RefreshedAt),
	}, nil
}

func (s *Service) resolveTargetID(ctx context.Context, botID string) (string, error) {
	if targetID := workspacepkg.WorkspaceTargetFromContext(ctx); targetID != "" {
		return targetID, nil
	}
	if s == nil || s.workspace == nil {
		return workspacepkg.WorkspaceTargetNative, nil
	}
	descriptor, err := s.workspace.ResolveWorkspaceTargetDescriptor(ctx, strings.TrimSpace(botID), "")
	if err != nil {
		return "", err
	}
	targetID := strings.TrimSpace(descriptor.TargetID)
	if targetID == "" {
		return "", errors.New("workspace target descriptor has no target id")
	}
	return targetID, nil
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func validText(value string) pgtype.Text {
	return pgtype.Text{String: strings.TrimSpace(value), Valid: true}
}

func readRawOptional(ctx context.Context, client *bridge.Client, filePath string) (string, bool, error) {
	rc, err := client.ReadRaw(ctx, filePath)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

func readTextOptional(ctx context.Context, client *bridge.Client, filePath string) (string, bool, error) {
	resp, err := client.ReadFile(ctx, filePath, 0, 0)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return resp.GetContent(), true, nil
}

func manifestEntry(filePath, content string) ManifestEntry {
	sum := sha256.Sum256([]byte(content))
	return ManifestEntry{
		Path:        path.Clean(filePath),
		ContentHash: hex.EncodeToString(sum[:]),
	}
}
