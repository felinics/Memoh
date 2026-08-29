package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	defaultWorkspaceBootstrapRoot = "/data"
	workspaceAgentsFileName       = "AGENTS.md"
	legacyIdentityFileName        = "IDENTITY.md"
	managedWorkspaceSkillsDir     = ".memoh/skills"
	templateKeepFileName          = ".gitkeep"
)

// ErrWorkspaceTemplateBootstrapFailed identifies a workspace that started but
// could not receive the built-in workspace files. The wrapped cause is private
// diagnostic data and must be translated at the transport boundary.
var ErrWorkspaceTemplateBootstrapFailed = errors.New("workspace template bootstrap failed")

// WorkspaceFileInfo is the provider-neutral subset of file metadata needed by
// the workspace bootstrapper.
type WorkspaceFileInfo struct {
	IsDir bool
}

// WorkspaceFileSystem is the minimal filesystem contract needed to seed a
// workspace. Container workspaces adapt bridge.Client; future providers such as
// E2B can implement this contract directly without running the Memoh bridge.
// Implementations must return an error matching fs.ErrNotExist for a missing
// path and make Mkdir recursive.
type WorkspaceFileSystem interface {
	Stat(ctx context.Context, filePath string) (WorkspaceFileInfo, error)
	Mkdir(ctx context.Context, dirPath string) error
	WriteFile(ctx context.Context, filePath string, content []byte) error
	Rename(ctx context.Context, oldPath, newPath string) error
}

// TemplateBootstrapper copies the canonical embedded template tree into one
// provider-owned workspace filesystem.
type TemplateBootstrapper struct {
	source fs.FS
}

func NewTemplateBootstrapper(source fs.FS) *TemplateBootstrapper {
	return &TemplateBootstrapper{source: source}
}

// Bootstrap applies the built-in workspace template under root. User-owned
// files are create-only, while files belonging to built-in skills are managed
// and refreshed on every reconciliation. Extra user skills are never removed.
func (b *TemplateBootstrapper) Bootstrap(ctx context.Context, target WorkspaceFileSystem, root string) error {
	if b == nil || b.source == nil {
		return errors.New("workspace template source is not configured")
	}
	if target == nil {
		return errors.New("workspace template target is not configured")
	}
	root = path.Clean(strings.TrimSpace(root))
	if root == "." || root == "/" || !path.IsAbs(root) {
		return fmt.Errorf("workspace template root must be an absolute non-root path: %q", root)
	}
	if err := target.Mkdir(ctx, root); err != nil {
		return fmt.Errorf("create workspace root: %w", err)
	}
	if err := migrateLegacyWorkspaceIdentity(ctx, target, root); err != nil {
		return err
	}

	return fs.WalkDir(b.source, ".", func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sourcePath == "." {
			return nil
		}
		if !fs.ValidPath(sourcePath) {
			return fmt.Errorf("invalid workspace template path %q", sourcePath)
		}
		if !entry.IsDir() && path.Base(sourcePath) == templateKeepFileName {
			return nil
		}

		destination := path.Join(root, sourcePath)
		if entry.IsDir() {
			if err := target.Mkdir(ctx, destination); err != nil {
				return fmt.Errorf("create template directory %s: %w", sourcePath, err)
			}
			return nil
		}
		if entry.Type()&fs.ModeType != 0 {
			return nil
		}

		managed := isManagedWorkspaceSkillPath(sourcePath)
		if !managed {
			if _, err := target.Stat(ctx, destination); err == nil {
				return nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("stat template destination %s: %w", sourcePath, err)
			}
		}

		content, err := fs.ReadFile(b.source, sourcePath)
		if err != nil {
			return fmt.Errorf("read template source %s: %w", sourcePath, err)
		}
		if err := target.Mkdir(ctx, path.Dir(destination)); err != nil {
			return fmt.Errorf("create template parent %s: %w", sourcePath, err)
		}
		if err := target.WriteFile(ctx, destination, content); err != nil {
			return fmt.Errorf("write template file %s: %w", sourcePath, err)
		}
		return nil
	})
}

func migrateLegacyWorkspaceIdentity(ctx context.Context, target WorkspaceFileSystem, root string) error {
	agentsPath := path.Join(root, workspaceAgentsFileName)
	if _, err := target.Stat(ctx, agentsPath); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", workspaceAgentsFileName, err)
	}

	identityPath := path.Join(root, legacyIdentityFileName)
	info, err := target.Stat(ctx, identityPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", legacyIdentityFileName, err)
	}
	if info.IsDir {
		return nil
	}
	if err := target.Rename(ctx, identityPath, agentsPath); err != nil {
		return fmt.Errorf("migrate %s to %s: %w", legacyIdentityFileName, workspaceAgentsFileName, err)
	}
	return nil
}

func isManagedWorkspaceSkillPath(relativePath string) bool {
	return relativePath == managedWorkspaceSkillsDir || strings.HasPrefix(relativePath, managedWorkspaceSkillsDir+"/")
}

type bridgeWorkspaceFileSystem struct {
	client *bridge.Client
}

func (b bridgeWorkspaceFileSystem) Stat(ctx context.Context, filePath string) (WorkspaceFileInfo, error) {
	entry, err := b.client.Stat(ctx, filePath)
	if errors.Is(err, bridge.ErrNotFound) {
		return WorkspaceFileInfo{}, fmt.Errorf("%w: %s", fs.ErrNotExist, filePath)
	}
	if err != nil {
		return WorkspaceFileInfo{}, err
	}
	return WorkspaceFileInfo{IsDir: entry.GetIsDir()}, nil
}

func (b bridgeWorkspaceFileSystem) Mkdir(ctx context.Context, dirPath string) error {
	return b.client.Mkdir(ctx, dirPath)
}

func (b bridgeWorkspaceFileSystem) WriteFile(ctx context.Context, filePath string, content []byte) error {
	return b.client.WriteFile(ctx, filePath, content)
}

func (b bridgeWorkspaceFileSystem) Rename(ctx context.Context, oldPath, newPath string) error {
	return b.client.Rename(ctx, oldPath, newPath)
}
