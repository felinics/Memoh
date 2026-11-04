package acp

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Process is one ACP agent process. ACP lets one connection host several
// concurrent sessions, so a process serves every conversation of its Agent.
// Each conversation's session on it is a Binding.
type Process struct {
	host *Host
	key  agentproc.Key
	id   string
	// state is the extension's own state of the process.
	state        any
	forceHTTPMCP bool

	proc   *client.Process
	conn   *client.Conn
	bridge *bridge.Client
	info   bridge.WorkspaceInfo

	mu sync.Mutex
	// sessions holds the conversations' sessions; options, by project, the
	// sessions that report what a new conversation there could choose (an
	// agent offers commands and settings by its working directory);
	// transient are discuss turns' sessions, closed when their turn ends.
	sessions  map[string]*Binding
	options   map[string]*Binding
	transient map[*Binding]struct{}
	// opening serializes opening a conversation's session, so two callers
	// never open it twice.
	opening map[string]chan struct{}
	// owned holds every session on the process from the moment it is
	// created, opening, live or closing, until it is gone: the process is
	// gone only once they are.
	owned map[*Binding]struct{}

	done      chan struct{}
	closeOnce sync.Once
}

// Conn is the process's ACP connection, for an extension's own methods.
func (r *Process) Conn() *client.Conn { return r.conn }

// State is the extension's own state of the process (Started.State).
func (r *Process) State() any { return r.state }

// Info is the workspace the process runs in.
func (r *Process) Info() bridge.WorkspaceInfo { return r.info }

// Done implements agentproc.Process.
// It is the agent process's own: out of service the moment the process is
// killed or exits, before any teardown here has run.
func (r *Process) Done() <-chan struct{} { return r.proc.Done() }

// Busy implements agentproc's busy report: a process is at work while any
// of its sessions is.
func (r *Process) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.sessions {
		if b.busy.Load() {
			return true
		}
	}
	for b := range r.transient {
		if b.busy.Load() {
			return true
		}
	}
	for _, b := range r.options {
		if b.busy.Load() {
			return true
		}
	}
	return false
}

// Close implements agentproc.Process. Every session closes with it, and the
// approvals and questions their running prompts wait on: they were issued
// by this process and nothing can answer them once it is gone.
func (r *Process) Close() error {
	var err error
	r.closeOnce.Do(func() {
		// Out of service before any cleanup here: Done is the process's own.
		r.proc.Kill()
		r.mu.Lock()
		// No session opens on the process from here on (own refuses).
		close(r.done)
		bindings := make([]*Binding, 0, len(r.sessions)+len(r.transient)+len(r.options))
		for _, b := range r.sessions {
			bindings = append(bindings, b)
		}
		for b := range r.transient {
			bindings = append(bindings, b)
		}
		for _, b := range r.options {
			bindings = append(bindings, b)
		}
		r.mu.Unlock()
		for _, b := range bindings {
			b.abandon()
		}
		err = r.proc.Close()
		// The process has exited, which ends every session the agent held:
		// what is left is each session's own teardown, those still opening
		// included.
		r.mu.Lock()
		owned := make([]*Binding, 0, len(r.owned))
		for b := range r.owned {
			owned = append(owned, b)
		}
		r.mu.Unlock()
		for _, b := range owned {
			<-b.gone
		}
	})
	return err
}

// own makes b the process's own from its creation; a process closing takes
// no new session.
func (r *Process) own(b *Binding) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.done:
		return false
	default:
	}
	r.owned[b] = struct{}{}
	return true
}

func (r *Process) disown(b *Binding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.owned, b)
}

// lockOpening holds off other openers of conversation's session until the
// returned unlock.
func (r *Process) lockOpening(ctx context.Context, conversation string) (func(), error) {
	for {
		r.mu.Lock()
		wait, held := r.opening[conversation]
		if !held {
			ch := make(chan struct{})
			r.opening[conversation] = ch
			r.mu.Unlock()
			return func() {
				r.mu.Lock()
				delete(r.opening, conversation)
				r.mu.Unlock()
				close(ch)
			}, nil
		}
		r.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.done:
			return nil, errProcessClosed
		}
	}
}

// forget drops b from the process's tables, if it is still there.
func (r *Process) forget(b *Binding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[b.conversation] == b {
		delete(r.sessions, b.conversation)
	}
	delete(r.transient, b)
	if r.options[b.project] == b {
		delete(r.options, b.project)
	}
}

// startProcess launches one agent process as l says. It opens no session:
// conversations open theirs when they first use it.
func (h *Host) startProcess(ctx context.Context, l Launch) (*Process, string, error) {
	started, err := l.Start(ctx)
	if err != nil {
		return nil, "", err
	}
	r := &Process{
		host:         h,
		key:          l.Key,
		id:           "proc_" + uuid.NewString(),
		state:        started.State,
		forceHTTPMCP: started.ForceHTTPMCP,
		proc:         started.Proc,
		conn:         started.Conn,
		bridge:       started.Bridge,
		info:         l.Info,
		sessions:     map[string]*Binding{},
		options:      map[string]*Binding{},
		transient:    map[*Binding]struct{}{},
		opening:      map[string]chan struct{}{},
		owned:        map[*Binding]struct{}{},
		done:         make(chan struct{}),
	}
	proc, conn := started.Proc, started.Conn
	// The connection can end while the agent keeps running: the ACP client
	// gives it up when a line is too long or its queue overflows. Every later
	// request would then fail on a process still handed out, so either end
	// takes the other down.
	go func() { //nolint:contextcheck // teardown follows the process, not the request that started it.
		select {
		case <-proc.Done():
		case <-conn.Done():
		case <-r.done:
			return
		}
		h.logger.Info("ACP agent process ended", slog.String("bot_id", l.Key.BotID),
			slog.String("bot_agent_id", l.Key.BotAgentID),
			slog.String("stderr", proc.StderrTail()))
		_ = r.Close()
	}()
	return r, started.Fingerprint, nil
}
