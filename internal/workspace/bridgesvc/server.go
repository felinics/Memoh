package bridgesvc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

var errListDirEntryLimit = errors.New("directory listing exceeds the requested entry bound")

const (
	readMaxLines      = 2000
	readMaxBytes      = 0
	readMaxLineLen    = 0
	listMaxEntries    = 200
	binaryProbeBytes  = 8 * 1024
	rawChunkSize      = 64 * 1024
	DefaultWorkDir    = "/data"
	defaultTimeout    = 30
	defaultPTYTimeout = 5 * 60
)

type Options struct {
	DefaultWorkDir    string
	WorkspaceRoot     string
	DataMount         string
	AllowHostAbsolute bool
	ReverseHTTP       *ReverseHTTPBroker
}

type Server struct {
	pb.UnimplementedContainerServiceServer
	defaultWorkDir    string
	workspaceRoot     string
	dataMount         string
	allowHostAbsolute bool
	reverseHTTP       *ReverseHTTPBroker
}

func New(opts Options) *Server {
	defaultWorkDir := strings.TrimSpace(opts.DefaultWorkDir)
	if defaultWorkDir == "" {
		defaultWorkDir = DefaultWorkDir
	}
	dataMount := strings.TrimRight(strings.TrimSpace(opts.DataMount), string(filepath.Separator))
	if dataMount == "" {
		dataMount = DefaultWorkDir
	}
	workspaceRoot := strings.TrimSpace(opts.WorkspaceRoot)
	if workspaceRoot != "" {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = abs
		}
	}
	return &Server{
		defaultWorkDir:    filepath.Clean(defaultWorkDir),
		workspaceRoot:     filepath.Clean(workspaceRoot),
		dataMount:         filepath.Clean(dataMount),
		allowHostAbsolute: opts.AllowHostAbsolute,
		reverseHTTP:       opts.ReverseHTTP,
	}
}

func (s *Server) ReadFile(_ context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	path := req.GetPath()
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	path = s.resolvePath(path)

	f, err := os.Open(path) //nolint:gosec // G304: workspace bridge intentionally serves agent-selected paths.
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "open: %v", err)
	}
	defer func() { _ = f.Close() }()

	probe := make([]byte, binaryProbeBytes)
	n, _ := f.Read(probe)
	if bytes.IndexByte(probe[:n], 0) >= 0 {
		return &pb.ReadFileResponse{Binary: true}, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, status.Errorf(codes.Internal, "seek: %v", err)
	}

	lineOffset := req.GetLineOffset()
	if lineOffset < 1 {
		lineOffset = 1
	}
	nLines := req.GetNLines()
	if nLines < 1 || nLines > readMaxLines {
		nLines = readMaxLines
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentLine int32
	var totalLines int32
	var out strings.Builder
	var linesRead int32
	bytesWritten := 0

	for scanner.Scan() {
		currentLine++
		totalLines = currentLine
		if currentLine < lineOffset {
			continue
		}
		if linesRead >= nLines {
			continue
		}

		line := scanner.Text()
		if readMaxLineLen > 0 && utf8.RuneCountInString(line) > readMaxLineLen {
			line = truncateRunes(line, readMaxLineLen) + "..."
		}

		entry := line + "\n"
		if readMaxBytes > 0 && bytesWritten+len(entry) > readMaxBytes {
			break
		}
		out.WriteString(entry)
		bytesWritten += len(entry)
		linesRead++
	}

	for scanner.Scan() {
		totalLines++
	}

	return &pb.ReadFileResponse{
		Content:    out.String(),
		TotalLines: totalLines,
	}, nil
}

func (s *Server) WriteFile(ctx context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	path := req.GetPath()
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	if err := contextStatusError(ctx); err != nil {
		return nil, err
	}
	path = s.resolvePath(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir: %v", err)
	}
	if err := contextStatusError(ctx); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(ctx, path, req.GetContent()); err != nil {
		if code := status.Code(err); code != codes.Unknown {
			return nil, err
		}
		return nil, status.Errorf(codes.Internal, "write: %v", err)
	}
	return &pb.WriteFileResponse{}, nil
}

func contextStatusError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return nil
}

