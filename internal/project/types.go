// Package project implements team-level Project collaboration spaces: a
// Wiki doc tree and an Issues kanban sharing one node table. Everything in
// the first version is visible to every team member — there is no ACL; the
// permission phase adds relation tables later without touching this model.
package project

import (
	"errors"
	"fmt"
	"time"
)

// Node types.
const (
	NodeTypeDoc   = "doc"
	NodeTypeIssue = "issue"
)

// Issue statuses (kanban columns).
const (
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
)

// Issue priorities.
const (
	PriorityLow    = "low"
	PriorityMedium = "medium"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

var (
	ErrProjectNotFound  = errors.New("project not found")
	ErrNodeNotFound     = errors.New("node not found")
	ErrCommentNotFound  = errors.New("comment not found")
	ErrLabelNotFound    = errors.New("label not found")
	ErrVersionNotFound  = errors.New("version not found")
	ErrLinkNotFound     = errors.New("link not found")
	ErrNameRequired     = errors.New("name is required")
	ErrTitleRequired    = errors.New("title is required")
	ErrBodyRequired     = errors.New("body is required")
	ErrInvalidNodeType  = errors.New("node type must be doc or issue")
	ErrInvalidStatus    = errors.New("invalid issue status")
	ErrInvalidPriority  = errors.New("invalid issue priority")
	ErrNotAnIssue       = errors.New("node is not an issue")
	ErrNotADoc          = errors.New("node is not a doc")
	ErrParentNotFound   = errors.New("parent node not found")
	ErrParentNotDoc     = errors.New("parent node is not a doc")
	ErrIssueParent      = errors.New("issues are flat and cannot have a parent")
	ErrMoveCycle        = errors.New("cannot move a node under its own descendant")
	ErrSelfLink         = errors.New("a node cannot link to itself")
	ErrLinkTargetGone   = errors.New("link target node not found")
	ErrLabelWrongScope  = errors.New("label does not belong to this project")
	ErrNotCommentAuthor = errors.New("only the comment author can modify it")
	ErrAssigneeConflict = errors.New("assignee cannot be both a user and a bot")
)

// VersionConflictError reports a content write that lost the optimistic
// lock race. It carries the current node so the client can re-render and
// retry without another round trip.
type VersionConflictError struct {
	Current Node
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("node content changed concurrently (current version %d)", e.Current.Version)
}

// RevisionConflictError is the issue-field counterpart of
// VersionConflictError, guarding project_issue_details.revision.
type RevisionConflictError struct {
	Current IssueDetails
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("issue fields changed concurrently (current revision %d)", e.Current.Revision)
}

// Project is the collaboration space container. The issue tallies are filled
// by the list endpoint only (the card view needs them); single-project reads
// leave them at zero rather than paying for the aggregate.
type Project struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	CreatedByUserID  string    `json:"created_by_user_id,omitempty"`
	OpenIssueCount   int       `json:"open_issue_count"`
	ClosedIssueCount int       `json:"closed_issue_count"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Node is a full node row: doc or issue, with current content.
type Node struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	Type            string    `json:"type"`
	ParentID        string    `json:"parent_id,omitempty"`
	Rank            string    `json:"rank"`
	Title           string    `json:"title"`
	Body            string    `json:"body"`
	Version         int       `json:"version"`
	CreatedByUserID string    `json:"created_by_user_id,omitempty"`
	CreatedByBotID  string    `json:"created_by_bot_id,omitempty"`
	UpdatedByUserID string    `json:"updated_by_user_id,omitempty"`
	UpdatedByBotID  string    `json:"updated_by_bot_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// TreeNode is one entry of the flat doc-tree listing (no body).
type TreeNode struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Rank      string    `json:"rank"`
	Title     string    `json:"title"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IssueDetails is the 1:1 issue extension.
type IssueDetails struct {
	NodeID         string     `json:"node_id"`
	Status         string     `json:"status"`
	AssigneeUserID string     `json:"assignee_user_id,omitempty"`
	AssigneeBotID  string     `json:"assignee_bot_id,omitempty"`
	Priority       string     `json:"priority,omitempty"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	Revision       int        `json:"revision"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Issue is a kanban card: the node projection plus details and labels.
type Issue struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"project_id"`
	Rank           string     `json:"rank"`
	Title          string     `json:"title"`
	Version        int        `json:"version"`
	Status         string     `json:"status"`
	AssigneeUserID string     `json:"assignee_user_id,omitempty"`
	AssigneeBotID  string     `json:"assignee_bot_id,omitempty"`
	Priority       string     `json:"priority,omitempty"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	Revision       int        `json:"revision"`
	Labels         []Label    `json:"labels"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// NodeDetail is the single-node read: content plus whatever hangs off it.
type NodeDetail struct {
	Node   Node          `json:"node"`
	Issue  *IssueDetails `json:"issue,omitempty"`
	Labels []Label       `json:"labels"`
	Links  NodeLinks     `json:"links"`
}

// NodeLinks lists outgoing references and backlinks of a node. Targets in
// other projects are included: cross-project references are allowed.
type NodeLinks struct {
	Outgoing []LinkedNode `json:"outgoing"`
	Incoming []LinkedNode `json:"incoming"`
}

// LinkedNode is the compact reference rendering of a linked node.
type LinkedNode struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
}

