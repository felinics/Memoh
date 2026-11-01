package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
)

const (
	// decisionQuiesceTimeout bounds how long a cancelled prompt waits for
	// in-flight permission/Form callbacks to unwind before the session is
	// reused; past it the process is replaced instead (see prompt).
	decisionQuiesceTimeout = 3 * time.Second

	// optionsConversation, followed by its project, stands for an options
	// session where a conversation's ID would: no conversation ID starts
	// with it.
	optionsConversation = "\x00"

	runtimeIDPrefix = "rt_"
)

// ErrRuntimeConfigUpdateFailed reports a transport or protocol failure while
// applying the model/reasoning values requested for a turn.
var ErrRuntimeConfigUpdateFailed = errors.New("ACP runtime configuration update failed")

// Host runs the processes of one kind of ACP agent and the conversations'
// sessions on them, as its Extension says. Processes are kept by the shared
// process supervisor (agentproc), which decides when one starts, is shared,
// is replaced or is closed. They never survive a server restart: agents own
// their durable files under /data, and a new process restores each
// conversation's native session from them.
type Host struct {
	logger         *slog.Logger
	procs          *agentproc.Supervisor
	ext            Extension
	gateway        toolmount.Gateway
	approval       client.ToolApprovalService
	userInput      client.UserInputService
	sessionRuntime HistoryResetWaiter

	mu sync.Mutex
	// byID finds a live session by the runtime ID its MCP requests carry.
	byID map[string]*Binding
	// holders finds the session a conversation runs on, whichever process
	// holds it; closing holds the conversation's sessions out of service but
	// not yet closed. A new session for the conversation opens only once they
	// are gone: a late session/close would otherwise end the session it
	// restores.
	holders map[string]*Binding
	closing map[string][]*Binding
}

// HistoryResetWaiter holds a conversation's session off while its history is
// reset.
type HistoryResetWaiter interface {
	WaitForHistoryReset(ctx context.Context, botID, sessionID string) error
}

// HostDeps are the services a Host's turns reach beyond its processes.
// Optional ones (nil) leave the feature off.
type HostDeps struct {
	Logger *slog.Logger
	Procs  *agentproc.Supervisor
	// Gateway serves Memoh's tools to the sessions.
	Gateway   toolmount.Gateway
	Approval  client.ToolApprovalService
	UserInput client.UserInputService
	// SessionRuntime, when set, holds sessions off while a history reset
	// is under way.
	SessionRuntime HistoryResetWaiter
}

// NewHost returns a Host running ext's agents.
func NewHost(ext Extension, deps HostDeps) *Host {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Host{
		logger:         logger,
		procs:          deps.Procs,
		ext:            ext,
		gateway:        deps.Gateway,
		approval:       deps.Approval,
		userInput:      deps.UserInput,
		sessionRuntime: deps.SessionRuntime,
		byID:           map[string]*Binding{},
		holders:        map[string]*Binding{},
		closing:        map[string][]*Binding{},
	}
}

// register records b as live: its MCP requests resolve to it, and a
// conversation's session is the one its conversation runs on.
func (h *Host) register(b *Binding) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byID[b.id] = b
	if b.conversation != "" && !b.transient {
		h.holders[b.conversation] = b
	}
}

// unregister drops b from the live tables, if it is still there, and
// tracks it as closing until its teardown ends.
func (h *Host) unregister(b *Binding) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byID[b.id] == b {
		delete(h.byID, b.id)
	}
	if h.holders[b.conversation] == b {
		delete(h.holders, b.conversation)
	}
	if b.conversation != "" && !b.transient {
		h.closing[b.conversation] = append(h.closing[b.conversation], b)
	}
}

// forgetClosing ends b's tracking as closing: it is gone.
func (h *Host) forgetClosing(b *Binding) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rest := slices.DeleteFunc(h.closing[b.conversation], func(other *Binding) bool { return other == b })
	if len(rest) == 0 {
		delete(h.closing, b.conversation)
	} else {
		h.closing[b.conversation] = rest
	}
}