func writeFileAtomic(ctx context.Context, targetPath string, content []byte) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(targetPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := contextStatusError(ctx); err != nil {
		return err
	}
	// #nosec G703 -- targetPath is resolved through safePath, and tmpPath is created in the same validated directory.
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *Server) ListDir(ctx context.Context, req *pb.ListDirRequest) (*pb.ListDirResponse, error) {
	dir := req.GetPath()
	if dir == "" {
		dir = "."
	}
	dir = s.resolvePath(dir)

	// Without max_entries, listings are unbounded on purpose: general callers
	// (data export, data counting, secret cleanup) must see every entry,
	// matching the pre-existing behavior. Callers with a hard ceiling - the
	// ACP session-state capture - pass max_entries so the walk STOPS at the
	// bound instead of collecting an attacker-sized tree into memory first.
	maxEntries := int(req.GetMaxEntries())
	var all []*pb.FileEntry
	appendEntry := func(entry *pb.FileEntry) error {
		if entry == nil {
			return nil
		}
		if maxEntries > 0 && len(all) >= maxEntries {
			return errListDirEntryLimit
		}
		all = append(all, entry)
		return nil
	}

	if req.GetRecursive() {
		err := walkDirBatched(ctx, dir, func(rel string, d fs.DirEntry) error {
			entry, _ := buildFileEntry(rel, d)
			return appendEntry(entry)
		})
		if errors.Is(err, errListDirEntryLimit) {
			return nil, status.Errorf(codes.ResourceExhausted, "recursive listing exceeds %d entries", maxEntries)
		}
		if ctxErr := contextStatusError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "walk: %v", err)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].GetPath() < all[j].GetPath() })

		if threshold := req.GetCollapseThreshold(); threshold > 0 {
			all = collapseHeavySubdirs(all, int(threshold))
		}
	} else {
		err := readDirBatched(ctx, dir, func(d fs.DirEntry) error {
			entry, _ := buildFileEntry(d.Name(), d)
			return appendEntry(entry)
		})
		if err != nil {
			if errors.Is(err, errListDirEntryLimit) {
				return nil, status.Errorf(codes.ResourceExhausted, "directory listing exceeds %d entries", maxEntries)
			}
			if ctxErr := contextStatusError(ctx); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, status.Errorf(codes.NotFound, "readdir: %v", err)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].GetPath() < all[j].GetPath() })
	}

	totalCount := int32(min(len(all), math.MaxInt32)) //nolint:gosec // clamped
	offset := req.GetOffset()
	if offset < 0 {
		offset = 0
	}
	limit := req.GetLimit()
	if limit < 0 {
		limit = 0
	}
	if limit > listMaxEntries {
		limit = listMaxEntries
	}

	var entries []*pb.FileEntry
	if int(offset) < len(all) {
		entries = all[offset:]
	}
	if limit > 0 && int(limit) < len(entries) {
		entries = entries[:limit]
	}

	truncated := int(offset)+len(entries) < int(totalCount)
	return &pb.ListDirResponse{
		Entries:    entries,
		TotalCount: totalCount,
		Truncated:  truncated,
	}, nil
}

const listReadBatchEntries = 256

