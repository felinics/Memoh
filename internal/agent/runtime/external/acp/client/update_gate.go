package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	// maxQueuedUpdates stays well below the 1024 notifications acp-go-sdk
	// queues before it closes the connection, leaving room for the
	// notifications the gate does not count.
	maxQueuedUpdates = 512
	// updateStallTimeout ends a wait no handler should cause: handlers return
	// without waiting, so a slot this late means one is stuck or was never
	// freed, and closing the connection beats leaving it silently stopped.
	// The SDK keeps the cause to itself and callers only see a disconnect, so
	// the gate logs it.
	updateStallTimeout = time.Minute
	// maxAgentLineSize is acp-go-sdk's limit for one message.
	maxAgentLineSize = 10 * 1024 * 1024
)

var errUpdatesStalled = errors.New("ACP session/update handling stalled")

// updateGate makes the Agent wait instead of overflowing acp-go-sdk's
// notification queue. The SDK reads each message as it arrives, queues
// notifications for its one notification goroutine and closes the
// connection once 1024 are waiting. Decoding an update costs more than
// reading it, so an Agent replaying a session or flushing a backlog outruns
// that goroutine even when every handler returns at once. The gate hands the
// SDK one line at a time and holds a session/update back until fewer than
// maxQueuedUpdates are waiting, so the backlog stays in the Agent's pipe;
// handle frees the slot when the update reaches it. Messages behind a held
// update wait with it, which is why no notification handler may wait on the
// Agent or on anything that does.
type updateGate struct {
	logger  *slog.Logger
	lines   *bufio.Scanner
	slots   chan struct{}
	line    []byte
	pending []byte
}

func newUpdateGate(logger *slog.Logger, r io.Reader) *updateGate {
	lines := bufio.NewScanner(r)
	lines.Buffer(nil, maxAgentLineSize)
	return &updateGate{logger: logger, lines: lines, slots: make(chan struct{}, maxQueuedUpdates)}
}

func (g *updateGate) Read(p []byte) (int, error) {
	for len(g.pending) == 0 {
		if !g.lines.Scan() {
			if err := g.lines.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if isQueuedUpdate(g.lines.Bytes()) {
			if err := g.acquire(); err != nil {
				return 0, err
			}
		}
		g.line = append(append(g.line[:0], g.lines.Bytes()...), '\n')
		g.pending = g.line
	}
	n := copy(p, g.pending)
	g.pending = g.pending[n:]
	return n, nil
}

func (g *updateGate) acquire() error {
	select {
	case g.slots <- struct{}{}:
		return nil
	default:
	}
	stall := time.NewTimer(updateStallTimeout)
	defer stall.Stop()
	select {
	case g.slots <- struct{}{}:
		return nil
	case <-stall.C:
		if g.logger != nil {
			g.logger.Error("ACP session/update handling stalled; closing connection",
				slog.Int("queued", maxQueuedUpdates), slog.Duration("timeout", updateStallTimeout))
		}
		return errUpdatesStalled
	}
}

// release never blocks: an update the gate did not count, such as one sent
// with an ID, only frees a slot early.
func (g *updateGate) release() {
	if g == nil {
		return
	}
	select {
	case <-g.slots:
	default:
	}
}

// isQueuedUpdate reports whether acp-go-sdk queues line as a session/update
// notification. It decodes the line into the SDK's message shape because a
// line the SDK fails to decode is dropped, and a slot taken for it would
// never be freed.
func isQueuedUpdate(line []byte) bool {
	if len(bytes.TrimSpace(line)) == 0 {
		return false
	}
	var msg struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      *json.RawMessage  `json:"id,omitempty"`
		Method  string            `json:"method,omitempty"`
		Params  json.RawMessage   `json:"params,omitempty"`
		Result  json.RawMessage   `json:"result,omitempty"`
		Error   *acp.RequestError `json:"error,omitempty"`
	}
	return json.Unmarshal(line, &msg) == nil && msg.ID == nil && msg.Method == acp.ClientMethodSessionUpdate
}
