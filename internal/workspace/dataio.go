package workspace

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/mount"

	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	containerDataDir = "/data"
	snapshotsSubdir  = "snapshots"
	backupsSubdir    = "backups"
	archivePrefix    = "archive:"
)

// snapshotMountProvider is a private escape hatch for runtimes, currently
// containerd, that can expose host-side snapshot mounts. It is deliberately not
// part of the public container snapshot lifecycle abstraction.
type snapshotMountProvider interface {
	SnapshotMounts(ctx context.Context, snapshotter, key string) ([]ctr.MountInfo, error)
}

// ExportData streams a tar.gz archive of the container's /data directory.
// The container is stopped during export and restarted afterwards.
// Caller must consume the returned reader before the context is cancelled.
func (m *Manager) ExportData(ctx context.Context, botID string) (io.ReadCloser, error) {
	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("get workspace runtime: %w", err)
	}
	defer ref.Close()

	mounts, err := m.snapshotMounts(ctx, ref.info)
	if errors.Is(err, errMountNotSupported) {
		return m.exportDataViaGRPC(ctx, botID, false)
	}
	if err != nil {
		return nil, err
	}

	restartTask, err := ref.StopTaskForMutation(ctx)
	if err != nil {
		return nil, fmt.Errorf("stop workspace runtime: %w", err)
	}

	pr, pw := io.Pipe()

	go func() {
		var exportErr error
		defer func() {
			_ = pw.CloseWithError(exportErr)
			restartTask()
		}()

		exportErr = mount.WithReadonlyTempMount(ctx, mounts, func(root string) error {
			dataDir := mountedDataDir(root)
			if _, err := os.Stat(dataDir); err != nil {
				return nil // no /data, produce empty archive
			}
			return tarGzDir(pw, dataDir, false)
		})
	}()

	return pr, nil
}

// ImportData extracts a tar.gz archive into the container's /data directory.
// The container is stopped during import and restarted afterwards.
func (m *Manager) ImportData(ctx context.Context, botID string, r io.Reader) error {
	return m.importData(ctx, botID, r, false)
}

func (m *Manager) importData(ctx context.Context, botID string, r io.Reader, preserveCredentials bool) error {
	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return fmt.Errorf("get workspace runtime: %w", err)
	}
	defer ref.Close()

	mounts, err := m.snapshotMounts(ctx, ref.info)
	if errors.Is(err, errMountNotSupported) {
		return m.importDataViaGRPC(ctx, botID, r, preserveCredentials)
	}
	if err != nil {
		return err
	}

	restartTask, err := ref.StopTaskForMutation(ctx)
	if err != nil {
		return fmt.Errorf("stop workspace runtime: %w", err)
	}
	defer restartTask()

	return mount.WithTempMount(ctx, mounts, func(root string) error {
		dataDir := mountedDataDir(root)
		if err := os.MkdirAll(dataDir, 0o750); err != nil {
			return err
		}
		return untarGzDir(r, dataDir, preserveCredentials)
	})
}

// PreserveData exports /data to a backup tar.gz on the host. Used before
// deleting a container when the user chooses to preserve data.
// For snapshot-mount backends the caller must stop the task first so the
// mounted snapshot is consistent; the Apple fallback uses gRPC and does not
// require a stop.
func (m *Manager) PreserveData(ctx context.Context, botID string) error {
	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return fmt.Errorf("get workspace runtime: %w", err)
	}
	defer ref.Close()

	mounts, mountErr := m.snapshotMounts(ctx, ref.info)
	if errors.Is(mountErr, errMountNotSupported) {
		return m.preserveDataViaGRPC(ctx, botID, m.backupPath(botID), true)
	}
	if mountErr != nil {
		return mountErr
	}
	return m.preserveDataToBackup(ctx, botID, mounts)
}

// RestorePreservedData imports preserved data (backup tar.gz) into a running
// container's /data.
func (m *Manager) RestorePreservedData(ctx context.Context, botID string) error {
	return m.consumePreservedData(botID, func(r io.Reader) error {
		return m.importData(ctx, botID, r, true)
	})
}