func readDirBatched(ctx context.Context, dir string, visit func(fs.DirEntry) error) error {
	file, err := os.Open(dir) //nolint:gosec // dir is resolved by the bridge workspace policy.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := file.ReadDir(listReadBatchEntries)
		for _, entry := range entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func walkDirBatched(ctx context.Context, root string, visit func(string, fs.DirEntry) error) error {
	directories := []string{root}
	for len(directories) > 0 {
		current := directories[len(directories)-1]
		directories = directories[:len(directories)-1]
		err := readDirBatched(ctx, current, func(entry fs.DirEntry) error {
			fullPath := filepath.Join(current, entry.Name())
			relPath, err := filepath.Rel(root, fullPath)
			if err != nil {
				return err
			}
			if err := visit(relPath, entry); err != nil {
				return err
			}
			if entry.IsDir() {
				directories = append(directories, fullPath)
			}
			return nil
		})
		if err != nil {
			// Preserve the old WalkDir behavior for an unreadable child while
			// still surfacing a missing or unreadable requested root.
			if current == root || errors.Is(err, errListDirEntryLimit) || ctx.Err() != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) Exec(stream pb.ContainerService_ExecServer) error {
	firstMsg, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "failed to receive exec config")
	}

	command := firstMsg.GetCommand()
	if command == "" {
		return status.Error(codes.InvalidArgument, "command is required")
	}

	if firstMsg.GetPty() {
		return s.execPTY(stream, firstMsg)
	}
	return s.execPipe(stream, firstMsg)
}

func (*Server) Tunnel(stream pb.ContainerService_TunnelServer) error {
	firstMsg, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "failed to receive tunnel open")
	}
	open := firstMsg.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "first tunnel frame must be open")
	}
	address, err := validateTunnelAddress(stream.Context(), open.GetAddress())
	if err != nil {
		return err
	}

	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(stream.Context(), "tcp", address)
	if err != nil {
		return status.Errorf(codes.Unavailable, "dial tunnel target: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var sendMu sync.Mutex
	sendFrame := func(frame *pb.TunnelFrame) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	if err := sendFrame(&pb.TunnelFrame{Frame: &pb.TunnelFrame_Data{Data: &pb.TunnelData{}}}); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := conn.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				if sendErr := sendFrame(&pb.TunnelFrame{Frame: &pb.TunnelFrame_Data{Data: &pb.TunnelData{Data: data}}}); sendErr != nil {
					return
				}
			}
			if readErr != nil {
				closeErr := ""
				if !errors.Is(readErr, io.EOF) {
					closeErr = readErr.Error()
				}
				_ = sendFrame(&pb.TunnelFrame{Frame: &pb.TunnelFrame_Close{Close: &pb.TunnelClose{Error: closeErr}}})
				return
			}
		}
	}()

	for {
		frame, recvErr := stream.Recv()
		if recvErr != nil {
			_ = conn.Close()
			<-done
			if errors.Is(recvErr, io.EOF) || stream.Context().Err() != nil {
				return nil
			}
			return recvErr
		}
		switch payload := frame.GetFrame().(type) {
		case *pb.TunnelFrame_Data:
			if len(payload.Data.GetData()) == 0 {
				continue
			}
			if _, err := conn.Write(payload.Data.GetData()); err != nil {
				_ = conn.Close()
				<-done
				return status.Errorf(codes.Unavailable, "write tunnel target: %v", err)
			}
		case *pb.TunnelFrame_Close:
			_ = conn.Close()
			<-done
			if msg := strings.TrimSpace(payload.Close.GetError()); msg != "" {
				return status.Error(codes.Canceled, msg)
			}
			return nil
		case *pb.TunnelFrame_Open:
			_ = conn.Close()
			<-done
			return status.Error(codes.InvalidArgument, "tunnel already open")
		default:
			_ = conn.Close()
			<-done
			return status.Error(codes.InvalidArgument, "empty tunnel frame")
		}
	}
}

func (s *Server) ReverseHTTP(stream pb.ContainerService_ReverseHTTPServer) error {
	if s.reverseHTTP == nil {
		return status.Error(codes.Unavailable, "reverse HTTP stream is not configured")
	}
	return s.reverseHTTP.ServeReverseHTTP(stream)
}

func (s *Server) execPTY(stream pb.ContainerService_ExecServer, firstMsg *pb.ExecInput) error {
	command := firstMsg.GetCommand()
	workDir := s.resolveExecWorkDir(firstMsg.GetWorkDir())

	timeout := int(firstMsg.GetTimeoutSeconds())
	if timeout <= 0 {
		timeout = defaultPTYTimeout
	}
	ctx, cancel := context.WithTimeout(stream.Context(), time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if isBarePath(command) {
		cmd = exec.CommandContext(ctx, command) //nolint:gosec // G204: intentional agent command execution.
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command) //nolint:gosec // G204: intentional agent command execution.
	}
	configurePTYCommandCancellation(cmd)
	cmd.Dir = workDir
	cmd.Env = execPTYEnv(firstMsg)

	initialSize := &pty.Winsize{Rows: 24, Cols: 80}
	if r := firstMsg.GetResize(); r != nil && r.GetCols() > 0 && r.GetRows() > 0 {
		initialSize.Rows = uint16(r.GetRows()) //nolint:gosec // G115
		initialSize.Cols = uint16(r.GetCols()) //nolint:gosec // G115
	}

	ptmx, err := pty.StartWithSize(cmd, initialSize)
	if err != nil {
		return status.Errorf(codes.Internal, "pty start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	go func() {
		for {
			msg, recvErr := stream.Recv()
			if recvErr != nil {
				return
			}
			if r := msg.GetResize(); r != nil && r.GetCols() > 0 && r.GetRows() > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{
					Rows: uint16(r.GetRows()), //nolint:gosec // G115
					Cols: uint16(r.GetCols()), //nolint:gosec // G115
				})
			}
			if data := msg.GetStdinData(); len(data) > 0 {
				_, _ = ptmx.Write(data)
			}
		}
	}()

	streamPipe(stream, ptmx, pb.ExecOutput_STDOUT)

	exitCode := resolveExitCode(cmd.Wait())

	return stream.Send(&pb.ExecOutput{
		Stream:   pb.ExecOutput_EXIT,
		ExitCode: exitCode,
	})
}

