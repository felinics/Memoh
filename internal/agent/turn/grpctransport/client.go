package grpctransport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agent/turn/turnpb"
)

type Client struct {
	client turnpb.TurnServiceClient
	logger *slog.Logger
}

// ClientOption configures optional client capabilities.
type ClientOption func(*Client)

// WithClientLogger routes client-side transport warnings (dropped control
// frames and the like) to the given logger instead of slog.Default().
func WithClientLogger(log *slog.Logger) ClientOption {
	return func(c *Client) {
		if log != nil {
			c.logger = log
		}
	}
}

func NewClient(conn grpc.ClientConnInterface, opts ...ClientOption) *Client {
	c := &Client{client: turnpb.NewTurnServiceClient(conn), logger: slog.Default()}
	for _, opt := range opts {
		opt(c)
	}
	c.logger = c.logger.With(slog.String("component", "turn_rpc_client"))
	return c
}

func (c *Client) StartTurn(ctx context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	data, err := marshalStartTurnCommand(cmd)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	stream, err := c.client.Run(runCtx)
	if err != nil {
		cancel()
		return nil, mapClientError(err)
	}
	if err := stream.Send(&turnpb.RunRequest{Body: &turnpb.RunRequest_StartJson{StartJson: data}}); err != nil {
		cancel()
		return nil, mapClientError(err)
	}
	first, err := stream.Recv()
	if err != nil {
		cancel()
		return nil, mapClientError(err)
	}
	started := first.GetStarted()
	if started == nil || started.GetRunId() == "" {
		cancel()
		return nil, errors.New("turn rpc: missing started frame")
	}
	h := &runHandle{
		id: started.GetRunId(), stream: stream,
		events: make(chan turn.Event, 16), errs: make(chan error, 1),
		ctx: runCtx, cancel: cancel, done: make(chan struct{}),
		logger: c.logger,
	}
	go h.pump()
	return h, nil
}

func (c *Client) RespondToolApproval(ctx context.Context, input turn.ToolApprovalResponse, eventCh chan<- json.RawMessage) error {
	data, err := marshalToolApprovalResponse(input)
	if err != nil {
		return err
	}
	stream, err := c.client.RespondToolApproval(ctx, &turnpb.JsonRequest{Json: data})
	if err != nil {
		return mapClientError(err)
	}
	return receiveContinuation(ctx, stream.Recv, eventCh)
}

func (c *Client) RespondUserInput(ctx context.Context, input turn.UserInputResponse, eventCh chan<- json.RawMessage) error {
	data, err := marshalUserInputResponse(input)
	if err != nil {
		return err
	}
	stream, err := c.client.RespondUserInput(ctx, &turnpb.JsonRequest{Json: data})
	if err != nil {
		return mapClientError(err)
	}
	return receiveContinuation(ctx, stream.Recv, eventCh)
}

func (c *Client) AdvancePlainTextUserInput(ctx context.Context, input userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return userinput.AdvanceTextResult{}, err
	}
	resp, err := c.client.AdvancePlainTextUserInput(ctx, &turnpb.JsonRequest{Json: data})
	if err != nil {
		return userinput.AdvanceTextResult{}, mapClientError(err)
	}
	var result userinput.AdvanceTextResult
	if err := json.Unmarshal(resp.GetJson(), &result); err != nil {
		return userinput.AdvanceTextResult{}, err
	}
	return result, nil
}

type continuationReceiver func() (*turnpb.EventResponse, error)

func receiveContinuation(ctx context.Context, recv continuationReceiver, eventCh chan<- json.RawMessage) error {
	for {
		event, err := recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return mapClientError(err)
		}
		select {
		case eventCh <- json.RawMessage(event.GetPayload()):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type runHandle struct {
	id     string
	stream turnpb.TurnService_RunClient
	events chan turn.Event
	errs   chan error
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	logger *slog.Logger
	mu     sync.Mutex
	once   sync.Once
}

func (h *runHandle) RunID() string             { return h.id }
func (h *runHandle) Events() <-chan turn.Event { return h.events }
func (h *runHandle) Errs() <-chan error        { return h.errs }

func (h *runHandle) Inject(ctx context.Context, msg turn.InjectMessage) error {
	data, err := json.Marshal(injectPayload{
		Text:            msg.Text,
		Attachments:     msg.Attachments,
		HeaderifiedText: msg.HeaderifiedText,
	})
	if err != nil {
		return err
	}
	return h.send(ctx, &turnpb.RunRequest{Body: &turnpb.RunRequest_InjectJson{InjectJson: data}})
}

func (h *runHandle) AddOutboundAssets(refs []turn.OutboundAssetRef) {
	data, err := json.Marshal(refs)
	if err != nil {
		h.logger.Warn("encode outbound asset refs failed",
			slog.String("run_id", h.id), slog.Any("error", err))
		return
	}
	// Bound by the run context so a finished run fails fast; a failure here
	// means the asset refs may miss server-side persistence, which must at
	// least be visible in logs.
	if err := h.send(h.ctx, &turnpb.RunRequest{Body: &turnpb.RunRequest_OutboundAssetsJson{OutboundAssetsJson: data}}); err != nil {
		h.logger.Warn("send outbound asset refs failed",
			slog.String("run_id", h.id), slog.Any("error", err))
	}
}

// Cancel hard-cancels the stream context. This is deliberate: a canceling
// consumer does not want tail events, and the server observes the broken
// stream context to cancel the run and release its idempotency claim. The
// RunRequest cancel frame remains server-supported for a future graceful
// (drain-then-complete) cancel without a wire change.
func (h *runHandle) Cancel() {
	h.once.Do(func() {
		h.cancel()
	})
}

func (h *runHandle) send(ctx context.Context, frame *turnpb.RunRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return mapClientError(h.stream.Send(frame))
	}
}

func (h *runHandle) pump() {
	defer close(h.events)
	defer close(h.errs)
	defer close(h.done)
	defer h.cancel()
	for {
		frame, err := h.stream.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			select {
			case h.errs <- mapClientError(err):
			case <-h.ctx.Done():
			}
			return
		}
		if frame.GetCompleted() != nil {
			return
		}
		event := frame.GetEvent()
		if event == nil {
			continue
		}
		select {
		case h.events <- eventFromProto(event):
		case <-h.ctx.Done():
			return
		}
	}
}

func mapClientError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.AlreadyExists:
		return turn.ErrDuplicateTurn
	case codes.PermissionDenied:
		return turn.ErrTeamNotServed
	case codes.Canceled:
		return context.Canceled
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	case codes.FailedPrecondition:
		if feedback := decodeFeedback(status.Convert(err).Message()); feedback != nil {
			return feedback
		}
		return err
	default:
		return err
	}
}