// HasPreservedData checks whether a backup tar.gz exists for a bot.
func (m *Manager) HasPreservedData(botID string) bool {
	_, err := os.Stat(m.backupPath(botID))
	return err == nil
}

// recoverOrphanedSnapshot detects a snapshot whose container was deleted
// (e.g. dev image rebuild, containerd metadata loss) and exports /data to a
// backup archive. The caller should invoke restorePreservedIntoSnapshot after
// creating the replacement container. Returns true when data was preserved.
func (m *Manager) recoverOrphanedSnapshot(ctx context.Context, botID string) bool {
	snapshotter := m.cfg.Snapshotter
	if snapshotter == "" {
		return false
	}

	snapshotKey := m.resolveContainerID(ctx, botID)
	mounter, ok := m.service.(snapshotMountProvider)
	if !ok {
		return false
	}
	raw, err := mounter.SnapshotMounts(ctx, snapshotter, snapshotKey)
	if err != nil {
		return false
	}

	mounts := make([]mount.Mount, len(raw))
	for i, r := range raw {
		mounts[i] = mount.Mount{Type: r.Type, Source: r.Source, Options: r.Options}
	}

	backupPath := m.backupPath(botID)
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o750); err != nil {
		m.logger.Warn("recover orphaned snapshot: mkdir failed",
			slog.String("bot_id", botID), slog.Any("error", err))
		return false
	}

	f, err := createPrivateWorkspaceArchive(backupPath)
	if err != nil {
		m.logger.Warn("recover orphaned snapshot: create backup file failed",
			slog.String("bot_id", botID), slog.Any("error", err))
		return false
	}

	writeErr := mount.WithReadonlyTempMount(ctx, mounts, func(root string) error {
		dataDir := mountedDataDir(root)
		if _, statErr := os.Stat(dataDir); statErr != nil {
			return nil
		}
		return tarGzDir(f, dataDir, true)
	})

	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(backupPath)
		m.logger.Warn("recover orphaned snapshot: export failed",
			slog.String("bot_id", botID), slog.Any("error", writeErr))
		return false
	}
	if closeErr != nil {
		_ = os.Remove(backupPath)
		return false
	}
	return true
}

// restorePreservedIntoSnapshot restores a preserved backup directly into
// the container's snapshot before the task is started. This avoids the
// stop/start cycle that RestorePreservedData (via ImportData) requires.
func (m *Manager) restorePreservedIntoSnapshot(ctx context.Context, botID string, info ctr.ContainerInfo) error {
	mounts, err := m.snapshotMounts(ctx, info)
	if err != nil {
		return err
	}

	return m.consumePreservedData(botID, func(r io.Reader) error {
		return mount.WithTempMount(ctx, mounts, func(root string) error {
			dataDir := mountedDataDir(root)
			if err := os.MkdirAll(dataDir, 0o750); err != nil {
				return err
			}
			return untarGzDir(r, dataDir, true)
		})
	})
}

func (m *Manager) restorePreservedDataViaGRPC(ctx context.Context, botID string) error {
	return m.consumePreservedData(botID, func(r io.Reader) error {
		return m.importDataViaGRPC(ctx, botID, r, true)
	})
}

func (m *Manager) consumePreservedData(botID string, restore func(io.Reader) error) error {
	bp := m.backupPath(botID)
	f, err := os.Open(bp) //nolint:gosec // G304: operator-controlled path
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no preserved data found")
		}
		return err
	}
	if err := restore(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(bp)
}

// errMountNotSupported indicates the backend doesn't support snapshot mounts
// (e.g. Apple Virtualization). Callers fall back to gRPC-based data operations.
var errMountNotSupported = errors.New("snapshot mount not supported on this backend")

// Workspace archive filtering is intentionally enforced at this layer because
// container backups can bypass higher-level bot/profile metadata scrub. Keep
// these runtime-home prefixes in sync with the ACP and direct-runtime config
// locations; workspace stays agent-agnostic otherwise, so do not import a
// runtime package here.
var (
	workspaceRuntimeHomePrefixes   = []string{".codex/", ".claude/", ".memoh-hermes/", ".hermes/"}
	workspaceRuntimeSecretDirNames = map[string]struct{}{
		"auth":       {},
		"mcp-tokens": {},
		"projects":   {},
		"sessions":   {},
	}
)