func (s *Server) execPipe(stream pb.ContainerService_ExecServer, firstMsg *pb.ExecInput) error {
	command := firstMsg.GetCommand()
	workDir := s.resolveExecWorkDir(firstMsg.GetWorkDir())

	timeout := int(firstMsg.GetTimeoutSeconds())
	noTimeout := timeout < 0
	var procCtx context.Context
	var procCancel context.CancelFunc
	if noTimeout {
		procCtx, procCancel = context.WithCancel(context.Background())
	} else {
		if timeout == 0 {
			timeout = defaultTimeout
		}
		procCtx, procCancel = context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	}
	defer procCancel()

	cmd := exec.CommandContext(procCtx, "/bin/sh", "-c", command) //nolint:gosec // G204: intentional agent command execution.
	configureCommandCancellation(cmd)
	cmd.Dir = workDir
	cmd.Env = execEnv(firstMsg)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return status.Errorf(codes.Internal, "stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return status.Errorf(codes.Internal, "stdout pipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return status.Errorf(codes.Internal, "stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return status.Errorf(codes.Internal, "start: %v", err)
	}
	if data := firstMsg.GetStdinData(); len(data) > 0 {
		_, _ = stdinPipe.Write(data)
	}

	go func() {
		select {
		case <-procCtx.Done():
		case <-stream.Context().Done():
			if noTimeout {
				procCancel()
			}
		}
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
	}()

	go func() {
		for {
			msg, recvErr := stream.Recv()
			if recvErr != nil {
				_ = stdinPipe.Close()
				return
			}
			if data := msg.GetStdinData(); len(data) > 0 {
				_, _ = stdinPipe.Write(data)
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		streamPipe(stream, stdoutPipe, pb.ExecOutput_STDOUT)
	}()
	streamPipe(stream, stderrPipe, pb.ExecOutput_STDERR)
	<-done

	exitCode := resolveExitCode(cmd.Wait())

	_ = stream.Send(&pb.ExecOutput{
		Stream:   pb.ExecOutput_EXIT,
		ExitCode: exitCode,
	})
	return nil
}

func execEnv(input *pb.ExecInput) []string {
	if input == nil {
		return nil
	}
	env := input.GetEnv()
	unset := input.GetUnsetEnv()
	clean := input.GetCleanEnv()
	if !clean && len(env) == 0 && len(unset) == 0 {
		return nil
	}
	out := []string{}
	if !clean {
		out = append(out, os.Environ()...)
	}
	if len(unset) > 0 {
		out = filterUnsetEnv(out, unset)
	}
	out = append(out, env...)
	return out
}

func execPTYEnv(input *pb.ExecInput) []string {
	env := execEnv(input)
	if env == nil {
		env = os.Environ()
	}
	return append(env, "TERM=xterm-256color")
}

func filterUnsetEnv(env []string, unset []string) []string {
	if len(env) == 0 || len(unset) == 0 {
		return env
	}
	// Keep wildcard semantics aligned with agent/runtime/acp/client.envNameBlocked. This side
	// filters inherited bridge process env before launching the workspace command.
	blocked := make(map[string]struct{}, len(unset))
	prefixes := make([]string, 0)
	for _, key := range unset {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if strings.HasSuffix(key, "*") {
			prefix := strings.TrimSuffix(key, "*")
			if prefix != "" {
				prefixes = append(prefixes, prefix)
			}
			continue
		}
		blocked[key] = struct{}{}
	}
	filtered := env[:0]
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		if _, skip := blocked[key]; skip {
			continue
		}
		skip := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// resolveExitCode normalises the result of cmd.Wait() into a single int32
// exit code that callers can rely on:
//
//   - nil               → 0 (clean exit)
//   - *exec.ExitError   → real exit code, or 128+signal for signal-killed
//     processes (the conventional shell encoding), so callers can tell
//     "killed by SIGKILL" (137) from "actually returned -1".
//   - any other error   → -1 (I/O failure during Wait; we genuinely don't know)
//
// Without this, signal-killed processes (including ones killed by our own
// procCtx timeout via SIGKILL, which cannot be wrapped by /bin/sh -c) were
// reported as -1, which looked like a generic failure even when the command
// had already printed all of its expected output.
func resolveExitCode(waitErr error) int32 {
	if waitErr == nil {
		return 0
	}
	exitErr := &exec.ExitError{}
	if !errors.As(waitErr, &exitErr) {
		return -1
	}
	if state := exitErr.ProcessState; state != nil {
		if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			sig := int(ws.Signal())
			if sig > 0 {
				return int32(128 + sig) //nolint:gosec // G115: signal numbers are small positive ints.
			}
		}
	}
	ec := exitErr.ExitCode()
	if ec < math.MinInt32 {
		return math.MinInt32
	}
	if ec > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(ec)
}

func (s *Server) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	path := req.GetPath()
	if path == "" {
		return status.Error(codes.InvalidArgument, "path is required")
	}
	path = s.resolvePath(path)

	f, err := os.Open(path) //nolint:gosec // G304: workspace bridge intentionally serves agent-selected paths.
	if err != nil {
		return status.Errorf(codes.NotFound, "open: %v", err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, rawChunkSize)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&pb.DataChunk{Data: buf[:n]}); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "read: %v", err)
		}
	}
	return nil
}

