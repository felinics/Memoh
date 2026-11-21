package acp

import (
	"context"

	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Extension is what one kind of ACP agent adds to the core. The core runs
// processes, sessions and turns as ACP defines them; an extension says how
// its agent's process starts, which session a conversation continues, and
// what the agent's sessions and turns carry beyond ACP.
type Extension interface {
	// Launch resolves the Agent's process, which serves all of its
	// conversations and its own work outside any.
	Launch(ctx context.Context, input PromptInput) (Launch, error)
	// Session resolves how input's conversation opens its session on proc.
	Session(ctx context.Context, proc *Process, input PromptInput) (SessionSpec, error)
	// Opened prepares a newly opened session before it serves anyone.
	Opened(ctx context.Context, b *Binding, input PromptInput) error
	// Configure applies the turn's selections to the session before its
	// prompt. A refused selection (isPromptConfigSelectionError) leaves the
	// session as it was; any other failure leaves its state unknown, and the
	// core opens it again for the next turn.
	Configure(ctx context.Context, b *Binding, input PromptInput) error
}

// Launch is everything an agent process starts from, resolved before a
// lease.
type Launch struct {
	Key agentproc.Key
	// Fingerprint covers every input of the start: a process started from
	// another is replaced on the next lease.
	Fingerprint string
	Info        bridge.WorkspaceInfo
	// Start launches the process and completes the ACP handshake.
	Start func(ctx context.Context) (Started, error)
}

// Started is a launched agent process.
type Started struct {
	Proc   *client.Process
	Conn   *client.Conn
	Bridge *bridge.Client
	// State is the extension's own state of the process, read back with
	// Process.State.
	State any
	// ForceHTTPMCP gives the sessions Memoh's tools over HTTP MCP though the
	// agent did not declare it takes them.
	ForceHTTPMCP bool
	// Fingerprint, when set, names what the process turned out to start
	// from (the version it reported, say) in place of Launch.Fingerprint.
	Fingerprint string
}

// SessionSpec is how a conversation's session opens.
type SessionSpec struct {
	// ProjectPath is the conversation's project; empty for the workspace
	// default.
	ProjectPath string
	// Variant is everything else the session opens under that ACP keeps
	// for the session's life. A live session opened under another variant
	// is opened again.
	Variant string
	// Stored is the conversation's stored native session.
	Stored StoredSession
	// Config completes the session's configuration with what the agent
	// reads beyond ACP.
	Config func(*client.SessionConfig)
}

// StoredSession is a conversation's stored native session: its ID, whether
// the conversation owns it (a fork or a restored backup copies the ID of
// another), and whether there is a record to say (without one the live
// session is all there is).
type StoredSession struct {
	ID    string
	Owned bool
	Known bool
}