// awaitClosed waits until every old session of the conversation still
// closing that holds native, the session about to be restored on r, is gone.
// One the agent did not confirm closing ends only with its process, which is
// retired so that it exits; when that is r itself, which the caller holds,
// errProcessClosed sends the caller to another process.
func (h *Host) awaitClosed(ctx context.Context, conversation, native string, r *Process) error {
	if native == "" {
		return nil
	}
	h.mu.Lock()
	closing := slices.Clone(h.closing[conversation])
	h.mu.Unlock()
	for _, b := range closing {
		if b.sess.ID() != native {
			continue
		}
		select {
		case <-b.settled:
		case <-ctx.Done():
			return ctx.Err()
		}
		if b.endsWithProcess.Load() {
			h.procs.MarkBroken(b.rt.key, b.rt)
			if b.rt == r {
				return errProcessClosed
			}
		}
		select {
		case <-b.gone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// holder returns the live session conversation runs on, on any process.
func (h *Host) holder(conversation string) *Binding {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.holders[strings.TrimSpace(conversation)]
}

// ResolveRuntimeToolContext resolves the trusted MCP tool context for a
// session referenced by its stable ID (from the headers it was opened with).
// Fails closed: a closed or foreign session resolves to nothing.
func (h *Host) ResolveRuntimeToolContext(botID, runtimeID, toolToken string) (mcp.ToolSessionContext, bool) {
	h.mu.Lock()
	b := h.byID[strings.TrimSpace(runtimeID)]
	h.mu.Unlock()
	if b == nil || b.rt.key.BotID != strings.TrimSpace(botID) || b.toolToken == "" || strings.TrimSpace(toolToken) != b.toolToken {
		return mcp.ToolSessionContext{}, false
	}
	return b.toolContext(), true
}

// leaseProcess returns the live process l names, starting one when there is
// none that fits.
func (h *Host) leaseProcess(ctx context.Context, l Launch) (*Process, func(), error) {
	proc, release, err := h.procs.Lease(ctx, l.Key, l.Fingerprint, func(startCtx context.Context) (agentproc.Process, string, error) {
		r, fingerprint, err := h.startProcess(startCtx, l)
		if err != nil {
			h.logger.WarnContext(ctx, "ACP agent process start failed",
				slog.String("bot_id", l.Key.BotID), slog.String("bot_agent_id", l.Key.BotAgentID), slog.Any("error", err))
			return nil, "", err
		}
		return r, fingerprint, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return proc.(*Process), release, nil
}

// Process leases the Agent's process for its own work outside any
// conversation (sign-in, account, catalog). The caller must call the
// returned release when done.
func (h *Host) Process(ctx context.Context, input PromptInput) (*Process, func(), error) {
	l, err := h.ext.Launch(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return h.leaseProcess(ctx, l)
}

// Acquire returns input's conversation's session, opening it when it has
// none that fits, and a release the caller must call when done. A stored
// session the agent no longer has is replaced with a new one.
func (h *Host) Acquire(ctx context.Context, input PromptInput) (*Binding, func(), error) {
	return h.acquire(ctx, input, false, false)
}

// AcquireExisting is Acquire for a control on the conversation's own
// session: one the agent no longer has is external.ErrThreadUnavailable,
// never replaced. A control run on a new session would report success for a
// conversation it never touched.
func (h *Host) AcquireExisting(ctx context.Context, input PromptInput) (*Binding, func(), error) {
	return h.acquire(ctx, input, false, true)
}

// acquire returns the conversation's session. A fresh acquire opens a
// throwaway session for one turn.
func (h *Host) acquire(ctx context.Context, input PromptInput, fresh, existing bool) (*Binding, func(), error) {
	if h.sessionRuntime != nil {
		if err := h.sessionRuntime.WaitForHistoryReset(ctx, input.BotID, input.SessionID); err != nil {
			return nil, nil, err
		}
	}
	l, err := h.ext.Launch(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	var b *Binding
	var release func()
	for attempt := 0; ; attempt++ {
		var r *Process
		r, release, err = h.leaseProcess(ctx, l)
		if err != nil {
			return nil, nil, err
		}
		b, err = r.bind(ctx, input, fresh, existing)
		if err == nil {
			break
		}
		release()
		if !errors.Is(err, errProcessClosed) {
			return nil, nil, err
		}
		if attempt == 1 {
			return nil, nil, fmt.Errorf("open ACP session: %w", err)
		}
		// The process is going away under the open, or holds the
		// conversation's old session until it does: the next lease starts
		// another.
	}
	if fresh {
		return b, func() { b.close(); release() }, nil
	}
	return b, release, nil
}

// Prompt runs one turn on input's conversation, opening its session when it
// has none that fits. Discuss turns inject a complete bounded context, so
// they run on a new native session that no later turn picks up.
func (h *Host) Prompt(ctx context.Context, input PromptInput) (client.PromptResult, error) {
	var err error
	input.Images, err = client.NormalizePromptImages(input.Images)
	if err != nil {
		return client.PromptResult{}, err
	}
	hasAttachmentContext := len(input.AttachmentReferences) > 0 && len(promptResources(input)) > 0
	if strings.TrimSpace(input.Prompt) == "" && len(input.Images) == 0 && !hasAttachmentContext {
		return client.PromptResult{}, client.ErrPromptRequired
	}
	b, release, err := h.acquire(ctx, input, input.ForceFreshRuntime, false)
	if err != nil {
		return client.PromptResult{}, err
	}
	defer release()
	return h.prompt(ctx, b, input)
}

func (h *Host) prompt(ctx context.Context, b *Binding, input PromptInput) (client.PromptResult, error) {
	defer b.lockOp()()
	sess := b.sess
	toolCtx := toolSessionContext(ctx, input, b)
	b.mu.Lock()
	b.active = &toolCtx
	b.mu.Unlock()
	defer b.clearActive()

	if err := h.ext.Configure(ctx, b, input); err != nil {
		if isPromptConfigSelectionError(err) {
			// Refused before anything reached the agent: the session is as it
			// was, and the next turn reapplies.
			return client.PromptResult{}, err
		}
		// Anything else may have reached the agent unanswered, cancelled
		// requests too: what the session runs on is no longer known, so the
		// next turn reopens it.
		b.discard()
		if configUpdateCanceled(ctx, err) {
			return client.PromptResult{}, err
		}
		return client.PromptResult{}, fmt.Errorf("%w: %w", ErrRuntimeConfigUpdateFailed, err)
	}

	turn := external.NewTurnStream(input.Sink, input.ToolOutputLimit)
	defer turn.Finish()
	// Taken only after the configuration applied: a turn that fails or is
	// stopped before then leaves the notice to the next turn.
	b.mu.Lock()
	historyLost := b.historyLost
	b.historyLost = false
	b.prompted = true
	b.mu.Unlock()
	if historyLost {
		turn.EmitStreamEvent(event.StreamEvent{
			Type:       event.RuntimeNotice,
			NoticeKind: event.NoticeNativeHistoryLost,
			Delta:      "The agent could not restore this conversation's session and started a new one. It does not remember the earlier messages shown here.",
		})
	}
	// Said every turn: silently missing tools is what this notice exists to
	// prevent.
	if b.toolsMissing != "" {
		toolmount.EmitUnavailableNotice(turn, b.toolsMissing)
	}
	unregisterToolEvents := toolmount.RegisterTurnSink(h.gateway.Contexts, input.BotID, input.SessionID, input.RunID, turn.EmitStreamEvent)
	defer unregisterToolEvents()

	// The agent cannot finish a cancelled prompt while one of its permission
	// requests still waits on the user. Cancelling the pending rows wakes those
	// waiters, so the cancellation completes in its grace window and the
	// session survives the Stop.
	promptDone := make(chan struct{})
	defer close(promptDone)
	go func() {
		select {
		case <-ctx.Done():
		case <-promptDone:
			// Both may be ready; recheck so a cancellation is never skipped.
			if ctx.Err() == nil {
				return
			}
		}
		h.cancelPendingDecisions(context.WithoutCancel(ctx), toolCtx.BotID, toolCtx.SessionID,
			"decision cancelled: the turn was aborted before a response arrived")
	}()

	resources := promptResources(input)
	options := client.PromptOptions{
		ToolOutputLimit:   input.ToolOutputLimit,
		Images:            input.Images,
		AllowResourceOnly: len(input.AttachmentReferences) > 0 && len(resources) > 0,
		RequiredCommand:   input.RequiredCommand,
	}
	result, err := sess.PromptWithToolContextOptions(ctx, input.Prompt, resources, toolCtx, options, turn)
	if errors.Is(err, client.ErrImagePromptUnsupported) && len(options.Images) > 0 && input.CanFallbackImagesToFiles {
		options.Images = nil
		result, err = sess.PromptWithToolContextOptions(ctx, input.Prompt, resources, toolCtx, options, turn)
	}
	// Stop MCP deliveries before taking the durable result snapshot.
	unregisterToolEvents()
	// The turn sink's record also holds the gateway's tool events.
	result.Output = turn.Transcript(result.Text)
	switch {
	case err == nil,
		errors.Is(err, client.ErrImagePromptUnsupported),
		errors.Is(err, client.ErrInvalidPromptImage),
		errors.Is(err, client.ErrPromptRequired),
		errors.Is(err, client.ErrAgentCommandUnavailable):
		return result, err
	case ctx.Err() != nil:
		// A stopped turn says nothing about the agent: keep the session.
		// Permission and form callbacks outlive the prompt on the
		// connection's goroutines, so reuse waits for them. One that outlives
		// the grace window, or a cancellation the agent never confirmed, may
		// hold a write on the connection every session of the process
		// shares: the process is retired, its other turns finish on it, and
		// the conversations move to a new one.
		if errors.Is(err, client.ErrPromptCancellationUnconfirmed) || !sess.WaitDecisionCallbacksIdle(decisionQuiesceTimeout) {
			_ = sess.ForceClose() //nolint:contextcheck // forced teardown outlives the cancelled turn.
			b.discard()
			h.procs.MarkBroken(b.rt.key, b.rt)
		}
		return result, err
	default:
		// A failed prompt leaves the session's state unknown: the next turn
		// restores it on a new session. A crashed agent takes its process
		// down with it, and the next lease starts another.
		b.discard()
		return result, err
	}
}

// isPromptConfigSelectionError reports a selection the client refused from
// what the session offers, before any request reached the agent.
func isPromptConfigSelectionError(err error) bool {
	return errors.Is(err, client.ErrModelIDRequired) ||
		errors.Is(err, client.ErrModelSelectionUnsupported) ||
		errors.Is(err, client.ErrModelUnavailable) ||
		errors.Is(err, client.ErrReasoningEffortRequired) ||
		errors.Is(err, client.ErrReasoningSelectionUnsupported) ||
		errors.Is(err, client.ErrReasoningEffortUnavailable) ||
		errors.Is(err, client.ErrModeIDRequired) ||
		errors.Is(err, client.ErrModeSelectionUnsupported) ||
		errors.Is(err, client.ErrModeUnavailable)
}

func configUpdateCanceled(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

//nolint:contextcheck // cleanup is detached from the closed prompt but retains its fence value.
func (h *Host) cancelPendingDecisions(parent context.Context, botID, sessionID, reason string) {
	botID, sessionID = strings.TrimSpace(botID), strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return
	}
	var cleanup sync.WaitGroup
	cancel := func(kind string, fn func(context.Context) error) {
		cleanup.Add(1)
		go func() {
			defer cleanup.Done()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
			defer cancel()
			if err := fn(ctx); err != nil && !errors.Is(err, runtimefence.ErrStale) {
				h.logger.WarnContext(parent, "cancel pending ACP decisions failed", slog.String("kind", kind),
					slog.Any("error", err), slog.String("bot_id", botID), slog.String("session_id", sessionID))
			}
		}()
	}
	if approval, ok := h.approval.(interface {
		CancelPendingForSession(context.Context, string, string, string) ([]toolapproval.Request, error)
	}); ok {
		cancel("approvals", func(ctx context.Context) error {
			_, err := approval.CancelPendingForSession(ctx, botID, sessionID, reason)
			return err
		})
	}
	if input, ok := h.userInput.(interface {
		CancelPendingForSession(context.Context, string, string, string) ([]userinput.Request, error)
	}); ok {
		cancel("user inputs", func(ctx context.Context) error {
			_, err := input.CancelPendingForSession(ctx, botID, sessionID, reason)
			return err
		})
	}
	cleanup.Wait()
}

func toolSessionContext(ctx context.Context, input PromptInput, b *Binding) client.ToolSessionContext {
	fence, _ := runtimefence.FromContext(ctx)
	return client.ToolSessionContext{
		BotID:             b.rt.key.BotID,
		ChatID:            external.FirstNonEmpty(input.ChatID, b.rt.key.BotID),
		RuntimeID:         b.id,
		SessionID:         strings.TrimSpace(input.SessionID),
		RunID:             strings.TrimSpace(input.RunID),
		SessionType:       external.FirstNonEmpty(input.SessionType, sessionmode.ACPAgent),
		RouteID:           input.RouteID,
		ChannelIdentityID: input.ChannelIdentityID,
		SessionToken:      input.SessionToken,
		CurrentPlatform:   input.CurrentPlatform,
		ReplyTarget:       input.ReplyTarget,
		ConversationType:  input.ConversationType,
		// PromptInput.ReasoningEffort is the current turn's explicit selection.
		// The bot-stored fallback is loaded by SpawnProvider when this ACP tool
		// context does not already carry one.
		ReasoningRequestedEffort: strings.TrimSpace(input.ReasoningEffort),
		CanRequestUserInput:      input.CanRequestUserInput,
		SupportsImageInput:       input.SupportsImageInput,
		ContextBudgetMaxTokens:   input.ContextBudgetMaxTokens,
		RuntimeFence:             fence,
		RunContext:               ctx,
		RuntimeGuard:             input.RuntimeGuard,
	}
}

func promptResources(input PromptInput) []client.PromptResource {
	markdown := strings.TrimSpace(input.ContextMarkdown)
	if markdown == "" {
		return nil
	}
	uri := strings.TrimSpace(input.ContextURI)
	if uri == "" {
		uri = contextURI
	}
	return []client.PromptResource{{URI: uri, MimeType: "text/markdown", Text: markdown}}
}