func (s *Server) WriteRaw(stream pb.ContainerService_WriteRawServer) error {
	var (
		f          *os.File
		targetPath string
		tmpPath    string
		written    int64
		committed  bool
	)
	defer func() {
		if f != nil {
			_ = f.Close()
		}
		if !committed && tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if chunk.GetAbort() {
			// The sender's source failed; the deferred cleanup removes the
			// temporary file before this status reaches the sender.
			return status.Error(codes.Aborted, "write aborted by sender")
		}

		if f == nil {
			path := chunk.GetPath()
			if path == "" {
				return status.Error(codes.InvalidArgument, "first chunk must include path")
			}
			targetPath = s.resolvePath(path)
			dir := filepath.Dir(targetPath)
			if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
				return status.Errorf(codes.Internal, "mkdir: %v", mkErr)
			}
			f, err = os.CreateTemp(dir, "."+filepath.Base(targetPath)+".tmp-*")
			if err != nil {
				return status.Errorf(codes.Internal, "create temp file: %v", err)
			}
			tmpPath = f.Name()
		}

		if len(chunk.GetData()) > 0 {
			n, err := f.Write(chunk.GetData())
			written += int64(n)
			if err != nil {
				return status.Errorf(codes.Internal, "write: %v", err)
			}
		}
	}

	if f == nil {
		return status.Error(codes.InvalidArgument, "first chunk must include path")
	}
	if err := f.Close(); err != nil {
		f = nil
		return status.Errorf(codes.Internal, "close: %v", err)
	}
	f = nil
	if err := contextStatusError(stream.Context()); err != nil {
		return err
	}
	// #nosec G703 -- targetPath is resolved by the workspace bridge, and tmpPath is created in the same directory.
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return status.Errorf(codes.Internal, "commit: %v", err)
	}
	committed = true
	return stream.SendAndClose(&pb.WriteRawResponse{BytesWritten: written})
}

func (s *Server) DeleteFile(_ context.Context, req *pb.DeleteFileRequest) (*pb.DeleteFileResponse, error) {
	path := req.GetPath()
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	path = s.resolvePath(path)

	var err error
	if req.GetRecursive() {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "delete: %v", err)
	}
	return &pb.DeleteFileResponse{}, nil
}

func (s *Server) Stat(_ context.Context, req *pb.StatRequest) (*pb.StatResponse, error) {
	path := req.GetPath()
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	path = s.resolvePath(path)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Error(codes.NotFound, "not found")
		}
		return nil, status.Errorf(codes.Internal, "stat: %v", err)
	}
	return &pb.StatResponse{
		Entry: &pb.FileEntry{
			Path:    filepath.Base(path),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format(time.RFC3339),
		},
	}, nil
}

func (s *Server) Mkdir(_ context.Context, req *pb.MkdirRequest) (*pb.MkdirResponse, error) {
	path := req.GetPath()
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	path = s.resolvePath(path)

	if err := os.MkdirAll(path, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir: %v", err)
	}
	return &pb.MkdirResponse{}, nil
}