func (m *Manager) snapshotMounts(ctx context.Context, info ctr.ContainerInfo) ([]mount.Mount, error) {
	mounter, ok := m.service.(snapshotMountProvider)
	if !ok {
		return nil, errMountNotSupported
	}
	raw, err := mounter.SnapshotMounts(ctx, info.StorageRef.Driver, info.StorageRef.Key)
	if err != nil {
		if errors.Is(err, ctr.ErrNotSupported) {
			return nil, errMountNotSupported
		}
		return nil, fmt.Errorf("get snapshot mounts: %w", err)
	}
	mounts := make([]mount.Mount, len(raw))
	for i, r := range raw {
		mounts[i] = mount.Mount{
			Type:    r.Type,
			Source:  r.Source,
			Options: r.Options,
		}
	}
	return mounts, nil
}

func (m *Manager) restartContainer(ctx context.Context, botID, containerID string) {
	m.grpcPool.Remove(botID)
	if err := m.service.DeleteTask(ctx, containerID, &ctr.DeleteTaskOptions{Force: true}); err != nil && !ctr.IsNotFound(err) {
		m.logger.Warn("cleanup stale task after data operation failed",
			slog.String("container_id", containerID), slog.Any("error", err))
		return
	}
	if err := m.startTaskAndEnsureNetwork(ctx, botID, containerID); err != nil {
		m.logger.Error("restart after data operation failed",
			slog.String("container_id", containerID), slog.Any("error", err))
		return
	}
}

func (m *Manager) stopTaskForMutation(ctx context.Context, botID, containerID string) (func(), error) {
	if err := m.safeStopTask(ctx, containerID); err != nil {
		return nil, err
	}
	restartCtx := context.WithoutCancel(ctx)
	return func() {
		m.restartContainer(restartCtx, botID, containerID)
	}, nil
}

