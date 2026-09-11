package skills

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const appRevisionMarker = ".memoh-app-revision"

// AppArchive is one validated Skill member prepared for App publication.
type AppArchive struct {
	SkillID string
	Archive Archive
}

type appPublicationClient interface {
	DeleteFile(ctx context.Context, path string, recursive bool) error
	Rename(ctx context.Context, oldPath, newPath string) error
}

type appPaths struct {
	target    string
	staging   string
	candidate string
	backup    string
}

// AppPublication retains the previous App directory until the caller
// commits, so an App installation can roll back all expanded Skill changes.
type AppPublication struct {
	client       appPublicationClient
	targetDir    string
	backupDir    string
	stagingDir   string
	targetExists bool
	closed       bool
}

type AppRemoval struct {
	client     appPublicationClient
	targetDir  string
	backupDir  string
	stagingDir string
	closed     bool
}

// ReconcileApp restores the filesystem state selected by the installation
// record after an interrupted publish or removal. A false result means no
// recoverable copy matched the recorded revision; callers may replace or remove
// the managed App to repair it.
func ReconcileApp(
	ctx context.Context,
	client *bridge.Client,
	registryID, appID, expectedRevision string,
) (bool, error) {
	paths, err := appOperationPaths(registryID, appID)
	if err != nil || client == nil || (expectedRevision != "" && !isAppRevision(expectedRevision)) {
		return false, errors.New("registry App identity is invalid")
	}
	if err := deleteAppPath(ctx, client, paths.candidate, true); err != nil {
		return false, fmt.Errorf("remove incomplete App candidate: %w", err)
	}

	targetExists, err := appPathExists(ctx, client, paths.target)
	if err != nil {
		return false, fmt.Errorf("inspect installed App: %w", err)
	}
	if expectedRevision == "" {
		if targetExists {
			if err := deleteAppPath(ctx, client, paths.target, true); err != nil {
				return false, fmt.Errorf("remove unrecorded App: %w", err)
			}
		}
		if err := deleteAppPath(ctx, client, paths.staging, true); err != nil {
			return false, fmt.Errorf("clean unrecorded App staging: %w", err)
		}
		return !targetExists, nil
	}

	if targetExists {
		revision, markerExists, readErr := readAppRevision(ctx, client, paths.target)
		if readErr != nil {
			return false, fmt.Errorf("read installed App revision: %w", readErr)
		}
		if !markerExists {
			if err := writeAppRevision(ctx, client, paths.target, expectedRevision); err != nil {
				return false, fmt.Errorf("record existing App revision: %w", err)
			}
			if err := deleteAppPath(ctx, client, paths.staging, true); err != nil {
				return false, fmt.Errorf("clean existing App staging: %w", err)
			}
			return true, nil
		}
		if revision == expectedRevision {
			if err := deleteAppPath(ctx, client, paths.staging, true); err != nil {
				return false, fmt.Errorf("clean completed App staging: %w", err)
			}
			return true, nil
		}
	}

	backupExists, err := appPathExists(ctx, client, paths.backup)
	if err != nil {
		return false, fmt.Errorf("inspect App recovery copy: %w", err)
	}
	if backupExists {
		revision, markerExists, readErr := readAppRevision(ctx, client, paths.backup)
		if readErr != nil {
			return false, fmt.Errorf("read App recovery revision: %w", readErr)
		}
		if markerExists && revision == expectedRevision {
			if targetExists {
				if err := deleteAppPath(ctx, client, paths.target, true); err != nil {
					return false, fmt.Errorf("remove incomplete App replacement: %w", err)
				}
			}
			if err := client.Rename(ctx, paths.backup, paths.target); err != nil {
				return false, fmt.Errorf("restore recorded App revision: %w", err)
			}
			if err := deleteAppPath(ctx, client, paths.staging, true); err != nil {
				return false, fmt.Errorf("clean recovered App staging: %w", err)
			}
			return true, nil
		}
	}

	if err := deleteAppPath(ctx, client, paths.staging, true); err != nil {
		return false, fmt.Errorf("clean inconsistent App staging: %w", err)
	}
	return false, nil
}

