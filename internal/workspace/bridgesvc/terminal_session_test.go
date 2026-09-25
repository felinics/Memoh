//go:build !windows

package bridgesvc

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestByteRingDropsOnRuneBoundary(t *testing.T) {
	var ring byteRing
	rune4 := []byte{0xf0, 0x9f, 0x92, 0xa9}
	payload := bytes.Repeat(append([]byte("a"), rune4...), 40)
	ring.append(payload, 11)
	if ring.start == 0 {
		t.Fatal("expected the ring to drop bytes")
	}
	if !utf8.Valid(ring.buf) {
		t.Fatalf("ring is not valid UTF-8: %x", ring.buf)
	}
	for i := 0; i < len(ring.buf); {
		r, size := utf8.DecodeRune(ring.buf[i:])
		if r == utf8.RuneError && size == 1 {
			t.Fatalf("invalid rune at %d: %x", i, ring.buf)
		}
		i += size
	}
}

func TestTerminalOpenEchoesStdinAndAssignsSessionID(t *testing.T) {
	srv, pipe := startTerminal(t, &pb.TerminalOpen{
		Command: "printf 'READY\\n'; while IFS= read -r line; do printf 'got:%s\\n' \"$line\"; done",
		WorkDir: t.TempDir(),
		Cols:    80,
		Rows:    24,
	})
	ready := pipe.waitReady(t)
	if ready.GetSessionId() == "" {
		t.Fatal("session id is empty")
	}
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Input{Input: []byte("hi\n")}})
	out := pipe.waitOutput(t, "got:hi", 3*time.Second)
	if bytes.Contains(out, []byte("got:hi")) == false {
		t.Fatalf("output = %q", out)
	}
	releaseTerminal(t, srv, ready.GetSessionId())
	_ = srv
}

func TestTerminalDetachKeepsProcessAndReplaysGap(t *testing.T) {
	srv, pipe := startTerminal(t, &pb.TerminalOpen{
		Command: "printf 'PID:%s\\n' $$; i=0; while true; do i=$((i+1)); printf 'n=%s\\n' \"$i\"; sleep 0.05; done",
		WorkDir: t.TempDir(),
	})
	ready := pipe.waitReady(t)
	pid := pipe.waitPID(t)
	pipe.waitOutput(t, "n=", 3*time.Second)
	since := pipe.offset()
	pipe.cancel()
	waitTerminalDone(t, pipe, 2*time.Second)
	if !processAlive(pid) {
		t.Fatal("process exited on detach")
	}

	again := newTerminalPipe()
	done := runTerminal(srv, again)
	again.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{
		SessionId:   ready.GetSessionId(),
		SinceOffset: since,
		Cols:        80,
		Rows:        24,
	}}})
	second := again.waitReady(t)
	if second.GetSessionId() != ready.GetSessionId() {
		t.Fatalf("session id = %q, want %q", second.GetSessionId(), ready.GetSessionId())
	}
	again.waitOutput(t, "n=", 3*time.Second)
	if !processAlive(pid) {
		t.Fatal("process exited after reattach")
	}
	releaseTerminal(t, srv, ready.GetSessionId())
	_ = waitDoneErr(t, done, time.Second)
}

func TestTerminalReleaseKillsProcessWithoutGrace(t *testing.T) {
	srv := newTestTerminalServer(t)
	srv.terminals.grace = time.Minute
	pipe := newTerminalPipe()
	done := runTerminal(srv, pipe)
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: &pb.TerminalOpen{
		Command: "printf 'PID:%s\\n' $$; exec sleep 30",
		WorkDir: t.TempDir(),
	}}})
	ready := pipe.waitReady(t)
	pid := pipe.waitPID(t)
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Release{Release: true}})
	_ = waitDoneErr(t, done, 3*time.Second)
	waitDead(t, pid)
	missing := newTerminalPipe()
	missingDone := runTerminal(srv, missing)
	missing.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{SessionId: ready.GetSessionId()}}})
	err := waitDoneErr(t, missingDone, 2*time.Second)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("attach after release = %v, want NotFound", err)
	}
}

