package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	terminalCloseExit        = websocket.CloseNormalClosure
	terminalCloseTransport   = websocket.CloseGoingAway
	terminalCloseGone        = 4000
	terminalCloseUnsupported = 4001
	terminalCloseFull        = 4002
	terminalCloseTaken       = 4003
	terminalWorkDir          = "/data"
	terminalReattachDelay    = 200 * time.Millisecond
	terminalPingInterval     = 30 * time.Second
	terminalPongWait         = 10 * time.Second
)

type terminalRPC interface {
	Send(*pb.TerminalClient) error
	Recv() (*pb.TerminalServer, error)
	Close() error
}

type terminalDialer interface {
	Terminal(ctx context.Context) (terminalRPC, error)
}

type bridgeTerminalDialer struct {
	client *bridge.Client
}

func (d bridgeTerminalDialer) Terminal(ctx context.Context) (terminalRPC, error) {
	return d.client.Terminal(ctx)
}

type terminalInbound struct {
	stdin   []byte
	resize  bool
	cols    uint32
	rows    uint32
	open    bool
	attach  bool
	session string
	since   uint64
	release bool
}

type terminalRunResult struct {
	reason   string
	session  string
	offset   uint64
	exitCode int32
}

func (h *ContainerdHandler) serveTerminalSocket(ctx context.Context, botID string, conn *websocket.Conn, client terminalDialer, shell string) {
	logger := h.logger
	serveTerminalWebSocket(ctx, logger, botID, conn, client, shell, terminalWorkDir, terminalPingInterval, terminalPongWait)
}

func serveTerminalWebSocket(ctx context.Context, logger *slog.Logger, botID string, conn *websocket.Conn, client terminalDialer, shell, workDir string, pingInterval, pongWait time.Duration) {
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sock := newTerminalSocket(conn)
	conn.SetPongHandler(func(string) error {
		sock.markPong()
		return nil
	})
	go sock.ping(ctx, cancel, pingInterval, pongWait)

	inbound := make(chan terminalInbound, 32)
	go readTerminalInbound(ctx, conn, inbound)

	var (
		sessionID string
		offset    uint64
		mode      = "open"
		reason    = "closed"
		cols      uint32
		rows      uint32
	)
	defer func() {
		if sock.released && sessionID != "" {
			releaseTerminalSession(ctx, client, sessionID)
			reason = "release"
		}
		if logger != nil {
			logger.InfoContext(ctx, "terminal websocket closed",
				slog.String("bot_id", botID),
				slog.String("session_id", sessionID),
				slog.String("reason", reason))
		}
	}()

	first, ok := <-inbound
	if !ok || ctx.Err() != nil {
		reason = "client closed"
		return
	}
	switch {
	case first.open:
		cols, rows = first.cols, first.rows
	case first.attach && first.session != "":
		mode = "attach"
		sessionID = first.session
		offset = first.since
		cols, rows = first.cols, first.rows
	default:
		sock.writeClose(websocket.CloseProtocolError, "expected open or attach")
		reason = "bad open"
		return
	}

	for {
		if ctx.Err() != nil {
			reason = "transport closed"
			return
		}
		result := runTerminalStream(ctx, sock, client, inbound, shell, workDir, mode, sessionID, offset, cols, rows)
		if result.session != "" {
			sessionID = result.session
		}
		offset = result.offset
		mode = "attach"
		switch result.reason {
		case "exit":
			_ = sock.writeText(map[string]any{"type": "exit", "code": result.exitCode})
			sock.writeClose(terminalCloseExit, "exit")
			reason = "exit"
			return
		case "gone":
			_ = sock.writeText(map[string]any{"type": "gone"})
			sock.writeClose(terminalCloseGone, "gone")
			reason = "gone"
			return
		case "unsupported":
			_ = sock.writeText(map[string]any{"type": "unsupported"})
			sock.writeClose(terminalCloseUnsupported, "unsupported")
			reason = "unsupported"
			return
		case "full":
			_ = sock.writeText(map[string]any{"type": "full"})
			sock.writeClose(terminalCloseFull, "full")
			reason = "full"
			return
		case "taken":
			_ = sock.writeText(map[string]any{"type": "taken"})
			sock.writeClose(terminalCloseTaken, "taken")
			reason = "taken"
			return
		case "release":
			sock.released = true
			reason = "release"
			return
		case "client-closed":
			reason = "client closed"
			return
		case "retry":
			timer := time.NewTimer(terminalReattachDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				reason = "transport closed"
				return
			case <-timer.C:
			}
		default:
			reason = result.reason
			return
		}
	}
}

