package skills

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const packageRevisionMarker = ".memoh-package-revision"

// PackageArchive is one validated Skill member prepared for Package publication.
type PackageArchive struct {
	SkillID string
	Archive Archive
}

type packagePublicationClient interface {
	DeleteFile(ctx context.Context, path string, recursive bool) error
	Rename(ctx context.Context, oldPath, newPath string) error
}

type packagePaths struct {
	target    string
	staging   string
	candidate string
	backup    string
}

// PackagePublication retains the previous Package directory until the caller
// commits, so a Package installation can roll back all expanded Skill changes.
type PackagePublication struct {
	client       packagePublicationClient
	targetDir    string
	backupDir    string
	stagingDir   string
	targetExists bool
	closed       bool
}

type PackageRemoval struct {
	client     packagePublicationClient
	targetDir  string
	backupDir  string
	stagingDir string
	closed     bool
}

// ReconcilePackage restores the filesystem state selected by the installation
// record after an interrupted publish or removal. A false result means no
// recoverable copy matched the recorded revision; callers may replace or remove
// the managed Package to repair it.
func ReconcilePackage(
	ctx context.Context,
	client *bridge.Client,
	registryID, packageID, expectedRevision string,
) (bool, error) {
	paths, err := packageOperationPaths(registryID, packageID)
	if err != nil || client == nil || (expectedRevision != "" && !isPackageRevision(expectedRevision)) {
		return false, errors.New("registry Package identity is invalid")
	}
	if err := deletePackagePath(ctx, client, paths.candidate, true); err != nil {
		return false, fmt.Errorf("remove incomplete Package candidate: %w", err)
	}

	targetExists, err := packagePathExists(ctx, client, paths.target)
	if err != nil {
		return false, fmt.Errorf("inspect installed Package: %w", err)
	}
	if expectedRevision == "" {
		if targetExists {
			if err := deletePackagePath(ctx, client, paths.target, true); err != nil {
				return false, fmt.Errorf("remove unrecorded Package: %w", err)
			}
		}
		if err := deletePackagePath(ctx, client, paths.staging, true); err != nil {
			return false, fmt.Errorf("clean unrecorded Package staging: %w", err)
		}
		return !targetExists, nil
	}

	if targetExists {
		revision, markerExists, readErr := readPackageRevision(ctx, client, paths.target)
		if readErr != nil {
			return false, fmt.Errorf("read installed Package revision: %w", readErr)
		}
		if !markerExists {
			if err := writePackageRevision(ctx, client, paths.target, expectedRevision); err != nil {
				return false, fmt.Errorf("record existing Package revision: %w", err)
			}
			if err := deletePackagePath(ctx, client, paths.staging, true); err != nil {
				return false, fmt.Errorf("clean existing Package staging: %w", err)
			}
			return true, nil
		}
		if revision == expectedRevision {
			if err := deletePackagePath(ctx, client, paths.staging, true); err != nil {
				return false, fmt.Errorf("clean completed Package staging: %w", err)
			}
			return true, nil
		}
	}

	backupExists, err := packagePathExists(ctx, client, paths.backup)
	if err != nil {
		return false, fmt.Errorf("inspect Package recovery copy: %w", err)
	}
	if backupExists {
		revision, markerExists, readErr := readPackageRevision(ctx, client, paths.backup)
		if readErr != nil {
			return false, fmt.Errorf("read Package recovery revision: %w", readErr)
		}
		if markerExists && revision == expectedRevision {
			if targetExists {
				if err := deletePackagePath(ctx, client, paths.target, true); err != nil {
					return false, fmt.Errorf("remove incomplete Package replacement: %w", err)
				}
			}
			if err := client.Rename(ctx, paths.backup, paths.target); err != nil {
				return false, fmt.Errorf("restore recorded Package revision: %w", err)
			}
			if err := deletePackagePath(ctx, client, paths.staging, true); err != nil {
				return false, fmt.Errorf("clean recovered Package staging: %w", err)
			}
			return true, nil
		}
	}

	if err := deletePackagePath(ctx, client, paths.staging, true); err != nil {
		return false, fmt.Errorf("clean inconsistent Package staging: %w", err)
	}
	return false, nil
}