func (*Manager) preserveDataToArchive(
	ctx context.Context,
	archivePath string,
	mounts []mount.Mount,
	preserveCredentials bool,
) error {
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o750); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	f, err := createPrivateWorkspaceArchive(archivePath)
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}

	writeErr := mount.WithReadonlyTempMount(ctx, mounts, func(root string) error {
		dataDir := mountedDataDir(root)
		if _, statErr := os.Stat(dataDir); statErr != nil {
			return nil // no /data to backup
		}
		return tarGzDir(f, dataDir, preserveCredentials)
	})

	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(archivePath)
		return fmt.Errorf("export data: %w", writeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func (m *Manager) preserveDataToBackup(ctx context.Context, botID string, mounts []mount.Mount) error {
	return m.preserveDataToArchive(ctx, m.backupPath(botID), mounts, true)
}

func (m *Manager) preserveDataBeforeDelete(ctx context.Context, botID string) error {
	ref, err := m.loadLockedContainer(ctx, botID)
	if err != nil {
		return fmt.Errorf("get workspace runtime for preserve: %w", err)
	}
	defer ref.Close()

	mounts, err := m.snapshotMounts(ctx, ref.info)
	if errors.Is(err, errMountNotSupported) {
		return m.preserveDataViaGRPC(ctx, botID, m.backupPath(botID), true)
	}
	if err != nil {
		return err
	}

	if err := m.safeStopTask(ctx, ref.containerID); err != nil {
		return fmt.Errorf("stop for data preserve: %w", err)
	}
	if err := m.preserveDataToBackup(ctx, botID, mounts); err != nil {
		m.restartContainer(ctx, botID, ref.containerID)
		return err
	}
	return nil
}

func mountedDataDir(root string) string {
	return filepath.Join(root, strings.TrimPrefix(containerDataDir, string(filepath.Separator)))
}

func (m *Manager) backupPath(botID string) string {
	return filepath.Join(m.dataRoot(), backupsSubdir, botID+".tar.gz")
}

func createPrivateWorkspaceArchive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: operator-controlled path
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (*Manager) archiveSnapshotKey(botID string) string {
	return archivePrefix + filepath.ToSlash(filepath.Join(botID, fmt.Sprintf("%d.tar.gz", time.Now().UnixNano())))
}

func (m *Manager) archiveSnapshotPath(key string) string {
	rel := strings.TrimPrefix(strings.TrimSpace(key), archivePrefix)
	return filepath.Join(m.dataRoot(), snapshotsSubdir, filepath.FromSlash(rel))
}

// ---------------------------------------------------------------------------
// gRPC fallback (Apple backend / no mount support)
// ---------------------------------------------------------------------------

// CountData returns the number of regular files under the container's /data
// directory, read live over the gRPC bridge so it never stops the container.
// It is best-effort context for the export dialog; callers should treat an
// error (e.g. a stopped or unreachable container) as "unknown".
func (m *Manager) CountData(ctx context.Context, botID string) (int, error) {
	client, err := m.nativeMCPClient(ctx, botID)
	if err != nil {
		return 0, fmt.Errorf("grpc connect: %w", err)
	}
	entries, err := client.ListDirAll(ctx, containerDataDir, true)
	if err != nil {
		return 0, fmt.Errorf("list dir: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.GetIsDir() {
			count++
		}
	}
	return count, nil
}

func (m *Manager) exportDataViaGRPC(
	ctx context.Context,
	botID string,
	preserveCredentials bool,
) (io.ReadCloser, error) {
	client, err := m.nativeMCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("grpc connect: %w", err)
	}

	entries, err := client.ListDirAll(ctx, containerDataDir, true)
	if err != nil {
		return nil, fmt.Errorf("list dir: %w", err)
	}

	pr, pw := io.Pipe()
	go func() {
		gw := gzip.NewWriter(pw)
		tw := tar.NewWriter(gw)
		var writeErr error
		defer func() {
			_ = tw.Close()
			_ = gw.Close()
			_ = pw.CloseWithError(writeErr)
		}()

		for _, entry := range entries {
			if entry.GetIsDir() {
				continue
			}
			if !isWorkspaceArchiveRegularMode(entry.GetMode()) {
				continue
			}
			relPath := strings.TrimPrefix(entry.GetPath(), "/")
			if shouldSkipWorkspaceArchivePath(relPath, false, preserveCredentials) {
				continue
			}
			absPath := containerDataDir + "/" + strings.TrimPrefix(relPath, "/")

			r, readErr := client.ReadRaw(ctx, absPath)
			if readErr != nil {
				writeErr = fmt.Errorf("read %s: %w", absPath, readErr)
				return
			}
			hdr := &tar.Header{
				Name: relPath,
				Size: entry.GetSize(),
				Mode: 0o644,
			}
			if writeErr = tw.WriteHeader(hdr); writeErr != nil {
				_ = r.Close()
				return
			}
			if _, writeErr = io.Copy(tw, r); writeErr != nil {
				_ = r.Close()
				return
			}
			_ = r.Close()
		}
	}()

	return pr, nil
}

func (m *Manager) preserveDataViaGRPC(
	ctx context.Context,
	botID string,
	backupPath string,
	preserveCredentials bool,
) error {
	reader, err := m.exportDataViaGRPC(ctx, botID, preserveCredentials)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	if err := os.MkdirAll(filepath.Dir(backupPath), 0o750); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	f, err := createPrivateWorkspaceArchive(backupPath)
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}
	if _, err := io.Copy(f, reader); err != nil {
		_ = f.Close()
		_ = os.Remove(backupPath)
		return err
	}
	return f.Close()
}