func runTerminalStream(ctx context.Context, sock *terminalSocket, client terminalDialer, inbound <-chan terminalInbound, shell, workDir, mode, sessionID string, offset uint64, cols, rows uint32) terminalRunResult {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Terminal(streamCtx)
	if err != nil {
		return terminalRunResult{reason: classifyDial(err), session: sessionID, offset: offset}
	}
	defer func() { _ = stream.Close() }()

	var frame *pb.TerminalClient
	if mode == "attach" && sessionID != "" {
		frame = &pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{
			SessionId:   sessionID,
			SinceOffset: offset,
			Cols:        cols,
			Rows:        rows,
		}}}
	} else {
		frame = &pb.TerminalClient{Frame: &pb.TerminalClient_Open{Open: &pb.TerminalOpen{
			Command: shell,
			WorkDir: workDir,
			Cols:    cols,
			Rows:    rows,
		}}}
	}
	if err := stream.Send(frame); err != nil {
		return terminalRunResult{reason: classifyStream(err), session: sessionID, offset: offset}
	}

	events := make(chan terminalEvent, 16)
	go func() {
		for {
			msg, recvErr := stream.Recv()
			ev := terminalEvent{msg: msg, err: recvErr}
			select {
			case events <- ev:
			case <-streamCtx.Done():
				return
			}
			if recvErr != nil {
				return
			}
		}
	}()

	told := false
	for {
		select {
		case <-ctx.Done():
			return terminalRunResult{reason: "client-closed", session: sessionID, offset: offset}
		case msg, ok := <-inbound:
			if !ok {
				return terminalRunResult{reason: "client-closed", session: sessionID, offset: offset}
			}
			if msg.release {
				_ = stream.Send(&pb.TerminalClient{Frame: &pb.TerminalClient_Release{Release: true}})
				// Give the bridge time to observe the release before this stream
				// is cancelled. Cancelling first would only detach the session.
				timer := time.NewTimer(time.Second)
				for {
					select {
					case ev := <-events:
						if ev.err != nil {
							timer.Stop()
							return terminalRunResult{reason: "release", session: sessionID, offset: offset}
						}
					case <-timer.C:
						return terminalRunResult{reason: "release", session: sessionID, offset: offset}
					case <-ctx.Done():
						timer.Stop()
						return terminalRunResult{reason: "release", session: sessionID, offset: offset}
					}
				}
			}
			if msg.resize && msg.cols > 0 && msg.rows > 0 {
				_ = stream.Send(&pb.TerminalClient{Frame: &pb.TerminalClient_Resize{Resize: &pb.TerminalResize{Cols: msg.cols, Rows: msg.rows}}})
			}
			if len(msg.stdin) > 0 {
				_ = stream.Send(&pb.TerminalClient{Frame: &pb.TerminalClient_Input{Input: msg.stdin}})
			}
		case ev := <-events:
			if ev.err != nil {
				return terminalRunResult{reason: classifyStream(ev.err), session: sessionID, offset: offset}
			}
			switch {
			case ev.msg.GetReady() != nil:
				ready := ev.msg.GetReady()
				if ready.GetSessionId() != "" {
					sessionID = ready.GetSessionId()
				}
				if ready.GetTruncated() || !told {
					if err := sock.writeText(map[string]any{
						"type":      "ready",
						"sessionId": sessionID,
						"offset":    ready.GetOffset(),
						"truncated": ready.GetTruncated(),
					}); err != nil {
						return terminalRunResult{reason: "client-closed", session: sessionID, offset: offset}
					}
					told = true
				}
				if ready.GetTruncated() {
					offset = ready.GetOffset()
				}
			case ev.msg.GetBytes() != nil:
				frame := ev.msg.GetBytes()
				data := frame.GetData()
				end := frame.GetOffset()
				start := end - uint64(len(data))
				if end > offset {
					if start < offset {
						data = data[offset-start:]
					}
					if err := sock.writeBinary(data); err != nil {
						return terminalRunResult{reason: "client-closed", session: sessionID, offset: offset}
					}
					offset = end
				}
			case ev.msg.GetExit() != nil:
				return terminalRunResult{
					reason:   "exit",
					session:  sessionID,
					offset:   offset,
					exitCode: ev.msg.GetExit().GetCode(),
				}
			}
		}
	}
}