// PublishApp stages every member before replacing the App root.
func PublishApp(
	ctx context.Context,
	client *bridge.Client,
	workspaceOS, registryID, appID, revision string,
	members []AppArchive,
) (*AppPublication, error) {
	paths, err := appOperationPaths(registryID, appID)
	if err != nil || client == nil || len(members) == 0 || !isAppRevision(revision) {
		return nil, errors.New("registry App identity is invalid")
	}
	registryDir, err := skillNamespaceDirForID(registryID)
	if err != nil {
		return nil, errors.New("registry App identity is invalid")
	}
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		if _, err := SkillDirForIDs(registryID, appID, member.SkillID); err != nil {
			return nil, errors.New("registry App Skill identity is invalid")
		}
		if _, exists := seen[member.SkillID]; exists {
			return nil, errors.New("registry App contains duplicate Skills")
		}
		seen[member.SkillID] = struct{}{}
	}
	backupExists, err := appPathExists(ctx, client, paths.backup)
	if err != nil {
		return nil, fmt.Errorf("inspect App staging: %w", err)
	}
	if backupExists {
		return nil, errors.New("app staging must be reconciled before publication")
	}
	if err := deleteAppPath(ctx, client, paths.candidate, true); err != nil {
		return nil, fmt.Errorf("clean temporary App directory: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		cleanupCtx, cancel := publicationCleanupContext(ctx)
		defer cancel()
		_ = deleteAppPath(cleanupCtx, client, paths.candidate, true)
	}()
	if err := client.Mkdir(ctx, paths.candidate); err != nil {
		return nil, fmt.Errorf("create temporary App directory: %w", err)
	}

	for _, member := range members {
		skillDir := path.Join(paths.candidate, member.SkillID)
		if err := client.Mkdir(ctx, skillDir); err != nil {
			return nil, fmt.Errorf("create temporary App Skill directory: %w", err)
		}
		if err := writeArchiveFiles(ctx, client, workspaceOS, skillDir, member.Archive); err != nil {
			return nil, fmt.Errorf("stage App Skill %q: %w", member.SkillID, err)
		}
	}
	if err := writeAppRevision(ctx, client, paths.candidate, revision); err != nil {
		return nil, fmt.Errorf("write App revision marker: %w", err)
	}

	if err := client.Mkdir(ctx, registryDir); err != nil {
		return nil, fmt.Errorf("create Registry Skill directory: %w", err)
	}
	targetExists := false
	if _, err := client.Stat(ctx, paths.target); err == nil {
		targetExists = true
	} else if !errors.Is(err, bridge.ErrNotFound) {
		return nil, fmt.Errorf("inspect existing App: %w", err)
	}
	if targetExists {
		if err := client.Rename(ctx, paths.target, paths.backup); err != nil {
			return nil, fmt.Errorf("prepare existing App for replacement: %w", err)
		}
	}
	if err := client.Rename(ctx, paths.candidate, paths.target); err != nil {
		if targetExists {
			rollbackCtx, cancel := publicationCleanupContext(ctx)
			defer cancel()
			if rollbackErr := client.Rename(rollbackCtx, paths.backup, paths.target); rollbackErr != nil {
				return nil, fmt.Errorf(
					"publish App: %w; restore previous App from %q: %w",
					err, paths.backup, rollbackErr,
				)
			}
		}
		return nil, fmt.Errorf("publish App: %w", err)
	}
	published = true
	return &AppPublication{
		client: client, targetDir: paths.target, backupDir: paths.backup,
		stagingDir: paths.staging, targetExists: targetExists,
	}, nil
}

func (p *AppPublication) Commit(ctx context.Context) error {
	if p == nil || p.closed {
		return nil
	}
	cleanupCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deleteAppPath(cleanupCtx, p.client, p.stagingDir, true); err != nil {
		return err
	}
	p.closed = true
	return nil
}

func (p *AppPublication) Rollback(ctx context.Context) error {
	if p == nil || p.closed {
		return nil
	}
	rollbackCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deleteAppPath(rollbackCtx, p.client, p.targetDir, true); err != nil {
		return fmt.Errorf("remove replacement App: %w", err)
	}
	if !p.targetExists {
		if err := deleteAppPath(rollbackCtx, p.client, p.stagingDir, true); err != nil {
			return fmt.Errorf("clean rolled back App staging: %w", err)
		}
		p.closed = true
		return nil
	}
	if err := p.client.Rename(rollbackCtx, p.backupDir, p.targetDir); err != nil {
		return fmt.Errorf("restore previous App from %q: %w", p.backupDir, err)
	}
	if err := deleteAppPath(rollbackCtx, p.client, p.stagingDir, true); err != nil {
		return fmt.Errorf("clean rolled back App staging: %w", err)
	}
	p.closed = true
	return nil
}

