// Package project manages named per-bot project directories. A project is a
// (workspace target, absolute directory path) pair: the target says which
// machine the directory lives on (the native container workspace or a
// user-owned remote runtime), the path says where. Sessions bind to a
// project immutably at creation time and derive their working directory
// from it for their whole life.
package project

import (
	"errors"
	"time"

	"github.com/memohai/memoh/internal/workspace"
)

// Target kinds. These are the workspace target kinds on purpose: a project
// does not invent its own notion of location.
const (
	TargetKindNative = workspace.WorkspaceTargetNative
	TargetKindRemote = workspace.WorkspaceTargetRemote
)

var (
	ErrProjectNotFound  = errors.New("project not found")
	ErrProjectArchived  = errors.New("project is archived")
	ErrNameRequired     = errors.New("project name is required")
	ErrPathRequired     = errors.New("project path is required")
	ErrInvalidPath      = errors.New("invalid project path")
	ErrPathNotFound     = errors.New("project directory does not exist")
	ErrPathNotDirectory = errors.New("project path is not a directory")
	ErrDuplicatePath    = errors.New("a project for this directory already exists")
)

// Project is the API shape of a project. WorkspaceTargetID is the derived
// target address: the native sentinel for native projects, the remote
// binding UUID otherwise.
type Project struct {
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
} // @name project.Project

// CreateRequest creates a project. An empty WorkspaceTargetID means the
// native workspace: project targets are explicit on purpose, so they never
// drift when the bot's primary target changes.
type CreateRequest struct {
	Name              string `json:"name" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id,omitempty"`
	Path              string `json:"path" validate:"required"`
}

// UpdateRequest renames a project. The target and path are immutable: they
// are already baked into the working directory of every session bound to
// this project.
type UpdateRequest struct {
	Name string `json:"name" validate:"required"`
}

type ProjectsResponse struct {
	Projects []Project `json:"projects"`
}

// Resolved is the per-session resolution of a project binding: which target
// the session is pinned to and the working directory tools and runtimes use.
// It reads only stored state — target liveness stays with the turn path.
type Resolved struct {
	ProjectID string
	TargetID  string
	Kind      string
	WorkDir   string
}
