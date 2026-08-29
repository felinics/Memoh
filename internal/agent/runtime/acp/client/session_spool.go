// Host-side spool lifecycle: capture admission, the on-disk spool a
// snapshot owns, and the verified record streams read from it.
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

// handoverSessionSpoolSlot releases a finished spool's capture-admission slot
// early by converting it into a byte reservation against the global spool
// budget. The capture semaphore then only bounds concurrent bridge reads and
// hashing; a finished spool that merely waits on its database write (or
// restore) no longer serializes unrelated sessions' checkpoints. When the byte
// budget is exhausted the slot is kept for the spool's lifetime instead, so
// total spool disk stays bounded either way.
func handoverSessionSpoolSlot(size int64, releaseSlot func()) func() {
	if releaseSlot == nil {
		return nil
	}
	if size <= 0 {
		return releaseSlot
	}
	for {
		used := runtimeSessionSpoolBudgetUsed.Load()
		if used+size > runtimeSessionSpoolBudgetBytes {
			return releaseSlot
		}
		if runtimeSessionSpoolBudgetUsed.CompareAndSwap(used, used+size) {
			releaseSlot()
			var once sync.Once
			return func() {
				once.Do(func() { runtimeSessionSpoolBudgetUsed.Add(-size) })
			}
		}
	}
}

type sessionSpoolFile struct {
	path        string
	spoolPath   string
	hash        [sha256.Size]byte
	size        int64
	recordCount int64
	// prefixRecords/prefixDigest snapshot the running content digest at the
	// caller-supplied append boundary (the previous canonical record count).
	// The store compares prefixDigest with the canonical version's full-file
	// digest: equality proves the stored prefix is byte-identical and only the
	// tail past prefixRecords needs to be written.
	prefixRecords int64
	prefixDigest  [sha256.Size]byte
	prefixTaken   bool
}

// SessionStateSnapshot owns a 0700 host-side spool directory. It is reusable:
// every Records call opens an independent ordered stream, which lets a bundled
// adapter retry restoration after a dynamic adapter fails. Close is mandatory.
type SessionStateSnapshot struct {
	state       SessionState
	cursor      SessionStateCursor
	files       []sessionSpoolFile
	fileCount   int32
	recordCount int64
	encodedSize int64
	root        string
	release     func()

	mu      sync.Mutex
	closed  bool
	readers map[*sessionStateRecordStream]struct{}
}

func (s *SessionStateSnapshot) State() SessionState {
	if s == nil {
		return SessionState{}
	}
	return s.state
}

func (s *SessionStateSnapshot) Cursor() SessionStateCursor {
	if s == nil {
		return SessionStateCursor{}
	}
	return s.cursor
}

func (s *SessionStateSnapshot) FileCount() int32 {
	if s == nil {
		return 0
	}
	return s.fileCount
}

func (s *SessionStateSnapshot) RecordCount() int64 {
	if s == nil {
		return 0
	}
	return s.recordCount
}

// FileShapes returns each captured file's shape in path order, including the
// append-boundary proof when one was taken during capture.
func (s *SessionStateSnapshot) FileShapes() []SessionStateFileShape {
	if s == nil {
		return nil
	}
	shapes := make([]SessionStateFileShape, 0, len(s.files))
	for _, file := range s.files {
		if file.recordCount == 0 {
			continue
		}
		shape := SessionStateFileShape{
			Path:    file.path,
			Records: file.recordCount,
			Digest:  hex.EncodeToString(file.hash[:]),
		}
		if file.prefixTaken {
			shape.PrefixRecords = file.prefixRecords
			shape.PrefixDigest = hex.EncodeToString(file.prefixDigest[:])
		}
		shapes = append(shapes, shape)
	}
	sort.Slice(shapes, func(i, j int) bool { return shapes[i].Path < shapes[j].Path })
	return shapes
}

// Records opens a new verified pass over the snapshot. Each spool file is
// hashed again as it is read; a changed or truncated host spool therefore
// aborts the surrounding database transaction or RuntimeLease restoration.
func (s *SessionStateSnapshot) Records() (SessionStateRecordReader, error) {
	if s == nil {
		return nil, errors.New("ACP session snapshot is unavailable")
	}
	stream := &sessionStateRecordStream{snapshot: s}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("ACP session snapshot is closed")
	}
	if s.readers == nil {
		s.readers = make(map[*sessionStateRecordStream]struct{})
	}
	s.readers[stream] = struct{}{}
	s.mu.Unlock()
	return stream.next, nil
}