func TestTerminalDetachGraceKillsProcess(t *testing.T) {
	srv := newTestTerminalServer(t)
	srv.terminals.grace = 150 * time.Millisecond
	pipe := newTerminalPipe()
	done := runTerminal(srv, pipe)
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: &pb.TerminalOpen{
		Command: "printf 'PID:%s\\n' $$; exec sleep 30",
		WorkDir: t.TempDir(),
	}}})
	pipe.waitReady(t)
	pid := pipe.waitPID(t)
	pipe.cancel()
	_ = waitDoneErr(t, done, 2*time.Second)
	if !processAlive(pid) {
		t.Fatal("process exited before the detach grace")
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("process still alive after the detach grace")
	}
}

func TestTerminalAttachedSessionSurvivesGrace(t *testing.T) {
	srv := newTestTerminalServer(t)
	srv.terminals.grace = 100 * time.Millisecond
	pipe := newTerminalPipe()
	_ = runTerminal(srv, pipe)
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: &pb.TerminalOpen{
		Command: "printf 'PID:%s\\n' $$; exec sleep 30",
		WorkDir: t.TempDir(),
	}}})
	ready := pipe.waitReady(t)
	pid := pipe.waitPID(t)
	time.Sleep(350 * time.Millisecond)
	if !processAlive(pid) {
		t.Fatal("attached process was killed by the detach grace")
	}
	releaseTerminal(t, srv, ready.GetSessionId())
}

func TestTerminalSecondAttachSupersedesFirst(t *testing.T) {
	srv, pipe := startTerminal(t, &pb.TerminalOpen{
		Command: "printf 'PID:%s\\n' $$; while true; do printf 'tick\\n'; sleep 0.05; done",
		WorkDir: t.TempDir(),
	})
	ready := pipe.waitReady(t)
	pid := pipe.waitPID(t)
	second := newTerminalPipe()
	secondDone := runTerminal(srv, second)
	second.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{
		SessionId: ready.GetSessionId(),
		Cols:      40,
		Rows:      12,
	}}})
	second.waitReady(t)
	err := waitDoneErr(t, pipe.done, 2*time.Second)
	if status.Code(err) != codes.Aborted || status.Convert(err).Message() != terminalSuperseded {
		t.Fatalf("first attach = %v, want superseded", err)
	}
	second.waitOutput(t, "tick", 2*time.Second)
	if !processAlive(pid) {
		t.Fatal("process exited when a new attach replaced the old one")
	}
	releaseTerminal(t, srv, ready.GetSessionId())
	_ = waitDoneErr(t, secondDone, time.Second)
}

func TestTerminalSubscriberOverflowKeepsProcess(t *testing.T) {
	srv := newTestTerminalServer(t)
	srv.terminals.ringBytes = 8
	pipe := newTerminalPipe()
	pipe.blockBytes = true
	done := runTerminal(srv, pipe)
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: &pb.TerminalOpen{
		Command: "i=0; while [ \"$i\" -lt 5000 ]; do printf x; i=$((i+1)); done; printf '\\nPID:%s\\n' $$; exec sleep 30",
		WorkDir: t.TempDir(),
	}}})
	pipe.waitReady(t)
	time.Sleep(200 * time.Millisecond)
	close(pipe.byteGate)
	err := waitDoneErr(t, done, 3*time.Second)
	if status.Code(err) != codes.Aborted || status.Convert(err).Message() != terminalOverflow {
		t.Fatalf("overflow = %v", err)
	}
	// The shell is still the session. Reattach and read the pid, then release.
	again := newTerminalPipe()
	againDone := runTerminal(srv, again)
	// Session id was on the first ready.
	id := pipe.readyID()
	again.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{SessionId: id}}})
	view := again.waitReady(t)
	if !view.GetTruncated() {
		t.Fatal("reattach after overflow was not truncated")
	}
	sess, err := srv.terminals.get(id)
	if err != nil {
		t.Fatalf("session after overflow: %v", err)
	}
	if !processAlive(sess.pid) {
		t.Fatal("process exited when the subscriber fell behind")
	}
	releaseTerminal(t, srv, id)
	_ = waitDoneErr(t, againDone, time.Second)
}

