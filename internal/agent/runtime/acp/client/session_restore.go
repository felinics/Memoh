// Restore: materialize a database checkpoint back into a fresh runtime
// home so the adapter can resume its native session.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
)

// SpoolSessionState drains an ordered database record stream into a reusable,
// private host snapshot. The database transaction ends before any workspace
// bridge I/O begins.
func SpoolSessionState(
	ctx context.Context,
	locator acpprofile.RuntimeSessionLocator,
	sessionRoots []string,
	state SessionState,
	reader SessionStateRecordReader,
	expectedFileCount int32,
	expectedRecordCount int64,
) (*SessionStateSnapshot, error) {
	if reader == nil {
		return nil, invalidRestoredSessionState(errors.New("ACP session record reader is required"))
	}
	if _, err := validateSessionStateHeader(locator, sessionRoots, state); err != nil {
		return nil, invalidRestoredSessionState(err)
	}
	if expectedFileCount <= 0 || expectedFileCount > runtimeSessionMaxFiles || expectedRecordCount <= 0 || expectedRecordCount > runtimeSessionMaxRecords {
		return nil, invalidRestoredSessionState(errors.New("ACP session record counts are outside supported limits"))
	}
	builder, err := newSessionSpoolBuilder(ctx, locator, state, nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { builder.abort() }()
	first, err := reader(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("ACP session record stream is empty")
		}
		return nil, invalidRestoredSessionState(err)
	}
	var previousPath string
	pending := first
	for pending.FilePath != "" {
		filePath, err := validateSessionFilePathForRoots(pending.FilePath, sessionRoots)
		if err != nil {
			return nil, invalidRestoredSessionState(err)
		}
		if previousPath != "" && filePath <= previousPath {
			return nil, invalidRestoredSessionState(errors.New("ACP session files are not strictly path ordered"))
		}
		if !sessionFileBelongsToPrimary(locator, filePath, state.TranscriptPath, state.SessionID) {
			return nil, invalidRestoredSessionState(fmt.Errorf("ACP session file %q is unrelated to primary transcript", filePath))
		}
		pending.FilePath = filePath
		if err := builder.addFileFromRecords(ctx, filePath, pending, reader, &pending); err != nil {
			return nil, invalidRestoredSessionState(err)
		}
		previousPath = filePath
		if len(builder.files) > runtimeSessionMaxFiles {
			return nil, invalidRestoredSessionState(fmt.Errorf("ACP session has more than %d files", runtimeSessionMaxFiles))
		}
	}
	if builder.fileCount != expectedFileCount || builder.recordCount != expectedRecordCount {
		return nil, invalidRestoredSessionState(fmt.Errorf(
			"ACP session record stream has %d files/%d records, want %d/%d",
			builder.fileCount, builder.recordCount, expectedFileCount, expectedRecordCount,
		))
	}
	snapshot, err := builder.finish()
	if err != nil {
		return nil, invalidRestoredSessionState(err)
	}
	return snapshot, nil
}

func (p *bridgeProcess) RestoreSessionState(ctx context.Context, snapshot *SessionStateSnapshot) error {
	if p == nil || p.lease == nil {
		return errors.New("ACP runtime process is unavailable")
	}
	return p.lease.restoreSessionState(ctx, snapshot)
}

func (l *runtimeLease) restoreSessionState(ctx context.Context, snapshot *SessionStateSnapshot) error {
	if snapshot == nil {
		return invalidRestoredSessionState(errors.New("ACP session snapshot is required"))
	}
	locator, err := l.sessionStateLocator()
	if err != nil {
		return invalidRestoredSessionState(err)
	}
	state := snapshot.State()
	if _, err := validateSessionStateHeader(locator, l.sessionRoots, state); err != nil {
		return invalidRestoredSessionState(err)
	}
	reader, err := snapshot.Records()
	if err != nil {
		return invalidRestoredSessionState(err)
	}
	if err := l.writeSessionRecords(ctx, state, reader); err != nil {
		return fmt.Errorf("restore ACP session state: %w", err)
	}
	return nil
}

type restoreFileWriter struct {
	path string
	pipe *io.PipeWriter
	done chan error
}

func (l *runtimeLease) startRestoreFile(ctx context.Context, filePath string) *restoreFileWriter {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := l.client.WriteRawNoFollow(ctx, l.root, filePath, reader)
		_ = reader.CloseWithError(err)
		done <- err
	}()
	return &restoreFileWriter{path: filePath, pipe: writer, done: done}
}

func (w *restoreFileWriter) finish(err error) error {
	if w == nil {
		return err
	}
	if err != nil {
		_ = w.pipe.CloseWithError(err)
	} else {
		_ = w.pipe.Close()
	}
	writeErr := <-w.done
	if err != nil {
		return err
	}
	return writeErr
}

func (l *runtimeLease) writeSessionRecords(ctx context.Context, state SessionState, reader SessionStateRecordReader) error {
	locator, err := l.sessionStateLocator()
	if err != nil {
		return invalidRestoredSessionState(err)
	}
	var current *restoreFileWriter
	var currentPath string
	var currentLine int64
	var files int32
	var records int64
	for {
		record, err := reader(ctx)
		if errors.Is(err, io.EOF) {
			if current == nil {
				return invalidRestoredSessionState(errors.New("ACP session record stream is empty"))
			}
			if err := current.finish(nil); err != nil {
				return err
			}
			break
		}
		if err != nil {
			if current != nil {
				_ = current.finish(err)
			}
			return err
		}
		filePath, err := l.validateSessionFilePath(record.FilePath)
		if err != nil || !sessionFileBelongsToPrimary(locator, filePath, state.TranscriptPath, state.SessionID) {
			if err == nil {
				err = errors.New("ACP session file is unrelated to primary transcript")
			}
			if current != nil {
				_ = current.finish(err)
			}
			return invalidRestoredSessionState(err)
		}
		if filePath != currentPath {
			if currentPath != "" && filePath <= currentPath {
				err = errors.New("ACP session files are not strictly path ordered")
				_ = current.finish(err)
				return invalidRestoredSessionState(err)
			}
			if current != nil {
				if err := current.finish(nil); err != nil {
					return err
				}
			}
			current = l.startRestoreFile(ctx, filePath)
			currentPath = filePath
			currentLine = 0
			files++
		}
		currentLine++
		records++
		if record.LineNumber != currentLine || files > runtimeSessionMaxFiles || records > runtimeSessionMaxRecords {
			err = errors.New("ACP session record order or count is invalid")
			_ = current.finish(err)
			return invalidRestoredSessionState(err)
		}
		content, err := compactSessionRecord(record.Content)
		if err == nil {
			_, err = current.pipe.Write(content)
		}
		if err == nil {
			_, err = current.pipe.Write([]byte{'\n'})
		}
		if err != nil {
			_ = current.finish(err)
			return err
		}
	}
	if records == 0 {
		return invalidRestoredSessionState(errors.New("ACP session restore produced an empty or incomplete snapshot"))
	}
	return nil
}

func invalidRestoredSessionState(err error) error {
	if errors.Is(err, ErrSessionStateRestoreInvalid) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrSessionStateRestoreInvalid, err)
}