func (s *SessionStateSnapshot) unregister(stream *sessionStateRecordStream) {
	s.mu.Lock()
	delete(s.readers, stream)
	s.mu.Unlock()
}

func (s *SessionStateSnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	readers := make([]*sessionStateRecordStream, 0, len(s.readers))
	for reader := range s.readers {
		readers = append(readers, reader)
	}
	s.readers = nil
	root := s.root
	release := s.release
	s.release = nil
	s.mu.Unlock()
	if release != nil {
		defer release()
	}
	for _, reader := range readers {
		reader.close()
	}
	if root == "" {
		return nil
	}
	return os.RemoveAll(root)
}

type sessionStateRecordStream struct {
	mu       sync.Mutex
	snapshot *SessionStateSnapshot
	index    int
	file     *os.File
	scanner  *bufio.Scanner
	hasher   hash.Hash
	read     int64
	line     int64
	total    int64
	closed   bool
}

func (r *sessionStateRecordStream) next(ctx context.Context) (SessionStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return SessionStateRecord{}, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			r.closeLocked()
			return SessionStateRecord{}, err
		}
		if r.scanner == nil {
			if err := r.openNextFile(); err != nil {
				return SessionStateRecord{}, err
			}
			if r.scanner == nil {
				if r.total != r.snapshot.recordCount {
					err := fmt.Errorf("ACP session spool has %d records, want %d", r.total, r.snapshot.recordCount)
					r.closeLocked()
					return SessionStateRecord{}, err
				}
				r.closeLocked()
				return SessionStateRecord{}, io.EOF
			}
		}
		if r.scanner.Scan() {
			r.line++
			r.total++
			content := append(json.RawMessage(nil), r.scanner.Bytes()...)
			if len(content) == 0 || len(content) > runtimeSessionMaxLineSize || !json.Valid(content) {
				err := fmt.Errorf("ACP session spool %q line %d is invalid", r.snapshot.files[r.index-1].path, r.line)
				r.closeLocked()
				return SessionStateRecord{}, err
			}
			return SessionStateRecord{
				FilePath:   r.snapshot.files[r.index-1].path,
				LineNumber: r.line,
				Content:    content,
			}, nil
		}
		if err := r.finishFile(); err != nil {
			r.closeLocked()
			return SessionStateRecord{}, err
		}
	}
}

func (r *sessionStateRecordStream) openNextFile() error {
	for r.index < len(r.snapshot.files) {
		meta := r.snapshot.files[r.index]
		r.index++
		if meta.recordCount == 0 {
			continue
		}
		file, err := os.Open(meta.spoolPath) //nolint:gosec // path belongs to the snapshot's private temp directory.
		if err != nil {
			r.closeLocked()
			return fmt.Errorf("open ACP session spool %q: %w", meta.path, err)
		}
		r.file = file
		r.hasher = sha256.New()
		counted := &countingReader{reader: io.TeeReader(file, r.hasher), count: &r.read}
		r.read = 0
		r.line = 0
		r.scanner = bufio.NewScanner(counted)
		r.scanner.Buffer(make([]byte, 64*1024), runtimeSessionMaxLineSize+1)
		return nil
	}
	return nil
}

func (r *sessionStateRecordStream) finishFile() error {
	meta := r.snapshot.files[r.index-1]
	if scanErr := r.scanner.Err(); scanErr != nil {
		return fmt.Errorf("scan ACP session spool %q: %w", meta.path, scanErr)
	}
	if closeErr := r.file.Close(); closeErr != nil {
		return fmt.Errorf("close ACP session spool %q: %w", meta.path, closeErr)
	}
	r.file = nil
	r.scanner = nil
	if r.line != meta.recordCount || r.read != meta.size {
		return fmt.Errorf("ACP session spool %q changed size or record count", meta.path)
	}
	var actual [sha256.Size]byte
	copy(actual[:], r.hasher.Sum(nil))
	if actual != meta.hash {
		return fmt.Errorf("ACP session spool %q changed content", meta.path)
	}
	return nil
}

func (r *sessionStateRecordStream) close() {
	r.mu.Lock()
	r.closeLocked()
	r.mu.Unlock()
}

func (r *sessionStateRecordStream) closeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	r.scanner = nil
	if r.snapshot != nil {
		r.snapshot.unregister(r)
	}
}