func TestTerminalUnknownSession(t *testing.T) {
	srv := newTestTerminalServer(t)
	pipe := newTerminalPipe()
	done := runTerminal(srv, pipe)
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{SessionId: "missing"}}})
	err := waitDoneErr(t, done, time.Second)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("attach = %v, want NotFound", err)
	}
}

func TestTerminalExitIsReportedOnce(t *testing.T) {
	srv, pipe := startTerminal(t, &pb.TerminalOpen{
		Command: "exit 3",
		WorkDir: t.TempDir(),
	})
	ready := pipe.waitReady(t)
	code := pipe.waitExit(t)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	err := waitDoneErr(t, pipe.done, 2*time.Second)
	if err != nil {
		t.Fatalf("terminal returned %v after exit", err)
	}
	again := newTerminalPipe()
	againDone := runTerminal(srv, again)
	again.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{SessionId: ready.GetSessionId()}}})
	err = waitDoneErr(t, againDone, time.Second)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("attach after exit = %v, want NotFound", err)
	}
}

func TestTerminalSessionLimit(t *testing.T) {
	srv := newTestTerminalServer(t)
	srv.terminals.maxSessions = 1
	first := newTerminalPipe()
	firstDone := runTerminal(srv, first)
	first.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: &pb.TerminalOpen{
		Command: "printf 'PID:%s\\n' $$; exec sleep 30",
		WorkDir: t.TempDir(),
	}}})
	ready := first.waitReady(t)
	t.Cleanup(func() { releaseTerminal(t, srv, ready.GetSessionId()) })
	second := newTerminalPipe()
	secondDone := runTerminal(srv, second)
	second.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: &pb.TerminalOpen{
		Command: "sleep 30",
		WorkDir: t.TempDir(),
	}}})
	err := waitDoneErr(t, secondDone, time.Second)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second open = %v, want ResourceExhausted", err)
	}
	_ = firstDone
}

type terminalPipe struct {
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	cond       *sync.Cond
	in         []*pb.TerminalClient
	out        []*pb.TerminalServer
	done       chan error
	blockBytes bool
	byteGate   chan struct{}
	bytesOnce  sync.Once
}

func newTerminalPipe() *terminalPipe {
	ctx, cancel := context.WithCancel(context.Background())
	p := &terminalPipe{
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan error, 1),
		byteGate: make(chan struct{}),
	}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func newTestTerminalServer(t *testing.T) *Server {
	t.Helper()
	srv := New(Options{DefaultWorkDir: t.TempDir(), AllowHostAbsolute: true})
	t.Cleanup(func() {
		srv.terminals.mu.Lock()
		ids := make([]string, 0, len(srv.terminals.sessions))
		for id := range srv.terminals.sessions {
			ids = append(ids, id)
		}
		srv.terminals.mu.Unlock()
		for _, id := range ids {
			if sess, err := srv.terminals.get(id); err == nil {
				sess.release()
			}
		}
	})
	return srv
}

func startTerminal(t *testing.T, open *pb.TerminalOpen) (*Server, *terminalPipe) {
	t.Helper()
	srv := newTestTerminalServer(t)
	pipe := newTerminalPipe()
	pipe.done = runTerminal(srv, pipe)
	pipe.push(&pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: open}})
	return srv, pipe
}

func runTerminal(srv *Server, pipe *terminalPipe) chan error {
	done := make(chan error, 1)
	go func() {
		done <- srv.Terminal(pipe)
	}()
	pipe.done = done
	return done
}