func (m *Manager) createArchiveSnapshotFromRef(ctx context.Context, ref *lockedContainerRef, archiveKey string) error {
	archivePath := m.archiveSnapshotPath(archiveKey)
	mounts, mountErr := m.snapshotMounts(ctx, ref.info)
	if errors.Is(mountErr, errMountNotSupported) {
		var lastErr error
		for range 20 {
			m.grpcPool.Remove(ref.botID)
			if err := m.preserveDataViaGRPC(ctx, ref.botID, archivePath, false); err == nil {
				return nil
			} else {
				lastErr = err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		return lastErr
	}
	if mountErr != nil {
		return mountErr
	}
	restartTask, err := ref.StopTaskForMutation(ctx)
	if err != nil {
		return fmt.Errorf("stop workspace runtime: %w", err)
	}
	defer restartTask()
	return m.preserveDataToArchive(ctx, archivePath, mounts, false)
}

func (m *Manager) restoreArchiveSnapshotFromRef(ctx context.Context, ref *lockedContainerRef, archiveKey string) error {
	archivePath := m.archiveSnapshotPath(archiveKey)
	f, err := os.Open(archivePath) //nolint:gosec // G304: archive path is derived from managed snapshot metadata
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	client, err := m.nativeMCPClient(ctx, ref.botID)
	if err != nil {
		return fmt.Errorf("grpc connect: %w", err)
	}
	_ = client.DeleteFile(ctx, containerDataDir, true)
	if err := client.Mkdir(ctx, containerDataDir); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}
	return m.importDataViaGRPC(ctx, ref.botID, f, false)
}

func (m *Manager) importDataViaGRPC(
	ctx context.Context,
	botID string,
	r io.Reader,
	preserveCredentials bool,
) error {
	client, err := m.nativeMCPClient(ctx, botID)
	if err != nil {
		return fmt.Errorf("grpc connect: %w", err)
	}

	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gr.Close() }()

	if !preserveCredentials {
		if err := cleanWorkspaceRuntimeSecretsViaGRPC(ctx, client); err != nil {
			return err
		}
	}

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		target, err := sanitizeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if target == "" || shouldSkipWorkspaceArchivePath(target, false, preserveCredentials) {
			continue
		}
		absPath := containerDataDir + "/" + filepath.ToSlash(target)
		if _, err := client.WriteRaw(ctx, absPath, io.LimitReader(tr, header.Size)); err != nil {
			return fmt.Errorf("write %s: %w", absPath, err)
		}
	}
}

// ---------------------------------------------------------------------------
// tar.gz helpers
// ---------------------------------------------------------------------------

// tarGzDir writes a gzip-compressed tar archive of all files under dir to w.
// Paths inside the archive are relative to dir.
func tarGzDir(w io.Writer, dir string, preserveCredentials bool) error {
	gw := gzip.NewWriter(w)
	defer func() { _ = gw.Close() }()
	tw := tar.NewWriter(gw)
	defer func() { _ = tw.Close() }()

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if shouldSkipWorkspaceArchivePath(rel, d.IsDir(), preserveCredentials) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(rel)
			return tw.WriteHeader(header)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		// For regular files: open first, then Fstat on the same fd so that
		// the size in the tar header is guaranteed to match the content we
		// read. This avoids race conditions and overlayfs size mismatches
		// that cause "archive/tar: write too long".
		f, err := os.Open(path) //nolint:gosec // G304: iterating operator-controlled data directory
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()

		fileInfo, err := f.Stat()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(fileInfo, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		_, err = io.Copy(tw, io.LimitReader(f, fileInfo.Size()))
		return err
	})
}

func shouldSkipWorkspaceArchivePath(rel string, isDir, preserveCredentials bool) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	rel = strings.TrimPrefix(rel, "/")
	sub, ok := workspaceRuntimeHomeSubpath(rel)
	if !ok {
		return false
	}
	if preserveCredentials {
		if isDir {
			return workspaceRuntimeStateDirSubpath(sub)
		}
		return workspaceRuntimeStateDirSubpath(filepath.ToSlash(filepath.Dir(sub))) ||
			sub == "state.db" || strings.HasPrefix(sub, "state.db-")
	}
	if isDir {
		return workspaceRuntimeSecretDirSubpath(sub)
	}
	switch {
	case sub == ".env", sub == "auth.json", sub == ".credentials.json":
		return true
	case strings.HasSuffix(sub, "/.env"), strings.HasSuffix(sub, "/auth.json"), strings.HasSuffix(sub, "/.credentials.json"):
		return true
	case workspaceRuntimeSecretDirSubpath(filepath.ToSlash(filepath.Dir(sub))):
		return true
	case sub == "state.db", strings.HasPrefix(sub, "state.db-"):
		return true
	default:
		return false
	}
}