type countingReader struct {
	reader io.Reader
	count  *int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	*r.count += int64(n)
	return n, err
}

type sessionSpoolBuilder struct {
	root        string
	state       SessionState
	files       []sessionSpoolFile
	fileCount   int32
	recordCount int64
	encodedSize int64
	primary     primaryStateValidator
	receipt     *claudeReceiptValidator
	release     func()
	// boundaries maps file path to the previous canonical record count. While
	// spooling, the builder snapshots the running content digest at each
	// boundary so the store can prove the canonical prefix is unchanged and
	// append only the tail.
	boundaries map[string]int64
}

func newSessionSpoolBuilder(
	ctx context.Context,
	locator acpprofile.RuntimeSessionLocator,
	state SessionState,
	receipt *SessionStateReceipt,
	boundaries map[string]int64,
) (*sessionSpoolBuilder, error) {
	runtimeSessionSpoolCleanup.Do(func() {
		cleanupStaleSessionSpools(os.TempDir(), time.Now())
	})
	release, err := acquireSessionSpool(ctx)
	if err != nil {
		return nil, fmt.Errorf("admit ACP session spool: %w", err)
	}
	root, err := os.MkdirTemp("", sessionSpoolProcessPrefix())
	if err != nil {
		release()
		return nil, fmt.Errorf("create ACP session spool: %w", err)
	}
	builder := &sessionSpoolBuilder{
		root:       root,
		release:    release,
		state:      state,
		primary:    primaryStateValidator{locator: locator, sessionID: state.SessionID},
		boundaries: boundaries,
	}
	if receipt != nil {
		validator, err := newClaudeReceiptValidator(state.SessionID, receipt)
		if err != nil {
			builder.abort()
			return nil, err
		}
		builder.receipt = validator
	}
	return builder, nil
}

func (b *sessionSpoolBuilder) abort() {
	if b == nil {
		return
	}
	root := b.root
	release := b.release
	b.root = ""
	b.release = nil
	if root != "" {
		_ = os.RemoveAll(root)
	}
	if release != nil {
		release()
	}
}

func acquireSessionSpool(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case runtimeSessionSpoolAdmission <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-runtimeSessionSpoolAdmission
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { <-runtimeSessionSpoolAdmission })
	}, nil
}

// sessionSpoolProcessPrefix scopes every spool directory to the creating
// process. Spools are process-transient - nothing references them after the
// process dies - so cleanup reclaims a provably dead process's spools
// immediately, which keeps disk usage bounded across repeated crashes (the
// in-memory byte budget resets with the process). The age window remains the
// backstop for everything a pid probe cannot prove.
func sessionSpoolProcessPrefix() string {
	return fmt.Sprintf("%s%d-", runtimeSessionSpoolPrefix, os.Getpid())
}

func cleanupStaleSessionSpools(tempDir string, now time.Time) {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return
	}
	cutoff := now.Add(-runtimeSessionSpoolStaleAge)
	for _, entry := range entries {
		name := entry.Name()
		if len(name) <= len(runtimeSessionSpoolPrefix) || !strings.HasPrefix(name, runtimeSessionSpoolPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		// A provably dead owner is reclaimed immediately. Everything else -
		// live owners, inconclusive probes (a recycled pid now owned by an
		// unrelated process), and legacy names - falls through to the age
		// backstop: captures are bounded by sessionStateIOTimeout, so a spool
		// older than the window is never a live capture.
		if pid, ok := spoolOwnerPID(name); ok && !spoolProcessAlive(pid) {
			_ = os.RemoveAll(filepath.Join(tempDir, name))
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(tempDir, name))
		}
	}
}