func (p *terminalPipe) push(msg *pb.TerminalClient) {
	p.mu.Lock()
	p.in = append(p.in, msg)
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *terminalPipe) Send(msg *pb.TerminalServer) error {
	if p.blockBytes && msg.GetBytes() != nil {
		p.bytesOnce.Do(func() {})
		select {
		case <-p.byteGate:
		case <-p.ctx.Done():
			return p.ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ctx.Err(); err != nil {
		return err
	}
	clone := proto.Clone(msg).(*pb.TerminalServer)
	if frame := msg.GetBytes(); frame != nil {
		clone = &pb.TerminalServer{Frame: &pb.TerminalServer_Bytes{Bytes: &pb.TerminalBytes{
			Data:   append([]byte(nil), frame.GetData()...),
			Offset: frame.GetOffset(),
		}}}
	}
	p.out = append(p.out, clone)
	p.cond.Broadcast()
	return nil
}

func (p *terminalPipe) Recv() (*pb.TerminalClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.in) == 0 && p.ctx.Err() == nil {
		p.cond.Wait()
	}
	if len(p.in) == 0 {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	msg := p.in[0]
	p.in = p.in[1:]
	return msg, nil
}

func (p *terminalPipe) Context() context.Context  { return p.ctx }
func (*terminalPipe) SetHeader(metadata.MD) error { return nil }
func (*terminalPipe) SendHeader(metadata.MD) error {
	return nil
}
func (*terminalPipe) SetTrailer(metadata.MD) {}
func (*terminalPipe) SendMsg(any) error      { return nil }
func (*terminalPipe) RecvMsg(any) error      { return nil }

func (p *terminalPipe) waitReady(t *testing.T) *pb.TerminalReady {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, msg := range p.out {
			if ready := msg.GetReady(); ready != nil {
				p.mu.Unlock()
				return ready
			}
		}
		p.cond.Wait()
		p.mu.Unlock()
	}
	t.Fatal("timed out waiting for terminal ready")
	return nil
}

func (p *terminalPipe) readyID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, msg := range p.out {
		if ready := msg.GetReady(); ready != nil {
			return ready.GetSessionId()
		}
	}
	return ""
}

func (p *terminalPipe) offset() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var off uint64
	for _, msg := range p.out {
		if frame := msg.GetBytes(); frame != nil {
			off = frame.GetOffset()
		}
	}
	if off == 0 {
		for _, msg := range p.out {
			if ready := msg.GetReady(); ready != nil {
				off = ready.GetOffset()
			}
		}
	}
	return off
}

func (p *terminalPipe) output() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	var buf bytes.Buffer
	for _, msg := range p.out {
		if frame := msg.GetBytes(); frame != nil {
			buf.Write(frame.GetData())
		}
	}
	return buf.Bytes()
}

func (p *terminalPipe) waitOutput(t *testing.T, needle string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := p.output()
		if bytes.Contains(out, []byte(needle)) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in %q", needle, p.output())
	return nil
}

func (p *terminalPipe) waitPID(t *testing.T) int {
	t.Helper()
	out := p.waitOutput(t, "PID:", 3*time.Second)
	match := regexp.MustCompile(`PID:(\d+)`).FindSubmatch(bytes.ReplaceAll(out, []byte("\r"), nil))
	if match == nil {
		t.Fatalf("pid not found in %q", out)
	}
	pid, err := strconv.Atoi(string(match[1]))
	if err != nil || pid <= 0 {
		t.Fatalf("pid %q: %v", match[1], err)
	}
	return pid
}

func (p *terminalPipe) waitExit(t *testing.T) int32 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, msg := range p.out {
			if exit := msg.GetExit(); exit != nil {
				code := exit.GetCode()
				p.mu.Unlock()
				return code
			}
		}
		p.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for terminal exit")
	return -1
}

func waitDoneErr(t *testing.T, done chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatal("timed out waiting for terminal call")
		return nil
	}
}

func waitTerminalDone(t *testing.T, pipe *terminalPipe, timeout time.Duration) {
	t.Helper()
	_ = waitDoneErr(t, pipe.done, timeout)
}

func releaseTerminal(t *testing.T, srv *Server, id string) {
	t.Helper()
	sess, err := srv.terminals.get(id)
	if err != nil {
		return
	}
	sess.release()
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d still alive", pid)
}
