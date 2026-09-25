package bridgesvc

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	terminalRingBytes   = 1 << 20
	terminalMaxSessions = 32
	terminalDetachGrace = 15 * time.Minute
	terminalChunkBytes  = 32 * 1024
	terminalDefaultCols = 80
	terminalDefaultRows = 24
	terminalSuperseded  = "terminal attach superseded"
	terminalOverflow    = "terminal subscriber fell behind"
)

type terminalHub struct {
	mu          sync.Mutex
	sessions    map[string]*terminalSession
	grace       time.Duration
	maxSessions int
	ringBytes   int
}

func newTerminalHub() *terminalHub {
	return &terminalHub{
		sessions:    map[string]*terminalSession{},
		grace:       terminalDetachGrace,
		maxSessions: terminalMaxSessions,
		ringBytes:   terminalRingBytes,
	}
}

type byteRing struct {
	buf   []byte
	start uint64
}

func (r *byteRing) end() uint64 {
	return r.start + uint64(len(r.buf))
}

func (r *byteRing) append(p []byte, limit int) {
	if len(p) == 0 {
		return
	}
	if limit <= 0 {
		limit = terminalRingBytes
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) <= limit {
		return
	}
	drop := len(r.buf) - limit
	r.buf = r.buf[drop:]
	// drop is a non-negative length below the ring cap, so it fits in uint64.
	r.start += uint64(drop) //nolint:gosec // G115: drop is a bounded slice length.
	skip := 0
	for skip < len(r.buf) && !utf8.RuneStart(r.buf[skip]) {
		skip++
	}
	if skip > 0 {
		r.buf = r.buf[skip:]
		r.start += uint64(skip)
	}
}

// sliceFrom returns the bytes at and after absolute offset off.
// truncated is set when off is older than the bytes still kept.
func (r *byteRing) sliceFrom(off uint64) (data []byte, from uint64, truncated bool) {
	end := r.end()
	if off < r.start {
		return append([]byte(nil), r.buf...), r.start, true
	}
	if off >= end {
		return nil, end, false
	}
	// off-start is an index into buf, so it fits in int.
	skip := int(off - r.start) //nolint:gosec // G115: difference is at most len(buf).
	return append([]byte(nil), r.buf[skip:]...), off, false
}

type terminalSession struct {
	hub        *terminalHub
	id         string
	ptmx       *os.File
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	pid        int
	mu         sync.Mutex
	cond       *sync.Cond
	ring       byteRing
	generation uint64
	attached   bool
	released   bool
	exited     bool
	exitCode   int32
	graceTimer *time.Timer
}

type attachView struct {
	from      uint64
	data      []byte
	truncated bool
	exited    bool
	exitCode  int32
	next      uint64
}

func (h *terminalHub) open(host *Server, req *pb.TerminalOpen) (*terminalSession, error) {
	if req == nil || req.GetCommand() == "" {
		return nil, status.Error(codes.InvalidArgument, "command is required")
	}
	h.mu.Lock()
	if len(h.sessions) >= h.maxSessions {
		h.mu.Unlock()
		return nil, status.Error(codes.ResourceExhausted, "terminal session limit reached")
	}
	h.mu.Unlock()

	sess, err := host.startTerminal(h, req)
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) >= h.maxSessions {
		go sess.kill()
		return nil, status.Error(codes.ResourceExhausted, "terminal session limit reached")
	}
	h.sessions[sess.id] = sess
	return sess, nil
}

func (h *terminalHub) get(id string) (*terminalSession, error) {
	h.mu.Lock()
	sess := h.sessions[id]
	h.mu.Unlock()
	if sess == nil {
		return nil, status.Error(codes.NotFound, "terminal session not found")
	}
	sess.mu.Lock()
	released := sess.released
	sess.mu.Unlock()
	if released {
		return nil, status.Error(codes.NotFound, "terminal session not found")
	}
	return sess, nil
}

func (h *terminalHub) delete(id string) {
	h.mu.Lock()
	delete(h.sessions, id)
	h.mu.Unlock()
}

func (h *terminalHub) expire(id string, gen uint64) {
	h.mu.Lock()
	sess := h.sessions[id]
	h.mu.Unlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	if sess.generation != gen || sess.attached || sess.released {
		sess.mu.Unlock()
		return
	}
	sess.released = true
	sess.attached = false
	sess.mu.Unlock()
	sess.kill()
	h.delete(id)
}