// PublishPackage stages every member before replacing the Package root.
func PublishPackage(
	ctx context.Context,
	client *bridge.Client,
	workspaceOS, registryID, packageID, revision string,
	members []PackageArchive,
) (*PackagePublication, error) {
	paths, err := packageOperationPaths(registryID, packageID)
	if err != nil || client == nil || len(members) == 0 || !isPackageRevision(revision) {
		return nil, errors.New("registry Package identity is invalid")
	}
	registryDir, err := skillNamespaceDirForID(registryID)
	if err != nil {
		return nil, errors.New("registry Package identity is invalid")
	}
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		if _, err := SkillDirForIDs(registryID, packageID, member.SkillID); err != nil {
			return nil, errors.New("registry Package Skill identity is invalid")
		}
		if _, exists := seen[member.SkillID]; exists {
			return nil, errors.New("registry Package contains duplicate Skills")
		}
		seen[member.SkillID] = struct{}{}
	}
	backupExists, err := packagePathExists(ctx, client, paths.backup)
	if err != nil {
		return nil, fmt.Errorf("inspect Package staging: %w", err)
	}
	if backupExists {
		return nil, errors.New("package staging must be reconciled before publication")
	}
	if err := deletePackagePath(ctx, client, paths.candidate, true); err != nil {
		return nil, fmt.Errorf("clean temporary Package directory: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		cleanupCtx, cancel := publicationCleanupContext(ctx)
		defer cancel()
		_ = deletePackagePath(cleanupCtx, client, paths.candidate, true)
	}()
	if err := client.Mkdir(ctx, paths.candidate); err != nil {
		return nil, fmt.Errorf("create temporary Package directory: %w", err)
	}

	for _, member := range members {
		skillDir := path.Join(paths.candidate, member.SkillID)
		if err := client.Mkdir(ctx, skillDir); err != nil {
			return nil, fmt.Errorf("create temporary Package Skill directory: %w", err)
		}
		if err := writeArchiveFiles(ctx, client, workspaceOS, skillDir, member.Archive); err != nil {
			return nil, fmt.Errorf("stage Package Skill %q: %w", member.SkillID, err)
		}
	}
	if err := writePackageRevision(ctx, client, paths.candidate, revision); err != nil {
		return nil, fmt.Errorf("write Package revision marker: %w", err)
	}

	if err := client.Mkdir(ctx, registryDir); err != nil {
		return nil, fmt.Errorf("create Registry Skill directory: %w", err)
	}
	targetExists := false
	if _, err := client.Stat(ctx, paths.target); err == nil {
		targetExists = true
	} else if !errors.Is(err, bridge.ErrNotFound) {
		return nil, fmt.Errorf("inspect existing Package: %w", err)
	}
	if targetExists {
		if err := client.Rename(ctx, paths.target, paths.backup); err != nil {
			return nil, fmt.Errorf("prepare existing Package for replacement: %w", err)
		}
	}
	if err := client.Rename(ctx, paths.candidate, paths.target); err != nil {
		if targetExists {
			rollbackCtx, cancel := publicationCleanupContext(ctx)
			defer cancel()
			if rollbackErr := client.Rename(rollbackCtx, paths.backup, paths.target); rollbackErr != nil {
				return nil, fmt.Errorf(
					"publish Package: %w; restore previous Package from %q: %w",
					err, paths.backup, rollbackErr,
				)
			}
		}
		return nil, fmt.Errorf("publish Package: %w", err)
	}
	published = true
	return &PackagePublication{
		client: client, targetDir: paths.target, backupDir: paths.backup,
		stagingDir: paths.staging, targetExists: targetExists,
	}, nil
}

func (p *PackagePublication) Commit(ctx context.Context) error {
	if p == nil || p.closed {
		return nil
	}
	cleanupCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deletePackagePath(cleanupCtx, p.client, p.stagingDir, true); err != nil {
		return err
	}
	p.closed = true
	return nil
}

func (p *PackagePublication) Rollback(ctx context.Context) error {
	if p == nil || p.closed {
		return nil
	}
	rollbackCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deletePackagePath(rollbackCtx, p.client, p.targetDir, true); err != nil {
		return fmt.Errorf("remove replacement Package: %w", err)
	}
	if !p.targetExists {
		if err := deletePackagePath(rollbackCtx, p.client, p.stagingDir, true); err != nil {
			return fmt.Errorf("clean rolled back Package staging: %w", err)
		}
		p.closed = true
		return nil
	}
	if err := p.client.Rename(rollbackCtx, p.backupDir, p.targetDir); err != nil {
		return fmt.Errorf("restore previous Package from %q: %w", p.backupDir, err)
	}
	if err := deletePackagePath(rollbackCtx, p.client, p.stagingDir, true); err != nil {
		return fmt.Errorf("clean rolled back Package staging: %w", err)
	}
	p.closed = true
	return nil
}

