// Package workdir manages named per-bot working directories. A workdir is a
// (workspace target, absolute directory path) pair: the target says which
// machine the directory lives on (the native container workspace or a
// user-owned remote runtime), the path says where. Sessions bind to a
// workdir immutably at creation time and derive their working directory
// from it for their whole life.
//
// The UI calls these Folders; the domain, API, and database call them
// workdirs. The name Project is deliberately avoided here — it belongs to
// the Team-level collaboration container (docs, issues, resources).
package workdir

import (
	"errors"
	"time"

	"github.com/felinics/memoh/internal/workspace"
)

// Target kinds. These are the workspace target kinds on purpose: a workdir
// does not invent its own notion of location.
const (
	TargetKindNative = workspace.WorkspaceTargetNative
	TargetKindRemote = workspace.WorkspaceTargetRemote
)

var (
	ErrWorkdirNotFound  = errors.New("workdir not found")
	ErrWorkdirArchived  = errors.New("workdir is archived")
	ErrNameRequired     = errors.New("workdir name is required")
	ErrPathRequired     = errors.New("workdir path is required")
	ErrInvalidPath      = errors.New("invalid workdir path")
	ErrPathNotFound     = errors.New("workdir path does not exist")
	ErrPathNotDirectory = errors.New("workdir path is not a directory")
	ErrDuplicatePath    = errors.New("a workdir for this directory already exists")
)

// Workdir is the API shape of a workdir. WorkspaceTargetID is the derived
// target address: the native sentinel for native workdirs, the remote
// binding UUID otherwise.
type Workdir struct {
	ID                string    `json:"id"`
	BotID             string    `json:"bot_id"`
	Name              string    `json:"name"`
	TargetKind        string    `json:"target_kind"`
	WorkspaceTargetID string    `json:"workspace_target_id"`
	Path              string    `json:"path"`
	CreatedByUserID   string    `json:"created_by_user_id,omitempty"`
	Archived          bool      `json:"archived,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
} // @name workdir.Workdir

// CreateRequest creates a workdir. An empty WorkspaceTargetID means the
// native workspace: workdir targets are explicit on purpose, so they never
// drift when the bot's primary target changes.
type CreateRequest struct {
	Name              string `json:"name" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
	Path              string `json:"path" validate:"required"`
}

// UpdateRequest renames a workdir. The target and path are immutable: they
// are already baked into the working directory of every session bound to
// this workdir.
type UpdateRequest struct {
	Name string `json:"name" validate:"required"`
}

type WorkdirsResponse struct {
	Workdirs []Workdir `json:"workdirs"`
}

// Resolved is the per-session resolution of a workdir binding: which target
// the session is pinned to and the working directory tools and runtimes use.
// It reads only stored state — target liveness stays with the turn path.
type Resolved struct {
	WorkdirID string
	TargetID  string
	Kind      string
	WorkDir   string
}
