package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// errProcessClosed answers a caller waiting on a process that went away.
var errProcessClosed = errors.New("ACP agent process closed")

// Binding is one session on an agent process: a conversation's, a discuss
// turn's throwaway one, or the one that reports what a new conversation
// could choose. Everything that belongs to a conversation lives here, not on
// the process: its tool identity and route, its project and owner, what else
// it opened under, and the lock that keeps its turns and configuration
// changes one at a time.
type Binding struct {
	rt *Process
	// conversation is the thread the session belongs to; empty for the
	// options session.
	conversation string
	transient    bool

	id        string
	toolToken string
	project   string
	owner     string
	// variant is what else the session opened under (SessionSpec.Variant).
	variant string
	// openedFor is the stored native session ID the session opened for: the
	// one it restored, or empty for a conversation that had none of its own.
	openedFor string

	sess      *client.Session
	stopTools func()
	// toolsURL is where the session reaches Memoh's tools; empty without.
	toolsURL string
	// toolsMissing says why the session opened without Memoh's tools; every
	// turn tells the user.
	toolsMissing string

	// op keeps prompts and configuration changes one at a time; busy is set
	// while it is held, so the process is not closed for idleness.
	op   sync.Mutex
	busy atomic.Bool

	mu             sync.Mutex
	active         *client.ToolSessionContext
	defaultModelID string
	// historyLost records that opening replaced the conversation's stored
	// session with a new one. The next prompt tells the user.
	historyLost bool
	// prompted records that a prompt reached the session, after which the
	// conversation's stored session ID must name it.
	prompted bool
	// retired marks the session out of service; gone closes once its
	// teardown has finished (lifecycle model: agentproc/AGENTS.md).
	retired bool
	gone    chan struct{}
	// settled closes once the session's own teardown is done; by then
	// endsWithProcess says whether it is gone only once its process exits,
	// the agent not having confirmed the close.
	settled         chan struct{}
	endsWithProcess atomic.Bool
}

// Session is the ACP session, for an extension's own methods.
func (b *Binding) Session() *client.Session { return b.sess }

// Process is the agent process the session runs on.
func (b *Binding) Process() *Process { return b.rt }

// Hold keeps the session's turns and other controls off until the returned
// release, for an extension's own control of the session.
func (b *Binding) Hold() func() { return b.lockOp() }

// ToolsMounted reports whether the session opened with Memoh's tools.
func (b *Binding) ToolsMounted() bool { return b.toolsURL != "" }

func (b *Binding) lockOp() func() {
	b.op.Lock()
	b.busy.Store(true)
	return func() {
		b.busy.Store(false)
		b.op.Unlock()
	}
}

func (b *Binding) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.retired
}

// close ends the session: session/close when the agent offers it, so the
// agent lets go of it before another process resumes it. A prompt still
// running is cancelled, with what it waits on.
func (b *Binding) close() {
	if b.retire() {
		b.teardown(false)
	}
}

// discard takes the session out of service at once and ends it in the
// background, for a caller that still holds its operation lock.
func (b *Binding) discard() {
	if b.retire() {
		go b.teardown(false)
	}
}

// abandon ends the session with its process: the process is going away and
// takes the agent's side of the session with it, so no request is sent to an
// agent that may no longer answer.
func (b *Binding) abandon() {
	if b.retire() {
		b.teardown(true)
	}
}

// retire takes b out of service at once: it leaves the tables, so nothing
// picks it up again, and is tracked as closing until its teardown ends. It
// reports whether this call did it.
func (b *Binding) retire() bool {
	b.mu.Lock()
	if b.retired {
		b.mu.Unlock()
		return false
	}
	b.retired = true
	b.mu.Unlock()
	b.rt.forget(b)
	b.rt.host.unregister(b)
	return true
}

// teardown ends the session. It is gone once the agent confirmed closing it,
// or, when the agent did not, once its process has exited: until then the
// agent may still hold it (see Host.awaitClosed). Nothing restores a session
// outside a conversation, so one is gone at once.
//
//nolint:contextcheck // teardown follows the session, not the request that ends it.
func (b *Binding) teardown(force bool) {
	b.cancelActiveDecisions()
	closeErr := client.ErrSessionCloseUnconfirmed
	if force {
		_ = b.sess.ForceClose()
	} else {
		closeErr = b.sess.Close()
	}
	if b.stopTools != nil {
		b.stopTools()
	}
	if closeErr == nil || b.conversation == "" || b.transient {
		close(b.settled)
		b.finish()
		return
	}
	b.endsWithProcess.Store(true)
	close(b.settled)
	go func() {
		<-b.rt.proc.Exited()
		b.finish()
	}()
}