func (s *Server) Rename(_ context.Context, req *pb.RenameRequest) (*pb.RenameResponse, error) {
	oldPath := req.GetOldPath()
	newPath := req.GetNewPath()
	if oldPath == "" || newPath == "" {
		return nil, status.Error(codes.InvalidArgument, "old_path and new_path are required")
	}
	oldPath = s.resolvePath(oldPath)
	newPath = s.resolvePath(newPath)

	if err := os.MkdirAll(filepath.Dir(newPath), 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir parent: %v", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, status.Errorf(codes.NotFound, "rename: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "rename: %v", err)
	}
	return &pb.RenameResponse{}, nil
}

func (s *Server) resolveExecWorkDir(path string) string {
	if strings.TrimSpace(path) == "" {
		return s.defaultWorkDir
	}
	return s.resolvePath(path)
}

func (s *Server) resolvePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return s.defaultWorkDir
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		if s.workspaceRoot != "." && (clean == s.dataMount || strings.HasPrefix(clean, s.dataMount+string(filepath.Separator))) {
			rel := strings.TrimPrefix(clean, s.dataMount)
			return filepath.Join(s.workspaceRoot, strings.TrimPrefix(rel, string(filepath.Separator)))
		}
		if s.allowHostAbsolute || s.workspaceRoot == "." || clean == s.defaultWorkDir || strings.HasPrefix(clean, s.defaultWorkDir+string(filepath.Separator)) {
			return clean
		}
		return filepath.Join(s.defaultWorkDir, strings.TrimPrefix(clean, string(filepath.Separator)))
	}
	return filepath.Join(s.defaultWorkDir, clean)
}

func collapseHeavySubdirs(entries []*pb.FileEntry, threshold int) []*pb.FileEntry {
	counts := make(map[string]int)
	for _, e := range entries {
		top := listTopDir(e.GetPath())
		if top != "" {
			counts[top]++
		}
	}

	heavy := make(map[string]bool)
	for dir, n := range counts {
		if n > threshold {
			heavy[dir] = true
		}
	}
	if len(heavy) == 0 {
		return entries
	}

	seen := make(map[string]bool)
	out := make([]*pb.FileEntry, 0, len(entries))
	for _, e := range entries {
		path := e.GetPath()
		top := listTopDir(path)

		if !heavy[top] {
			out = append(out, e)
			continue
		}
		if path == top && e.GetIsDir() {
			out = append(out, e)
			continue
		}
		if seen[top] {
			continue
		}
		seen[top] = true
		out = append(out, &pb.FileEntry{
			Path:    top + "/",
			IsDir:   true,
			Summary: fmt.Sprintf("%d items (not expanded)", counts[top]),
		})
	}
	return out
}

func listTopDir(path string) string {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

func isBarePath(cmd string) bool {
	if cmd == "" {
		return false
	}
	for _, c := range cmd {
		if c == ' ' || c == '\t' || c == '|' || c == '&' || c == ';' || c == '>' || c == '<' || c == '$' || c == '(' || c == ')' || c == '`' {
			return false
		}
	}
	return strings.HasPrefix(cmd, "/") || !strings.Contains(cmd, "/")
}

func validateTunnelAddress(ctx context.Context, address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", status.Error(codes.InvalidArgument, "tunnel address is required")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "invalid tunnel address: %v", err)
	}
	if _, err := net.DefaultResolver.LookupPort(ctx, "tcp", port); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "invalid tunnel port: %v", err)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", status.Errorf(codes.InvalidArgument, "resolve tunnel host: %v", err)
	}
	if len(ips) == 0 {
		return "", status.Error(codes.InvalidArgument, "tunnel host did not resolve")
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return "", status.Error(codes.PermissionDenied, "tunnel target must resolve to loopback")
		}
	}
	return net.JoinHostPort(host, port), nil
}

func streamPipe(stream pb.ContainerService_ExecServer, r io.Reader, st pb.ExecOutput_Stream) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_ = stream.Send(&pb.ExecOutput{
				Stream: st,
				Data:   buf[:n],
			})
		}
		if err != nil {
			break
		}
	}
}

func buildFileEntry(name string, d fs.DirEntry) (*pb.FileEntry, error) {
	info, err := d.Info()
	if err != nil {
		return nil, err
	}
	return &pb.FileEntry{
		Path:    name,
		IsDir:   d.IsDir(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().Format(time.RFC3339),
	}, nil
}

func truncateRunes(s string, maxRunes int) string {
	pos := 0
	count := 0
	for pos < len(s) && count < maxRunes {
		_, size := utf8.DecodeRuneInString(s[pos:])
		pos += size
		count++
	}
	return s[:pos]
}