func isWorkspaceArchiveRegularMode(mode string) bool {
	return strings.HasPrefix(mode, "-")
}

func workspaceRuntimeSecretDirSubpath(sub string) bool {
	sub = strings.Trim(strings.TrimSpace(filepath.ToSlash(sub)), "/")
	if sub == "" || sub == "." {
		return false
	}
	for _, part := range strings.Split(sub, "/") {
		if _, ok := workspaceRuntimeSecretDirNames[part]; ok {
			return true
		}
	}
	return false
}

func workspaceRuntimeStateDirSubpath(sub string) bool {
	for _, part := range strings.Split(strings.Trim(filepath.ToSlash(sub), "/"), "/") {
		if part == "sessions" || part == "projects" {
			return true
		}
	}
	return false
}

func workspaceRuntimeHomeSubpath(rel string) (string, bool) {
	for _, prefix := range workspaceRuntimeHomePrefixes {
		if strings.HasPrefix(rel, prefix) {
			return strings.TrimPrefix(rel, prefix), true
		}
	}
	return "", false
}

func cleanWorkspaceRuntimeSecretsViaGRPC(ctx context.Context, client *bridge.Client) error {
	entries, err := client.ListDirAll(ctx, containerDataDir, true)
	if err != nil {
		return fmt.Errorf("list workspace for runtime secret cleanup: %w", err)
	}
	for _, entry := range entries {
		relPath := strings.TrimPrefix(entry.GetPath(), "/")
		if !shouldSkipWorkspaceArchivePath(relPath, entry.GetIsDir(), false) {
			continue
		}
		absPath := containerDataDir + "/" + strings.TrimPrefix(relPath, "/")
		if err := client.DeleteFile(ctx, absPath, entry.GetIsDir()); err != nil {
			return fmt.Errorf("delete workspace runtime secret %s: %w", absPath, err)
		}
	}
	return nil
}

func cleanWorkspaceRuntimeSecretsInDir(root string) error {
	for _, prefix := range workspaceRuntimeHomePrefixes {
		base := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(prefix, "/")))
		if _, err := os.Stat(base); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !shouldSkipWorkspaceArchivePath(rel, d.IsDir(), false) {
				return nil
			}
			if d.IsDir() {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// untarGzDir extracts a gzip-compressed tar archive into dst.
func untarGzDir(r io.Reader, dst string, preserveCredentials bool) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)
	root, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	defer func() { _ = root.Close() }()

	if !preserveCredentials {
		if err := cleanWorkspaceRuntimeSecretsInDir(dst); err != nil {
			return fmt.Errorf("clean runtime secrets: %w", err)
		}
	}

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		target, err := sanitizeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}
		if shouldSkipWorkspaceArchivePath(target, header.Typeflag == tar.TypeDir, preserveCredentials) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			mode := header.FileInfo().Mode().Perm()
			if err := root.MkdirAll(target, mode); err != nil {
				return err
			}
		case tar.TypeReg:
			mode := header.FileInfo().Mode().Perm()
			parent := filepath.Dir(target)
			if parent != "." && parent != "" {
				if err := root.MkdirAll(parent, 0o750); err != nil {
					return err
				}
			}
			f, err := root.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // G110: decompression bomb not a concern for operator archives
				_ = f.Close()
				return err
			}
			_ = f.Close()
		}
	}
}

// sanitizeArchivePath converts a tar header path into a safe relative path.
// Empty or "." paths are ignored.
func sanitizeArchivePath(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == "" {
		return "", nil
	}
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("tar absolute path is not allowed: %s", name)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar path traversal: %s", name)
	}
	return clean, nil
}
