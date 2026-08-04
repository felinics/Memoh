package project

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/memohai/memoh/internal/db"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/workspace"
	"github.com/memohai/memoh/internal/workspace/bridge"
	"github.com/memohai/memoh/internal/workspace/vpath"
)

// targetResolver is the slice of *workspace.Manager the project service
// needs: resolving a target address to a live bridge client and workspace
// info, for directory validation at create time.
type targetResolver interface {
	ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (workspace.ResolvedWorkspaceTarget, error)
}

// Service owns bot project directories.
type Service struct {
	store   dbstore.BotProjectStore
	targets targetResolver
}

func NewService(store dbstore.BotProjectStore, manager *workspace.Manager) *Service {
	return &Service{store: store, targets: manager}
}

// Create validates the target and directory, then persists the project.
// The directory must already exist on the target: a mistyped path should
// fail here, at the creation site, not on the first agent turn.
func (s *Service) Create(ctx context.Context, botID, userID string, req CreateRequest) (Project, error) {
	if s == nil || s.store == nil {
		return Project{}, errors.New("project service not configured")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Project{}, ErrNameRequired
	}
	targetID := strings.TrimSpace(req.WorkspaceTargetID)
	if targetID == "" {
		targetID = workspace.WorkspaceTargetNative
	}
	resolved, err := s.targets.ResolveWorkspaceTarget(ctx, botID, targetID)
	if err != nil {
		return Project{}, err
	}
	normalized, err := normalizeProjectPath(req.Path, resolved)
	if err != nil {
		return Project{}, err
	}
	if err := s.requireDirectory(ctx, resolved, normalized); err != nil {
		return Project{}, err
	}
	input := dbstore.CreateBotProjectInput{
		BotID:           botID,
		Name:            name,
		TargetKind:      resolved.Kind,
		Path:            normalized,
		CreatedByUserID: strings.TrimSpace(userID),
	}
	if resolved.Kind == TargetKindRemote {
		input.RemoteBindingID = resolved.TargetID
	}
	record, err := s.store.CreateProject(ctx, input)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Project{}, ErrDuplicatePath
		}
		return Project{}, err
	}
	return toProject(record), nil
}

func (s *Service) List(ctx context.Context, botID string, includeArchived bool) ([]Project, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("project service not configured")
	}
	records, err := s.store.ListProjects(ctx, botID, includeArchived)
	if err != nil {
		return nil, err
	}
	projects := make([]Project, 0, len(records))
	for _, record := range records {
		projects = append(projects, toProject(record))
	}
	return projects, nil
}

// Get returns the project regardless of archive state: sessions bound to an
// archived project keep resolving their working directory.
func (s *Service) Get(ctx context.Context, botID, projectID string) (Project, error) {
	if s == nil || s.store == nil {
		return Project{}, errors.New("project service not configured")
	}
	record, err := s.store.GetProject(ctx, botID, projectID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, err
	}
	return toProject(record), nil
}

// RequireActive is Get plus an archive check — the gate for binding new
// sessions to a project.
func (s *Service) RequireActive(ctx context.Context, botID, projectID string) (Project, error) {
	project, err := s.Get(ctx, botID, projectID)
	if err != nil {
		return Project{}, err
	}
	if project.Archived {
		return Project{}, ErrProjectArchived
	}
	return project, nil
}

func (s *Service) Rename(ctx context.Context, botID, projectID, name string) (Project, error) {
	if s == nil || s.store == nil {
		return Project{}, errors.New("project service not configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, ErrNameRequired
	}
	record, err := s.store.RenameProject(ctx, botID, projectID, name)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, err
	}
	return toProject(record), nil
}

// Archive soft-deletes the project. Bound sessions keep their working
// directory (the row stays); only new bindings are refused from here on.
func (s *Service) Archive(ctx context.Context, botID, projectID string) error {
	if s == nil || s.store == nil {
		return errors.New("project service not configured")
	}
	if err := s.store.ArchiveProject(ctx, botID, projectID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrProjectNotFound
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
		return fmt.Errorf("stat project path: %w", err)
	}
	if !entry.GetIsDir() {
		return fmt.Errorf("%w: %s", ErrPathNotDirectory, normalized)
	}
	return nil
}

// normalizeProjectPath canonicalizes the user-supplied path for the target
// it will live on. Native paths are clamped under the container data mount;
// remote paths are host-absolute (Windows drive/UNC paths included) and the
// remote runtime stays the authority on what exists there.
func normalizeProjectPath(raw string, target workspace.ResolvedWorkspaceTarget) (string, error) {
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

func toProject(record dbstore.BotProjectRecord) Project {
	targetID := workspace.WorkspaceTargetNative
	if record.TargetKind == TargetKindRemote {
		targetID = record.RemoteBindingID
	}
	return Project{
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