// Label is a per-project label definition.
type Label struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"created_at"`
}

// Comment hangs off a node (doc or issue alike).
type Comment struct {
	ID           string    `json:"id"`
	NodeID       string    `json:"node_id"`
	AuthorUserID string    `json:"author_user_id,omitempty"`
	AuthorBotID  string    `json:"author_bot_id,omitempty"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// VersionMeta is one entry of the version history listing (no body).
type VersionMeta struct {
	NodeID       string    `json:"node_id"`
	Version      int       `json:"version"`
	Title        string    `json:"title"`
	EditorUserID string    `json:"editor_user_id,omitempty"`
	EditorBotID  string    `json:"editor_bot_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Version is a full immutable snapshot.
type Version struct {
	VersionMeta
	Body string `json:"body"`
}

// Activity is one issue-field change ("who dragged this to done").
type Activity struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	ActorUserID string    `json:"actor_user_id,omitempty"`
	ActorBotID  string    `json:"actor_bot_id,omitempty"`
	Field       string    `json:"field"`
	OldValue    string    `json:"old_value"`
	NewValue    string    `json:"new_value"`
	CreatedAt   time.Time `json:"created_at"`
}

// SearchResult is one hit of the cross-project search.
type SearchResult struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	Snippet     string    `json:"snippet"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateProjectRequest creates a collaboration space.
type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// UpdateProjectRequest patches name/description; nil means keep.
type UpdateProjectRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// CreateNodeRequest creates a doc or an issue. ParentID applies to docs
// only; Status applies to issues only (defaults to todo) so the kanban
// "new card in this column" flow is one call.
type CreateNodeRequest struct {
	Type     string  `json:"type"`
	ParentID *string `json:"parent_id"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	Status   string  `json:"status"`
}

// UpdateContentRequest writes title/body under the content optimistic
// lock. nil fields keep their current value.
type UpdateContentRequest struct {
	Title           *string `json:"title"`
	Body            *string `json:"body"`
	ExpectedVersion int     `json:"expected_version"`
}

// MoveNodeRequest re-parents and/or re-orders a doc node. Empty Rank means
// "append at the end of the new sibling group".
type MoveNodeRequest struct {
	ParentID *string `json:"parent_id"`
	Rank     string  `json:"rank"`
}

// UpdateIssueRequest patches issue fields under the revision optimistic
// lock. nil keeps the current value; for clearable fields an explicit empty
// string (or empty due_at) clears. Rank rides along so drag-to-column is
// one atomic write.
type UpdateIssueRequest struct {
	ExpectedRevision int     `json:"expected_revision"`
	Status           *string `json:"status"`
	AssigneeUserID   *string `json:"assignee_user_id"`
	AssigneeBotID    *string `json:"assignee_bot_id"`
	Priority         *string `json:"priority"`
	// RFC3339, empty string clears.
	DueAt *string `json:"due_at"`
	Rank  *string `json:"rank"`
}

// CommentRequest carries a comment body (create and edit).
type CommentRequest struct {
	Body string `json:"body"`
}

// LabelRequest creates or updates a label definition.
type LabelRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// SetNodeLabelsRequest replaces a node's label set.
type SetNodeLabelsRequest struct {
	LabelIDs []string `json:"label_ids"`
}

// LinkRequest adds a node → node reference.
type LinkRequest struct {
	TargetNodeID string `json:"target_node_id"`
}

// SearchRequest is the cross-project search input.
type SearchRequest struct {
	Query     string
	ProjectID string
	Type      string
	Limit     int
}

// safeInt32 narrows client-supplied ints for the int32 SQL params. Out of
// range values collapse to 0, which never matches a version/revision guard
// (both are >= 1), so oversized input degrades into the normal conflict or
// not-found path instead of wrapping around.
func safeInt32(v int) int32 {
	if v < 0 || v > 1<<31-1 {
		return 0
	}
	return int32(v)
}

func validStatus(s string) bool {
	switch s {
	case StatusTodo, StatusInProgress, StatusDone, StatusCancelled:
		return true
	}
	return false
}

func validPriority(p string) bool {
	switch p {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return true
	}
	return false
}