// PreparePackageRemoval moves a whole Package out of the discovery tree. The
// caller commits after its database transaction, or rolls back on failure.
func PreparePackageRemoval(ctx context.Context, client *bridge.Client, registryID, packageID string) (*PackageRemoval, error) {
	paths, err := packageOperationPaths(registryID, packageID)
	if err != nil || client == nil {
		return nil, errors.New("registry Package identity is invalid")
	}
	candidateExists, err := packagePathExists(ctx, client, paths.candidate)
	if err != nil {
		return nil, fmt.Errorf("inspect Package removal staging: %w", err)
	}
	if candidateExists {
		return nil, errors.New("package staging must be reconciled before removal")
	}
	backupExists, err := packagePathExists(ctx, client, paths.backup)
	if err != nil {
		return nil, fmt.Errorf("inspect staged Package removal: %w", err)
	}
	targetExists, err := packagePathExists(ctx, client, paths.target)
	if err != nil {
		return nil, fmt.Errorf("inspect Package before removal: %w", err)
	}
	if backupExists {
		if targetExists {
			return nil, errors.New("package removal staging must be reconciled")
		}
		return &PackageRemoval{client: client, targetDir: paths.target, backupDir: paths.backup, stagingDir: paths.staging}, nil
	}
	if !targetExists {
		return nil, nil
	}
	if err := client.Mkdir(ctx, paths.staging); err != nil {
		return nil, fmt.Errorf("create Package removal staging root: %w", err)
	}
	if err := client.Rename(ctx, paths.target, paths.backup); err != nil {
		return nil, fmt.Errorf("stage Package removal: %w", err)
	}
	return &PackageRemoval{client: client, targetDir: paths.target, backupDir: paths.backup, stagingDir: paths.staging}, nil
}

func (r *PackageRemoval) Commit(ctx context.Context) error {
	if r == nil || r.closed {
		return nil
	}
	cleanupCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deletePackagePath(cleanupCtx, r.client, r.stagingDir, true); err != nil {
		return err
	}
	r.closed = true
	return nil
}

func (r *PackageRemoval) Rollback(ctx context.Context) error {
	if r == nil || r.closed {
		return nil
	}
	rollbackCtx, cancel := publicationCleanupContext(ctx)
	defer cancel()
	if err := deletePackagePath(rollbackCtx, r.client, r.targetDir, true); err != nil {
		return fmt.Errorf("remove conflicting replacement Package: %w", err)
	}
	if err := r.client.Rename(rollbackCtx, r.backupDir, r.targetDir); err != nil {
		return fmt.Errorf("restore removed Package: %w", err)
	}
	if err := deletePackagePath(rollbackCtx, r.client, r.stagingDir, true); err != nil {
		return fmt.Errorf("clean rolled back Package removal: %w", err)
	}
	r.closed = true
	return nil
}

func packageOperationPaths(registryID, packageID string) (packagePaths, error) {
	targetDir, err := SkillPackageDirForIDs(registryID, packageID)
	if err != nil || registryID == UserSkillNamespace {
		return packagePaths{}, bridge.ErrBadRequest
	}
	stagingDir := path.Join(ManagedDir(), ".staging", registryID, packageID)
	return packagePaths{
		target: targetDir, staging: stagingDir,
		candidate: path.Join(stagingDir, "candidate"),
		backup:    path.Join(stagingDir, "backup"),
	}, nil
}

func packagePathExists(ctx context.Context, client *bridge.Client, targetPath string) (bool, error) {
	_, err := client.Stat(ctx, targetPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, bridge.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func deletePackagePath(ctx context.Context, client packagePublicationClient, targetPath string, recursive bool) error {
	err := client.DeleteFile(ctx, targetPath, recursive)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil
	}
	return err
}

func readPackageRevision(ctx context.Context, client *bridge.Client, packageDir string) (string, bool, error) {
	response, err := client.ReadFile(ctx, path.Join(packageDir, packageRevisionMarker), 0, 2)
	if errors.Is(err, bridge.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	revision := strings.TrimSpace(response.GetContent())
	if !isPackageRevision(revision) {
		return "", true, nil
	}
	return revision, true, nil
}

func writePackageRevision(ctx context.Context, client *bridge.Client, packageDir, revision string) error {
	return client.WriteFile(ctx, path.Join(packageDir, packageRevisionMarker), []byte(revision+"\n"))
}

func isPackageRevision(value string) bool {
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