func (s *Server) startTerminal(h *terminalHub, req *pb.TerminalOpen) (*terminalSession, error) {
	id, err := newTerminalID()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "terminal session id: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := req.GetCommand()
	var cmd *exec.Cmd
	if isBarePath(command) {
		cmd = exec.CommandContext(ctx, command) //nolint:gosec // G204: the browser terminal launches the workspace shell.
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command) //nolint:gosec // G204: the browser terminal launches the workspace shell.
	}
	configurePTYCommandCancellation(cmd)
	cmd.Dir = s.resolveExecWorkDir(req.GetWorkDir())
	cmd.Env = execPTYEnv(&pb.ExecInput{})

	ptmx, err := pty.StartWithSize(cmd, terminalWinsize(req.GetCols(), req.GetRows()))
	if err != nil {
		cancel()
		return nil, status.Errorf(codes.Internal, "pty start: %v", err)
	}
	sess := &terminalSession{
		hub:    h,
		id:     id,
		ptmx:   ptmx,
		cmd:    cmd,
		cancel: cancel,
		pid:    cmd.Process.Pid,
	}
	sess.cond = sync.NewCond(&sess.mu)
	go sess.readLoop()
	go sess.waitLoop(cmd)
	return sess, nil
}

func (s *terminalSession) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.appendOutput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *terminalSession) waitLoop(cmd *exec.Cmd) {
	code := resolveExitCode(cmd.Wait())
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *terminalSession) appendOutput(p []byte) {
	s.mu.Lock()
	s.ring.append(p, s.hub.ringBytes)
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *terminalSession) wake() {
	s.mu.Lock()
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *terminalSession) kill() {
	if s.cmd != nil && s.cmd.Cancel != nil {
		_ = s.cmd.Cancel()
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.ptmx != nil {
		_ = s.ptmx.Close()
	}
}

func (s *terminalSession) release() {
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return
	}
	s.released = true
	s.attached = false
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	s.kill()
	s.hub.delete(s.id)
}

func (s *terminalSession) beginAttach(since uint64, cols, rows uint32) (uint64, attachView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return 0, attachView{}, status.Error(codes.NotFound, "terminal session not found")
	}
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
	}
	s.generation++
	s.attached = true
	s.cond.Broadcast()
	s.resizeLocked(cols, rows)
	data, from, truncated := s.ring.sliceFrom(since)
	next := from + uint64(len(data))
	view := attachView{
		from:      from,
		data:      data,
		truncated: truncated,
		exited:    s.exited,
		exitCode:  s.exitCode,
		next:      next,
	}
	return s.generation, view, nil
}

func (s *terminalSession) endAttach(gen uint64) {
	s.mu.Lock()
	if s.generation != gen || s.released {
		s.mu.Unlock()
		return
	}
	s.attached = false
	if s.exited {
		s.released = true
		s.mu.Unlock()
		s.hub.delete(s.id)
		return
	}
	s.armGraceLocked()
	s.mu.Unlock()
}

func (s *terminalSession) armGraceLocked() {
	if s.graceTimer != nil {
		s.graceTimer.Stop()
	}
	gen := s.generation
	id := s.id
	grace := s.hub.grace
	s.graceTimer = time.AfterFunc(grace, func() {
		s.hub.expire(id, gen)
	})
}

func (s *terminalSession) resizeLocked(cols, rows uint32) {
	if s.ptmx == nil || cols == 0 || rows == 0 || cols > 10000 || rows > 10000 {
		return
	}
	_ = pty.Setsize(s.ptmx, &pty.Winsize{
		Cols: uint16(cols), //nolint:gosec // G115: cols is bounded by the check above.
		Rows: uint16(rows), //nolint:gosec // G115: rows is bounded by the check above.
	})
}

func (s *terminalSession) handleInput(msg *pb.TerminalClient, gen uint64) bool {
	if msg == nil {
		return false
	}
	switch frame := msg.GetFrame().(type) {
	case *pb.TerminalClient_Release:
		if frame.Release {
			s.mu.Lock()
			current := s.generation == gen && !s.released
			s.mu.Unlock()
			if current {
				s.release()
			}
			return true
		}
	case *pb.TerminalClient_Input:
		if len(frame.Input) == 0 {
			return false
		}
		s.mu.Lock()
		ok := s.generation == gen && !s.released && s.ptmx != nil
		file := s.ptmx
		s.mu.Unlock()
		if ok {
			_, _ = file.Write(frame.Input)
		}
	case *pb.TerminalClient_Resize:
		if frame.Resize == nil {
			return false
		}
		s.mu.Lock()
		ok := s.generation == gen && !s.released
		if ok {
			s.resizeLocked(frame.Resize.GetCols(), frame.Resize.GetRows())
		}
		s.mu.Unlock()
	default:
	}
	return false
}