// spoolOwnerPID parses the owning pid out of a spool directory name. ok is
// false for legacy names created before spools were pid-scoped.
func spoolOwnerPID(name string) (int, bool) {
	rest := strings.TrimPrefix(name, runtimeSessionSpoolPrefix)
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(rest[:dash])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// spoolProcessAlive probes a pid with signal 0. ESRCH / ErrProcessDone prove
// death; anything else (notably EPERM for a process owned by another user)
// counts as alive, so cleanup only ever deletes eagerly when the owner is
// provably gone.
func spoolProcessAlive(pid int) bool {
	if pid == os.Getpid() {
		return true
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	defer func() { _ = process.Release() }()
	signalErr := process.Signal(syscall.Signal(0))
	if signalErr == nil {
		return true
	}
	return !errors.Is(signalErr, os.ErrProcessDone) && !errors.Is(signalErr, syscall.ESRCH)
}

// spoolFileWriter is the single implementation of "write one file into the
// spool": every record is compacted content + LF, hashed as written, counted
// against the per-file and whole-spool budgets, and the running digest is
// snapshotted at the caller-declared append boundary. Both capture (bridge
// reader) and restore (database records) feed records through it, so the
// framing behind every stored digest has exactly one definition.
type spoolFileWriter struct {
	builder      *sessionSpoolBuilder
	path         string
	spoolPath    string
	file         *os.File
	hasher       hash.Hash
	writer       io.Writer
	boundary     int64
	prefixDigest [sha256.Size]byte
	prefixTaken  bool
	encoded      int64
	records      int64
}

func (b *sessionSpoolBuilder) beginFile(filePath string) (*spoolFileWriter, error) {
	spoolPath := filepath.Join(b.root, fmt.Sprintf("%06d.jsonl", len(b.files)))
	file, err := os.OpenFile(spoolPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // private generated spool path.
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	return &spoolFileWriter{
		builder:   b,
		path:      filePath,
		spoolPath: spoolPath,
		file:      file,
		hasher:    hasher,
		writer:    io.MultiWriter(file, hasher),
		boundary:  b.boundaries[filePath],
	}, nil
}

func (w *spoolFileWriter) writeRecord(content json.RawMessage) error {
	if err := w.builder.observe(w.path, content); err != nil {
		return err
	}
	n, err := w.writer.Write(content)
	w.encoded += int64(n)
	if err == nil {
		n, err = w.writer.Write([]byte{'\n'})
		w.encoded += int64(n)
	}
	if err != nil {
		return err
	}
	w.records++
	if w.boundary > 0 && w.records == w.boundary {
		// Sum is non-destructive: this is the digest over exactly the records
		// the previous canonical version ended at.
		copy(w.prefixDigest[:], w.hasher.Sum(nil))
		w.prefixTaken = true
	}
	if w.records > runtimeSessionMaxLinesPerFile {
		return fmt.Errorf("ACP session file %q exceeds %d records", w.path, runtimeSessionMaxLinesPerFile)
	}
	return nil
}

// commit closes the spool file and registers it with the builder, enforcing
// the whole-spool budgets. abort() on the writer is unnecessary: the builder's
// own abort removes the entire spool directory.
func (w *spoolFileWriter) commit() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	b := w.builder
	b.recordCount += w.records
	b.encodedSize += w.encoded
	if b.recordCount > runtimeSessionMaxRecords {
		return fmt.Errorf("ACP session state contains more than %d records", runtimeSessionMaxRecords)
	}
	if b.encodedSize > runtimeSessionMaxTotal {
		return fmt.Errorf("ACP session state exceeds %d bytes", runtimeSessionMaxTotal)
	}
	var digest [sha256.Size]byte
	copy(digest[:], w.hasher.Sum(nil))
	b.files = append(b.files, sessionSpoolFile{
		path: w.path, spoolPath: w.spoolPath, hash: digest, size: w.encoded, recordCount: w.records,
		prefixRecords: w.boundary, prefixDigest: w.prefixDigest, prefixTaken: w.prefixTaken,
	})
	if w.records > 0 {
		b.fileCount++
	}
	return nil
}

func (b *sessionSpoolBuilder) addFileFromReader(ctx context.Context, filePath string, reader io.Reader) error {
	writer, err := b.beginFile(filePath)
	if err != nil {
		return err
	}
	if err := scanSessionJSONL(ctx, reader, writer.writeRecord); err != nil {
		_ = writer.file.Close()
		return err
	}
	return writer.commit()
}

func (b *sessionSpoolBuilder) addFileFromRecords(
	ctx context.Context,
	filePath string,
	first SessionStateRecord,
	reader SessionStateRecordReader,
	pending *SessionStateRecord,
) error {
	writer, err := b.beginFile(filePath)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = writer.file.Close()
		return err
	}
	current := first
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if current.FilePath != filePath {
			*pending = current
			break
		}
		if current.LineNumber != writer.records+1 {
			return fail(fmt.Errorf("ACP session file %q record number is %d, want %d", filePath, current.LineNumber, writer.records+1))
		}
		content, err := compactSessionRecord(current.Content)
		if err != nil {
			return fail(fmt.Errorf("ACP session file %q line %d: %w", filePath, writer.records+1, err))
		}
		if err := writer.writeRecord(content); err != nil {
			return fail(err)
		}
		next, readErr := reader(ctx)
		if errors.Is(readErr, io.EOF) {
			pending.FilePath = ""
			break
		}
		if readErr != nil {
			return fail(readErr)
		}
		current = next
	}
	return writer.commit()
}