// finish marks the session gone.
func (b *Binding) finish() {
	close(b.gone)
	b.rt.disown(b)
	b.rt.host.forgetClosing(b)
}

// cancelActiveDecisions cancels what the running prompt waits on, under the
// prompt's fence: a newer owner of the conversation keeps its own.
func (b *Binding) cancelActiveDecisions() {
	b.mu.Lock()
	active := b.active
	b.mu.Unlock()
	if active == nil || active.RunContext == nil || b.conversation == "" {
		return
	}
	cleanupCtx := context.WithoutCancel(active.RunContext)
	if active.RuntimeFence.Valid() {
		cleanupCtx = runtimefence.WithContext(cleanupCtx, active.RuntimeFence)
	}
	b.rt.host.cancelPendingDecisions(cleanupCtx, b.rt.key.BotID, b.conversation,
		"decision cancelled: the ACP agent session closed before a response arrived")
}

func (b *Binding) clearActive() {
	b.mu.Lock()
	b.active = nil
	b.mu.Unlock()
}

// fits reports whether b can serve the conversation as spec describes it.
// A session that opened without Memoh's tools is opened again once they can
// be mounted.
func (b *Binding) fits(spec SessionSpec, toolsMountable bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.retired, b.project != spec.ProjectPath, b.variant != spec.Variant:
		return false
	case b.toolsMissing != "" && toolsMountable:
		return false
	case !spec.Stored.Known:
		return true
	}
	current := ""
	if spec.Stored.Owned {
		current = spec.Stored.ID
	}
	if current == b.sess.ID() {
		return true
	}
	// The record still says what it said when the session opened: the new
	// ID is written once the turn that used it ends. An empty record after a
	// prompt is different: the turn wrote the ID and a history reset cleared
	// it, as it clears a Codex thread.
	return current == b.openedFor && (current != "" || !b.prompted)
}

// bind returns the conversation's session on r, opening it when r has none
// that fits. A conversation another process still holds is closed there
// first: a session continued on two processes at once answers from the old
// one's stale context. A session at work is never replaced: that would
// cancel its turn. A fresh bind opens a throwaway session for one turn; an
// existing one only the conversation's own stored session.
func (r *Process) bind(ctx context.Context, input PromptInput, fresh, existing bool) (*Binding, error) {
	spec, err := r.host.ext.Session(ctx, r, input)
	if err != nil {
		return nil, err
	}
	conversation := strings.TrimSpace(input.SessionID)
	if fresh {
		return r.open(ctx, input, spec, "", "", true, false)
	}
	unlock, err := r.lockOpening(ctx, conversation)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r.mu.Lock()
	current := r.sessions[conversation]
	r.mu.Unlock()
	if current != nil && current.fits(spec, r.toolsMountable(input)) {
		return current, nil
	}
	held := r.host.holder(conversation)
	for _, replaced := range []*Binding{current, held} {
		if replaced != nil && replaced.busy.Load() {
			return nil, external.Fail(external.FailureRuntimeBusy, nil)
		}
	}
	if current != nil {
		current.close() //nolint:contextcheck // closing a session follows its own lifecycle, not the caller's request.
	}
	if held != nil && held.rt != r {
		held.close() //nolint:contextcheck // closing a session follows its own lifecycle, not the caller's request.
	}
	restore := ""
	if spec.Stored.Owned {
		restore = spec.Stored.ID
	}
	if existing && restore == "" {
		return nil, external.ErrThreadUnavailable
	}
	// An old session of the conversation still holding the native session
	// restored now must be gone first, on whichever process: a session
	// continued on two at once answers from stale context, and a late
	// session/close would end the one opened now.
	if err := r.host.awaitClosed(ctx, conversation, restore, r); err != nil {
		return nil, err
	}
	return r.open(ctx, input, spec, spec.Stored.ID, restore, false, existing)
}