// PrepareAppRemoval moves a whole App out of the discovery tree. The
// caller commits after its database transaction, or rolls back on failure.
func PrepareAppRemoval(ctx context.Context, client *bridge.Client, registryID, appID string) (*AppRemoval, error) {
	paths, err := appOperationPaths(registryID, appID)
	if err != nil || client == nil {
		return nil, errors.New("registry App identity is invalid")
	}
	candidateExists, err := appPathExists(ctx, client, paths.candidate)
	if err != nil {
		return nil, fmt.Errorf("inspect App removal staging: %w", err)
	}
	if candidateExists {
		return nil, errors.New("app staging must be reconciled before removal")
	}
	backupExists, err := appPathExists(ctx, client, paths.backup)
	if err != nil {
		return nil, fmt.Errorf("inspect staged App removal: %w", err)
	}
	targetExists, err := appPathExists(ctx, client, paths.target)
	if err != nil {
		return nil, fmt.Errorf("inspect App before removal: %w", err)
	}
	if backupExists {
		if targetExists {
			return nil, errors.New("app removal staging must be reconciled")
		}
		return &AppRemoval{client: client, targetDir: paths.target, backupDir: paths.backup, stagingDir: paths.staging}, nil
	}
	if !targetExists {
		return nil, nil
	}
	if err := client.Mkdir(ctx, paths.staging); err != nil {
		return nil, fmt.Errorf("create App removal staging root: %w", err)
	}
	if err := client.Rename(ctx, paths.target, paths.backup); err != nil {
		return nil, fmt.Errorf("stage App removal: %w", err)
	}
	return &AppRemoval{client: client, targetDir: paths.target, backupDir: paths.backup, stagingDir: paths.staging}, nil
}

func (r *AppRemoval) Commit(ctx context.Context) error {
	if r == nil || r.closed {
		return nil
	}
	cleanupCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deleteAppPath(cleanupCtx, r.client, r.stagingDir, true); err != nil {
		return err
	}
	r.closed = true
	return nil
}

func (r *AppRemoval) Rollback(ctx context.Context) error {
	if r == nil || r.closed {
		return nil
	}
	rollbackCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deleteAppPath(rollbackCtx, r.client, r.targetDir, true); err != nil {
		return fmt.Errorf("remove conflicting replacement App: %w", err)
	}
	if err := r.client.Rename(rollbackCtx, r.backupDir, r.targetDir); err != nil {
		return fmt.Errorf("restore removed App: %w", err)
	}
	if err := deleteAppPath(rollbackCtx, r.client, r.stagingDir, true); err != nil {
		return fmt.Errorf("clean rolled back App removal: %w", err)
	}
	r.closed = true
	return nil
}

func appOperationPaths(registryID, appID string) (appPaths, error) {
	targetDir, err := AppDirForIDs(registryID, appID)
	if err != nil || registryID == UserSkillNamespace {
		return appPaths{}, bridge.ErrBadRequest
	}
	stagingDir := path.Join(ManagedDir(), ".staging", registryID, appID)
	return appPaths{
		target: targetDir, staging: stagingDir,
		candidate: path.Join(stagingDir, "candidate"),
		backup:    path.Join(stagingDir, "backup"),
	}, nil
}

func appPathExists(ctx context.Context, client *bridge.Client, targetPath string) (bool, error) {
	_, err := client.Stat(ctx, targetPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, bridge.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func deleteAppPath(ctx context.Context, client appPublicationClient, targetPath string, recursive bool) error {
	err := client.DeleteFile(ctx, targetPath, recursive)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil
	}
	return err
}

func readAppRevision(ctx context.Context, client *bridge.Client, appDir string) (string, bool, error) {
	response, err := client.ReadFile(ctx, path.Join(appDir, appRevisionMarker), 0, 2)
	if errors.Is(err, bridge.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	revision := strings.TrimSpace(response.GetContent())
	if !isAppRevision(revision) {
		return "", true, nil
	}
	return revision, true, nil
}

func writeAppRevision(ctx context.Context, client *bridge.Client, appDir, revision string) error {
	return client.WriteFile(ctx, path.Join(appDir, appRevisionMarker), []byte(revision+"\n"))
}

func isAppRevision(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
