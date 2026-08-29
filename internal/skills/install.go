package skills

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const archivePublicationCleanupTimeout = 30 * time.Second

func writeArchiveFiles(
	ctx context.Context,
	client *bridge.Client,
	workspaceOS, targetDir string,
	archive Archive,
) error {
	executablePaths := make([]string, 0)
	for _, file := range archive.files {
		filePath := path.Join(targetDir, file.path)
		if dir := path.Dir(filePath); dir != targetDir {
			if err := client.Mkdir(ctx, dir); err != nil {
				return fmt.Errorf("create Skill directory %q: %w", dir, err)
			}
		}
		if err := client.WriteFile(ctx, filePath, file.content); err != nil {
			return fmt.Errorf("write Skill file %q: %w", file.path, err)
		}
		if file.executable {
			executablePaths = append(executablePaths, filePath)
		}
	}
	if err := applyExecutableModes(ctx, client, workspaceOS, executablePaths); err != nil {
		return err
	}
	return nil
}

func publicationCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), archivePublicationCleanupTimeout)
}

func applyExecutableModes(ctx context.Context, client *bridge.Client, workspaceOS string, paths []string) error {
	if len(paths) == 0 || strings.EqualFold(workspaceOS, "windows") || strings.EqualFold(workspaceOS, "win32") {
		return nil
	}
	for start := 0; start < len(paths); start += 100 {
		end := min(start+100, len(paths))
		quoted := make([]string, 0, end-start)
		for _, filePath := range paths[start:end] {
			quoted = append(quoted, shellQuote(filePath))
		}
		result, err := client.ExecWithEnv(ctx, "chmod 755 -- "+strings.Join(quoted, " "), "/", 30, nil)
		if err != nil {
			return fmt.Errorf("preserve executable Skill files: %w", err)
		}
		if result != nil && result.ExitCode != 0 {
			return fmt.Errorf("preserve executable Skill files: chmod exited with code %d", result.ExitCode)
		}
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
