package workdir

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// GitBranch reports the actual working directory, never a per-thread branch.
func (s *Service) GitBranch(ctx context.Context, botID, workdirID string) (GitBranchResponse, error) {
	record, target, err := s.gitTarget(ctx, botID, workdirID)
	if err != nil {
		return GitBranchResponse{}, err
	}
	state, root, err := readGitBranches(ctx, target.Client, record.Path)
	if err != nil || root == "" {
		return state, err
	}
	activity, ok := s.store.(dbstore.WorkdirActivityStore)
	if !ok {
		return state, fmt.Errorf("%w: activity store is unavailable", ErrGitUnavailable)
	}
	active, err := activity.ActiveWorkdirs(ctx, botID)
	if err != nil {
		return state, err
	}
	state.Busy, err = gitDirectoryBusy(ctx, target, root, active)
	return state, err
}

func (s *Service) SwitchGitBranch(ctx context.Context, botID, workdirID, branch string) (GitBranchResponse, error) {
	record, target, err := s.gitTarget(ctx, botID, workdirID)
	if err != nil {
		return GitBranchResponse{}, err
	}
	if record.Archived {
		return GitBranchResponse{}, ErrWorkdirArchived
	}
	// Remote execution is outside the current Codex composer scope.
	if target.Kind != TargetKindNative {
		return GitBranchResponse{}, ErrGitUnavailable
	}
	activity, ok := s.store.(dbstore.WorkdirActivityStore)
	if !ok {
		return GitBranchResponse{}, fmt.Errorf("%w: activity store is unavailable", ErrGitUnavailable)
	}
	err = activity.WithWorkdirMutation(ctx, botID, func(active []dbstore.BotWorkdirRecord) error {
		state, root, err := readGitBranches(ctx, target.Client, record.Path)
		if err != nil {
			return err
		}
		if root == "" {
			return ErrGitUnavailable
		}
		busy, err := gitDirectoryBusy(ctx, target, root, active)
		if err != nil {
			return err
		}
		if busy {
			return ErrGitBusy
		}
		found := false
		for _, name := range state.Branches {
			if name == branch {
				found = true
				break
			}
		}
		if !found {
			return ErrGitBranchUnavailable
		}
		if branch == state.Branch {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Wait for Git independently of a disconnected HTTP client, so admission
		// cannot resume while checkout is still changing workspace files.
		switchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		quoted := "'" + strings.ReplaceAll(branch, "'", "'\\''") + "'"
		result, err := target.Client.Exec(switchCtx, "git switch --no-guess -- "+quoted, root, 15)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrGitSwitchFailed, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("%w: %s", ErrGitSwitchFailed, result.Stderr)
		}
		return nil
	})
	if err != nil {
		return GitBranchResponse{}, err
	}
	return s.GitBranch(ctx, botID, workdirID)
}

func (s *Service) gitTarget(ctx context.Context, botID, workdirID string) (Workdir, workspace.ResolvedWorkspaceTarget, error) {
	record, err := s.Get(ctx, botID, workdirID)
	if err != nil {
		return record, workspace.ResolvedWorkspaceTarget{}, err
	}
	target, err := s.targets.ResolveWorkspaceTarget(ctx, botID, record.WorkspaceTargetID)
	return record, target, err
}

func gitRoot(ctx context.Context, client *bridge.Client, directory string) (string, error) {
	result, err := client.Exec(ctx, "git rev-parse --show-toplevel", directory, 5)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", nil
	}
	return strings.TrimSpace(result.Stdout), nil
}

func readGitBranches(ctx context.Context, client *bridge.Client, directory string) (GitBranchResponse, string, error) {
	state := GitBranchResponse{Branches: []string{}}
	root, err := gitRoot(ctx, client, directory)
	if err != nil || root == "" {
		return state, root, err
	}
	result, err := client.Exec(ctx, "git for-each-ref --format='%(refname)' refs/heads/", root, 5)
	if err != nil {
		return state, root, err
	}
	if result.ExitCode != 0 {
		return state, root, fmt.Errorf("%w: %s", ErrGitUnavailable, result.Stderr)
	}
	for _, ref := range strings.Fields(result.Stdout) {
		state.Branches = append(state.Branches, strings.TrimPrefix(ref, "refs/heads/"))
	}
	result, err = client.Exec(ctx, "git symbolic-ref --quiet --short HEAD", root, 5)
	if err != nil {
		return state, root, err
	}
	if result.ExitCode == 0 {
		state.Branch = strings.TrimSpace(result.Stdout)
	}
	return state, root, nil
}

func gitDirectoryBusy(ctx context.Context, target workspace.ResolvedWorkspaceTarget, root string, active []dbstore.BotWorkdirRecord) (bool, error) {
	seen := map[string]bool{}
	for _, directory := range active {
		if directory.TargetKind != target.Kind || (target.Kind == TargetKindRemote && directory.RemoteBindingID != target.TargetID) {
			continue
		}
		if seen[directory.Path] {
			continue
		}
		seen[directory.Path] = true
		// An unbound/default parent directory may operate in this repository.
		parent := strings.TrimRight(path.Clean(directory.Path), "/") + "/"
		if root == path.Clean(directory.Path) || strings.HasPrefix(root, parent) {
			return true, nil
		}
		otherRoot, err := gitRoot(ctx, target.Client, directory.Path)
		if err != nil {
			return false, err
		}
		if otherRoot == root {
			return true, nil
		}
	}
	return false, nil
}