func (s *Server) Terminal(stream pb.ContainerService_TerminalServer) error {
	if s.terminals == nil {
		s.terminals = newTerminalHub()
	}
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "failed to receive terminal open")
	}

	var (
		sess       *terminalSession
		since      uint64
		cols, rows uint32
	)
	switch frame := first.GetFrame().(type) {
	case *pb.TerminalClient_Open:
		if frame.Open == nil {
			return status.Error(codes.InvalidArgument, "terminal open is required")
		}
		sess, err = s.terminals.open(s, frame.Open)
		cols, rows = frame.Open.GetCols(), frame.Open.GetRows()
	case *pb.TerminalClient_Attach:
		if frame.Attach == nil {
			return status.Error(codes.InvalidArgument, "terminal attach is required")
		}
		sess, err = s.terminals.get(frame.Attach.GetSessionId())
		since = frame.Attach.GetSinceOffset()
		cols, rows = frame.Attach.GetCols(), frame.Attach.GetRows()
	default:
		return status.Error(codes.InvalidArgument, "first terminal frame must open or attach")
	}
	if err != nil {
		return err
	}

	gen, view, err := sess.beginAttach(since, cols, rows)
	if err != nil {
		return err
	}
	defer sess.endAttach(gen)

	if err := stream.Send(&pb.TerminalServer{Frame: &pb.TerminalServer_Ready{Ready: &pb.TerminalReady{
		SessionId: sess.id,
		Offset:    view.from,
		Truncated: view.truncated,
	}}}); err != nil {
		return err
	}
	if err := sendTerminalChunks(stream, view.data, view.from); err != nil {
		return err
	}
	if view.exited {
		return stream.Send(&pb.TerminalServer{Frame: &pb.TerminalServer_Exit{Exit: &pb.TerminalExit{Code: view.exitCode}}})
	}

	errCh := make(chan error, 2)
	go func() {
		defer sess.wake()
		for {
			msg, recvErr := stream.Recv()
			if recvErr != nil {
				errCh <- nil
				return
			}
			if sess.handleInput(msg, gen) {
				errCh <- nil
				return
			}
		}
	}()
	go func() {
		<-stream.Context().Done()
		sess.wake()
	}()
	go func() {
		errCh <- sess.pumpOutput(stream, gen, view.next)
	}()
	err = <-errCh
	if err == nil || status.Code(err) == codes.Canceled {
		return nil
	}
	return err
}

func (s *terminalSession) pumpOutput(stream pb.ContainerService_TerminalServer, gen uint64, sent uint64) error {
	for {
		if err := stream.Context().Err(); err != nil {
			return nil
		}
		s.mu.Lock()
		if s.released {
			s.mu.Unlock()
			return nil
		}
		if s.generation != gen {
			s.mu.Unlock()
			return status.Error(codes.Aborted, terminalSuperseded)
		}
		data, from, truncated := s.ring.sliceFrom(sent)
		exited := s.exited
		code := s.exitCode
		if !truncated && len(data) == 0 && !exited {
			s.cond.Wait()
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		if truncated {
			return status.Error(codes.Aborted, terminalOverflow)
		}
		if len(data) > 0 {
			if err := sendTerminalChunks(stream, data, from); err != nil {
				return err
			}
			sent = from + uint64(len(data))
		}
		if exited {
			return stream.Send(&pb.TerminalServer{Frame: &pb.TerminalServer_Exit{Exit: &pb.TerminalExit{Code: code}}})
		}
	}
}

func sendTerminalChunks(stream pb.ContainerService_TerminalServer, data []byte, from uint64) error {
	off := from
	for len(data) > 0 {
		n := len(data)
		if n > terminalChunkBytes {
			n = terminalChunkBytes
		}
		chunk := append([]byte(nil), data[:n]...)
		off += uint64(n)
		if err := stream.Send(&pb.TerminalServer{Frame: &pb.TerminalServer_Bytes{Bytes: &pb.TerminalBytes{
			Data:   chunk,
			Offset: off,
		}}}); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func terminalWinsize(cols, rows uint32) *pty.Winsize {
	if cols == 0 || cols > 10000 {
		cols = terminalDefaultCols
	}
	if rows == 0 || rows > 10000 {
		rows = terminalDefaultRows
	}
	return &pty.Winsize{
		Cols: uint16(cols), //nolint:gosec // G115: cols is bounded above.
		Rows: uint16(rows), //nolint:gosec // G115: rows is bounded above.
	}
}

func newTerminalID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