type terminalEvent struct {
	msg *pb.TerminalServer
	err error
}

func classifyDial(err error) string {
	if err == nil {
		return "retry"
	}
	return classifyStream(err)
}

func classifyStream(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return "retry"
	}
	st, ok := status.FromError(err)
	if !ok {
		if errors.Is(err, context.Canceled) {
			return "client-closed"
		}
		return "retry"
	}
	switch st.Code() {
	case codes.NotFound:
		return "gone"
	case codes.Unimplemented:
		return "unsupported"
	case codes.ResourceExhausted:
		return "full"
	case codes.Canceled:
		return "client-closed"
	case codes.Aborted:
		if st.Message() == "terminal attach superseded" {
			return "taken"
		}
		return "retry"
	default:
		return "retry"
	}
}

func releaseTerminalSession(ctx context.Context, client terminalDialer, sessionID string) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	stream, err := client.Terminal(releaseCtx)
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()
	_ = stream.Send(&pb.TerminalClient{Frame: &pb.TerminalClient_Attach{Attach: &pb.TerminalAttach{SessionId: sessionID}}})
	_ = stream.Send(&pb.TerminalClient{Frame: &pb.TerminalClient_Release{Release: true}})
}

func readTerminalInbound(ctx context.Context, conn *websocket.Conn, out chan<- terminalInbound) {
	defer close(out)
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, ok := decodeTerminalInbound(kind, data)
		if !ok {
			continue
		}
		select {
		case out <- msg:
		case <-ctx.Done():
			return
		}
	}
}

func decodeTerminalInbound(kind int, data []byte) (terminalInbound, bool) {
	switch kind {
	case websocket.BinaryMessage:
		if len(data) == 0 {
			return terminalInbound{}, false
		}
		return terminalInbound{stdin: append([]byte(nil), data...)}, true
	case websocket.TextMessage:
		var payload struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Since     uint64 `json:"since"`
			Cols      uint32 `json:"cols"`
			Rows      uint32 `json:"rows"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return terminalInbound{}, false
		}
		switch payload.Type {
		case "open":
			return terminalInbound{open: true, cols: payload.Cols, rows: payload.Rows}, true
		case "attach":
			return terminalInbound{attach: true, session: payload.SessionID, since: payload.Since, cols: payload.Cols, rows: payload.Rows}, true
		case "resize":
			return terminalInbound{resize: true, cols: payload.Cols, rows: payload.Rows}, true
		case "release":
			return terminalInbound{release: true}, true
		default:
			return terminalInbound{}, false
		}
	default:
		return terminalInbound{}, false
	}
}

type terminalSocket struct {
	conn     *websocket.Conn
	writeMu  sync.Mutex
	pong     chan struct{}
	released bool
}

func newTerminalSocket(conn *websocket.Conn) *terminalSocket {
	return &terminalSocket{conn: conn, pong: make(chan struct{}, 1)}
}

func (s *terminalSocket) markPong() {
	select {
	case s.pong <- struct{}{}:
	default:
	}
}

func (s *terminalSocket) ping(ctx context.Context, cancel context.CancelFunc, interval, pongWait time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.writeMu.Lock()
			err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(pongWait))
			s.writeMu.Unlock()
			if err != nil {
				cancel()
				return
			}
			timer := time.NewTimer(pongWait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.pong:
				timer.Stop()
			case <-timer.C:
				s.writeClose(terminalCloseTransport, "pong timeout")
				cancel()
				return
			}
		}
	}
}

func (s *terminalSocket) writeText(v any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteJSON(v)
}

func (s *terminalSocket) writeBinary(p []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (s *terminalSocket) writeClose(code int, text string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, text),
		time.Now().Add(time.Second),
	)
}