// optionsSession returns the session that reports what a new conversation
// with the Agent in input's project could choose, opening it on first use.
func (r *Process) optionsSession(ctx context.Context, input PromptInput) (*Binding, error) {
	options := input
	options.SessionID = ""
	spec, err := r.host.ext.Session(ctx, r, options)
	if err != nil {
		return nil, err
	}
	unlock, err := r.lockOpening(ctx, optionsConversation+spec.ProjectPath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r.mu.Lock()
	current := r.options[spec.ProjectPath]
	r.mu.Unlock()
	if current != nil && !current.isClosed() {
		return current, nil
	}
	return r.open(ctx, options, spec, "", "", false, false)
}

// probeSession opens a throwaway session outside any conversation, to read
// what one choice would offer without changing the options session every
// draft reads its defaults from. The caller closes it.
func (r *Process) probeSession(ctx context.Context, input PromptInput) (*Binding, error) {
	probe := input
	probe.SessionID = ""
	spec, err := r.host.ext.Session(ctx, r, probe)
	if err != nil {
		return nil, err
	}
	return r.open(ctx, probe, spec, "", "", true, false)
}

// open opens a session on r: the stored one when restore names it, a new
// one otherwise. stored is what the conversation had stored, owned or not,
// so a replaced session tells the user the agent starts over. An existing
// open never replaces the stored session.
func (r *Process) open(ctx context.Context, input PromptInput, spec SessionSpec, stored, restore string, transient, existing bool) (*Binding, error) {
	h := r.host
	conversation := strings.TrimSpace(input.SessionID)
	resolved, err := client.ResolveSessionContext(client.SessionContextInput{Backend: r.info.Backend, ProjectPath: spec.ProjectPath})
	if err != nil {
		return nil, fmt.Errorf("resolve ACP session context: %w", err)
	}
	b := &Binding{
		rt:           r,
		conversation: conversation,
		transient:    transient,
		id:           runtimeIDPrefix + uuid.NewString(),
		toolToken:    uuid.NewString(),
		project:      spec.ProjectPath,
		owner:        external.FirstNonEmpty(input.RuntimeOwnerAccountID, input.ChannelIdentityID),
		variant:      spec.Variant,
		gone:         make(chan struct{}),
		settled:      make(chan struct{}),
		openedFor:    restore,
	}
	if !r.own(b) {
		return nil, errProcessClosed
	}
	cfg := client.SessionConfig{
		WorkspaceRoot: resolved.WorkspaceRoot,
		ProjectPath:   resolved.ProjectPath,
		ToolSession:   b.stableToolIdentity(),
		Workspace:     r.bridge,
		ToolEnv:       r.proc.ToolEnv,
		UnsetEnv:      r.proc.UnsetEnv,
		Stderr:        r.proc.StderrTail,
	}
	if conversation != "" {
		cfg.ToolApproval = h.approval
		cfg.ToolPreflightGateway = h.gateway.Tools
		cfg.UserInput = h.userInput
		if b.toolsURL = r.serveTools(ctx, b, input); b.toolsURL != "" {
			cfg.McpServers = append(cfg.McpServers, client.MemohToolsHTTPMCPServer(b.toolsURL, b.stableToolIdentity()))
		}
	}
	if spec.Config != nil {
		spec.Config(&cfg)
	}
	// Failing before a session exists: only Memoh's own route to release.
	fail := func(err error) (*Binding, error) {
		if b.stopTools != nil {
			b.stopTools()
		}
		close(b.settled)
		b.finish()
		return nil, err
	}
	var sess *client.Session
	if existing {
		sess, err = r.conn.RestoreSession(ctx, restore, cfg)
		if errors.Is(err, client.ErrNativeSessionGone) {
			err = external.ErrThreadUnavailable
		}
	} else {
		sess, _, err = r.conn.OpenSession(ctx, restore, cfg)
	}
	if err != nil {
		return fail(err)
	}
	b.sess = sess
	failSession := func(err error) (*Binding, error) { //nolint:contextcheck // a failed open's session closes on its own lifecycle.
		if b.retire() {
			b.teardown(false)
		}
		return nil, err
	}
	if err := h.ext.Opened(ctx, b, input); err != nil {
		return failSession(err)
	}
	b.historyLost = stored != "" && sess.ID() != stored && !transient
	b.defaultModelID = strings.TrimSpace(sess.ModelState().CurrentModelID)

	r.mu.Lock()
	select {
	case <-r.done:
		r.mu.Unlock()
		return failSession(errProcessClosed)
	default:
	}
	switch {
	case transient:
		r.transient[b] = struct{}{}
	case conversation == "":
		r.options[b.project] = b
	default:
		r.sessions[conversation] = b
	}
	r.mu.Unlock()
	h.register(b)
	return b, nil
}

// toolsMountable reports whether a session of input's conversation on r
// could be given Memoh's tools.
func (r *Process) toolsMountable(input PromptInput) bool {
	return r.host.gateway.Tools != nil && r.takesHTTPMCP() && toolmount.ResolveBaseURL(r.info, input.ToolHTTPURL) != ""
}

func (r *Process) takesHTTPMCP() bool {
	return r.conn.SupportsHTTPMCP() || r.forceHTTPMCP
}

// serveTools gives the conversation's session Memoh's tools and returns the
// URL the agent reaches them at. In a container they are served into the
// workspace on a route of the session's own; elsewhere the agent reaches
// Memoh's endpoint, which knows the session by the headers it was opened
// with. A session left without them records why.
func (r *Process) serveTools(ctx context.Context, b *Binding, input PromptInput) string {
	h := r.host
	if h.gateway.Tools == nil {
		return ""
	}
	if !r.takesHTTPMCP() {
		b.toolsMissing = "the agent does not take MCP servers over HTTP"
		return ""
	}
	baseURL := toolmount.ResolveBaseURL(r.info, input.ToolHTTPURL)
	if baseURL == "" {
		b.toolsMissing = "the workspace has no Memoh tool gateway address"
		return ""
	}
	if backend := strings.TrimSpace(r.info.Backend); backend != "" && backend != bridge.WorkspaceBackendContainer {
		return baseURL
	}
	// The route serves every later turn of the conversation, not only the
	// one that opened it; it ends with the session.
	mount, err := toolmount.Serve(context.WithoutCancel(ctx), r.bridge, r.info, input.ToolHTTPURL, h.gateway, b.toolContext)
	if err != nil {
		h.logger.WarnContext(ctx, "Memoh tool gateway unavailable; opening ACP session without Memoh tools",
			slog.String("bot_id", r.key.BotID), slog.String("bot_agent_id", r.key.BotAgentID), slog.Any("error", err))
		b.toolsMissing = "the tool bridge failed to start"
		return ""
	}
	b.stopTools = mount.Stop
	return mount.URL
}

// stableToolIdentity is the only identity baked into the session's
// configuration (MCP HTTP headers); the binding ID lives as long as the
// session.
func (b *Binding) stableToolIdentity() client.ToolSessionContext {
	return client.ToolSessionContext{
		BotID:        b.rt.key.BotID,
		ChatID:       b.rt.key.BotID,
		RuntimeID:    b.id,
		RuntimeToken: b.toolToken,
		SessionType:  sessionmode.ACPAgent,
	}
}

// toolContext resolves the trusted MCP tool context for the session: stable
// identity plus, while a prompt is running, the per-prompt fields. Between
// turns the gateway lists tools but runs none, since no run owns the call.
func (b *Binding) toolContext() mcp.ToolSessionContext {
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx := mcp.ToolSessionContext{
		BotID:            b.rt.key.BotID,
		ChatID:           b.rt.key.BotID,
		RuntimeID:        b.id,
		SessionID:        b.conversation,
		SessionType:      sessionmode.ACPAgent,
		CanListUserInput: true,
	}
	if b.active == nil {
		ctx.RequireActiveRun = true
		return ctx
	}
	ctx.RuntimeActive = true
	overlay := func(dst *string, value string) {
		if value = strings.TrimSpace(value); value != "" {
			*dst = value
		}
	}
	overlay(&ctx.ChatID, b.active.ChatID)
	overlay(&ctx.SessionID, b.active.SessionID)
	overlay(&ctx.RunID, b.active.RunID)
	overlay(&ctx.SessionType, b.active.SessionType)
	overlay(&ctx.RouteID, b.active.RouteID)
	overlay(&ctx.ChannelIdentityID, b.active.ChannelIdentityID)
	overlay(&ctx.SessionToken, b.active.SessionToken)
	overlay(&ctx.CurrentPlatform, b.active.CurrentPlatform)
	overlay(&ctx.ReplyTarget, b.active.ReplyTarget)
	overlay(&ctx.ConversationType, b.active.ConversationType)
	overlay(&ctx.ReasoningStoredEffort, b.active.ReasoningStoredEffort)
	overlay(&ctx.ReasoningRequestedEffort, b.active.ReasoningRequestedEffort)
	if b.active.CanRequestUserInput {
		ctx.CanRequestUserInput = true
	}
	if b.active.SupportsImageInput {
		ctx.SupportsImageInput = true
	}
	if b.active.ContextBudgetMaxTokens != 0 {
		ctx.ContextBudgetMaxTokens = b.active.ContextBudgetMaxTokens
	}
	if b.active.RuntimeFence.Valid() {
		ctx.RuntimeFence = b.active.RuntimeFence
	}
	ctx.RunContext = b.active.RunContext
	ctx.RuntimeGuard = b.active.RuntimeGuard
	return ctx
}
