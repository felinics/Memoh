package workdir

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	"github.com/felinics/memoh/internal/workspace/vpath"
)

// targetResolver is the slice of *workspace.Manager the workdir service
// needs: resolving a target address to a live bridge client and workspace
// info, for directory validation at create time.
type targetResolver interface {
	ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (workspace.ResolvedWorkspaceTarget, error)
}

// Service owns per-bot working directories.
type Service struct {
	store   dbstore.BotWorkdirStore
	targets targetResolver
}

func NewService(store dbstore.BotWorkdirStore, manager *workspace.Manager) *Service {
	return &Service{store: store, targets: manager}
}

// Create validates the target and directory, then persists the workdir.
// The directory must already exist on the target: a mistyped path should
// fail here, at the creation site, not on the first agent turn.
func (s *Service) Create(ctx context.Context, botID, userID string, req CreateRequest) (Workdir, error) {
	if s == nil || s.store == nil {
		return Workdir{}, errors.New("workdir service not configured")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Workdir{}, ErrNameRequired
	}
	targetID := strings.TrimSpace(req.WorkspaceTargetID)
	if targetID == "" {
		targetID = workspace.WorkspaceTargetNative
	}
	resolved, err := s.targets.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
		return Workdir{}, err
	}
	normalized, err := normalizeWorkdirPath(req.Path, resolved)
	if err != nil {
		return Workdir{}, err
	}
	if err := s.requireDirectory(ctx, resolved, normalized); err != nil {
		return Workdir{}, err
	}
	input := dbstore.CreateBotWorkdirInput{
		BotID:           botID,
		Name:            name,
		TargetKind:      resolved.Kind,
		Path:            normalized,
		CreatedByUserID: strings.TrimSpace(userID),
	}
	if resolved.Kind == TargetKindRemote {
		input.RemoteBindingID = resolved.TargetID
	}
	record, err := s.store.CreateWorkdir(ctx, input)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Workdir{}, ErrDuplicatePath
		}
		return Workdir{}, err
	}
	return toWorkdir(record), nil
}

func (s *Service) List(ctx context.Context, botID string, includeArchived bool) ([]Workdir, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("workdir service not configured")
	}
	records, err := s.store.ListWorkdirs(ctx, botID, includeArchived)
	if err != nil {
		return nil, err
	}
	workdirs := make([]Workdir, 0, len(records))
	for _, record := range records {
		workdirs = append(workdirs, toWorkdir(record))
	}
	return workdirs, nil
}

// Get returns the workdir regardless of archive state: sessions bound to an
// archived workdir keep resolving their working directory.
func (s *Service) Get(ctx context.Context, botID, workdirID string) (Workdir, error) {
	if s == nil || s.store == nil {
		return Workdir{}, errors.New("workdir service not configured")
	}
	record, err := s.store.GetWorkdir(ctx, botID, workdirID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Workdir{}, ErrWorkdirNotFound
		}
		return Workdir{}, err
	}
	return toWorkdir(record), nil
}

// RequireActive is Get plus an archive check — the gate for binding new
// sessions to a workdir.
func (s *Service) RequireActive(ctx context.Context, botID, workdirID string) (Workdir, error) {
	record, err := s.Get(ctx, botID, workdirID)
	if err != nil {
		return Workdir{}, err
	}
	if record.Archived {
		return Workdir{}, ErrWorkdirArchived
	}
	return record, nil
}

func (s *Service) Rename(ctx context.Context, botID, workdirID, name string) (Workdir, error) {
	if s == nil || s.store == nil {
		return Workdir{}, errors.New("workdir service not configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Workdir{}, ErrNameRequired
	}
	record, err := s.store.RenameWorkdir(ctx, botID, workdirID, name)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Workdir{}, ErrWorkdirNotFound
		}
		return Workdir{}, err
	}
	return toWorkdir(record), nil
}

// Archive soft-deletes the workdir. Bound sessions keep their working
// directory (the row stays); only new bindings are refused from here on.
func (s *Service) Archive(ctx context.Context, botID, workdirID string) error {
	if s == nil || s.store == nil {
		return errors.New("workdir service not configured")
	}
	if err := s.store.ArchiveWorkdir(ctx, botID, workdirID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrWorkdirNotFound
		}
		return err
	}
	return nil
}

// requireDirectory stats the normalized path on the resolved target and
// requires an existing directory.
func (*Service) requireDirectory(ctx context.Context, target workspace.ResolvedWorkspaceTarget, normalized string) error {
	if target.Client == nil {
		return errors.New("workspace target is not reachable")
	}
	entry, err := target.Client.Stat(ctx, normalized)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrPathNotFound, normalized)
		}
		return fmt.Errorf("stat workdir path: %w", err)
	}
	if !entry.GetIsDir() {
		return fmt.Errorf("%w: %s", ErrPathNotDirectory, normalized)
	}
	return nil
}

// normalizeWorkdirPath canonicalizes the user-supplied path for the target
// it will live on. Native paths are clamped under the container data mount;
// remote paths are host-absolute (Windows drive/UNC paths included) and the
// remote runtime stays the authority on what exists there.
func normalizeWorkdirPath(raw string, target workspace.ResolvedWorkspaceTarget) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrPathRequired
	}
	if target.Kind == TargetKindRemote {
		if isWindowsOS(target.Info.OS) {
			return normalizeWindowsAbsPath(trimmed)
		}
		if !strings.HasPrefix(trimmed, "/") {
			return "", fmt.Errorf("%w: remote path must be absolute", ErrInvalidPath)
		}
		return path.Clean(trimmed), nil
	}
	normalized, err := vpath.ResolveUnderRoot(vpath.DataMount, trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}
	return normalized, nil
}

func isWindowsOS(os string) bool {
	return strings.EqualFold(strings.TrimSpace(os), "win32")
}

// normalizeWindowsAbsPath accepts drive-letter (C:\x or C:/x) and UNC
// (\\server\share) paths, normalizes separators to backslashes, and trims a
// trailing separator except on a bare drive root.
func normalizeWindowsAbsPath(raw string) (string, error) {
	normalized := strings.ReplaceAll(raw, "/", `\`)
	isUNC := strings.HasPrefix(normalized, `\\`)
	isDrive := len(normalized) >= 3 &&
		isASCIILetter(normalized[0]) && normalized[1] == ':' && normalized[2] == '\\'
	if !isUNC && !isDrive {
		return "", fmt.Errorf("%w: remote path must be absolute", ErrInvalidPath)
	}
	for len(normalized) > 3 && strings.HasSuffix(normalized, `\`) {
		normalized = strings.TrimSuffix(normalized, `\`)
	}
	return normalized, nil
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func toWorkdir(record dbstore.BotWorkdirRecord) Workdir {
	targetID := workspace.WorkspaceTargetNative
	if record.TargetKind == TargetKindRemote {
		targetID = record.RemoteBindingID
	}
	return Workdir{
		ID:                record.ID,
		BotID:             record.BotID,
		Name:              record.Name,
		TargetKind:        record.TargetKind,
		WorkspaceTargetID: targetID,
		Path:              record.Path,
		CreatedByUserID:   record.CreatedByUserID,
		Archived:          !record.ArchivedAt.IsZero(),
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
	}
}