func (b *sessionSpoolBuilder) observe(filePath string, content json.RawMessage) error {
	if filePath == b.state.TranscriptPath {
		b.primary.observe(content)
	}
	if b.receipt != nil {
		if err := b.receipt.observe(content); err != nil {
			return fmt.Errorf("validate Claude transcript %q: %w", filePath, err)
		}
	}
	return nil
}

func (b *sessionSpoolBuilder) finish() (*SessionStateSnapshot, error) {
	if b.fileCount == 0 || b.recordCount == 0 {
		return nil, errors.New("ACP session state contains no JSONL records")
	}
	if !b.primary.valid() {
		return nil, fmt.Errorf("primary ACP transcript %q does not contain session ID %q in the agent's audited header format", b.state.TranscriptPath, b.state.SessionID)
	}
	if b.receipt != nil {
		if err := b.receipt.validate(); err != nil {
			return nil, err
		}
	}
	snapshot := &SessionStateSnapshot{
		state: b.state, cursor: b.primary.cursor(), files: b.files,
		fileCount: b.fileCount, recordCount: b.recordCount, encodedSize: b.encodedSize,
		root: b.root, release: handoverSessionSpoolSlot(b.encodedSize, b.release),
		readers: make(map[*sessionStateRecordStream]struct{}),
	}
	b.root = ""
	b.release = nil
	return snapshot, nil
}

func scanSessionJSONL(ctx context.Context, reader io.Reader, consume func(json.RawMessage) error) error {
	var rawBytes int64
	limited := io.LimitReader(reader, runtimeSessionMaxFileSize+1)
	scanner := bufio.NewScanner(&countingReader{reader: limited, count: &rawBytes})
	scanner.Buffer(make([]byte, 64*1024), runtimeSessionMaxLineSize+1)
	var physicalLines int64
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		physicalLines++
		if physicalLines > runtimeSessionMaxLinesPerFile {
			return fmt.Errorf("JSONL contains more than %d physical lines", runtimeSessionMaxLinesPerFile)
		}
		trimmed := bytes.TrimSpace(scanner.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		content, err := compactSessionRecord(trimmed)
		if err != nil {
			return fmt.Errorf("line %d: %w", physicalLines, err)
		}
		if err := consume(content); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan JSONL within %d-byte line limit: %w", runtimeSessionMaxLineSize, err)
	}
	if rawBytes > runtimeSessionMaxFileSize {
		return fmt.Errorf("JSONL exceeds %d bytes", runtimeSessionMaxFileSize)
	}
	return nil
}

func compactSessionRecord(raw []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > runtimeSessionMaxLineSize {
		return nil, fmt.Errorf("JSON value is empty or exceeds %d bytes", runtimeSessionMaxLineSize)
	}
	var compact bytes.Buffer
	compact.Grow(len(trimmed))
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, fmt.Errorf("value is not valid JSON: %w", err)
	}
	return append(json.RawMessage(nil), compact.Bytes()...), nil
}

func cloneSnapshotManifest(snapshot *SessionStateSnapshot) *SessionStateSnapshot {
	if snapshot == nil {
		return nil
	}
	files := make([]sessionSpoolFile, len(snapshot.files))
	for index, file := range snapshot.files {
		files[index] = file
		files[index].spoolPath = ""
	}
	return &SessionStateSnapshot{
		state: snapshot.state, cursor: snapshot.cursor, files: files,
		fileCount: snapshot.fileCount, recordCount: snapshot.recordCount, encodedSize: snapshot.encodedSize,
	}
}

func equalSessionStateSnapshot(first, second *SessionStateSnapshot) bool {
	if first == nil || second == nil || first.state != second.state || len(first.files) != len(second.files) {
		return false
	}
	for index := range first.files {
		left, right := first.files[index], second.files[index]
		if left.path != right.path || left.hash != right.hash || left.size != right.size || left.recordCount != right.recordCount {
			return false
		}
	}
	return true
}
