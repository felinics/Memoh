package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/project"
)

// ProjectHandler exposes team-level Project collaboration spaces (Wiki doc
// tree + Issues kanban). First version has no per-project ACL: any
// authenticated team member can read and write.
type ProjectHandler struct {
	log     *slog.Logger
	service *project.Service
}

func NewProjectHandler(log *slog.Logger, service *project.Service) *ProjectHandler {
	if log == nil {
		log = slog.Default()
	}
	return &ProjectHandler{
		log:     log.With(slog.String("handler", "projects")),
		service: service,
	}
}

func (h *ProjectHandler) Register(e *echo.Echo) {
	g := e.Group("/projects")
	g.POST("", h.CreateProject)
	g.GET("", h.ListProjects)
	g.GET("/search", h.Search)
	g.GET("/:project_id", h.GetProject)
	g.PATCH("/:project_id", h.UpdateProject)
	g.DELETE("/:project_id", h.DeleteProject)

	g.GET("/:project_id/tree", h.Tree)
	g.GET("/:project_id/issues", h.Board)

	g.POST("/:project_id/labels", h.CreateLabel)
	g.GET("/:project_id/labels", h.ListLabels)
	g.PATCH("/:project_id/labels/:label_id", h.UpdateLabel)
	g.DELETE("/:project_id/labels/:label_id", h.DeleteLabel)

	g.POST("/:project_id/nodes", h.CreateNode)
	g.GET("/:project_id/nodes/:node_id", h.GetNode)
	g.PATCH("/:project_id/nodes/:node_id", h.UpdateContent)
	g.POST("/:project_id/nodes/:node_id/move", h.MoveNode)
	g.DELETE("/:project_id/nodes/:node_id", h.DeleteNode)
	g.PATCH("/:project_id/nodes/:node_id/issue", h.UpdateIssue)
	g.GET("/:project_id/nodes/:node_id/activity", h.Activity)
	g.PUT("/:project_id/nodes/:node_id/labels", h.SetNodeLabels)
	g.GET("/:project_id/nodes/:node_id/versions", h.Versions)
	g.GET("/:project_id/nodes/:node_id/versions/:version", h.GetVersion)
	g.GET("/:project_id/nodes/:node_id/comments", h.ListComments)
	g.POST("/:project_id/nodes/:node_id/comments", h.CreateComment)
	g.PATCH("/:project_id/nodes/:node_id/comments/:comment_id", h.UpdateComment)
	g.DELETE("/:project_id/nodes/:node_id/comments/:comment_id", h.DeleteComment)
	g.POST("/:project_id/nodes/:node_id/links", h.AddLink)
	g.DELETE("/:project_id/nodes/:node_id/links/:target_node_id", h.RemoveLink)
}

// conflictResponse is the 409 payload: the current server-side state so
// the client can re-render and retry without another round trip.
type conflictResponse struct {
	Error   string                `json:"error"`
	Node    *project.Node         `json:"node,omitempty"`
	Issue   *project.IssueDetails `json:"issue,omitempty"`
	Version int                   `json:"current_version,omitempty"`
}

// projectHTTPError maps domain errors onto HTTP statuses.
func projectHTTPError(c echo.Context, err error) error {
	var versionConflict *project.VersionConflictError
	if errors.As(err, &versionConflict) {
		return c.JSON(http.StatusConflict, conflictResponse{
			Error:   versionConflict.Error(),
			Node:    &versionConflict.Current,
			Version: versionConflict.Current.Version,
		})
	}
	var revisionConflict *project.RevisionConflictError
	if errors.As(err, &revisionConflict) {
		return c.JSON(http.StatusConflict, conflictResponse{
			Error:   revisionConflict.Error(),
			Issue:   &revisionConflict.Current,
			Version: revisionConflict.Current.Revision,
		})
	}
	switch {
	case errors.Is(err, project.ErrProjectNotFound),
		errors.Is(err, project.ErrNodeNotFound),
		errors.Is(err, project.ErrCommentNotFound),
		errors.Is(err, project.ErrLabelNotFound),
		errors.Is(err, project.ErrVersionNotFound),
		errors.Is(err, project.ErrLinkNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, project.ErrNotCommentAuthor):
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	case errors.Is(err, project.ErrNameRequired),
		errors.Is(err, project.ErrTitleRequired),
		errors.Is(err, project.ErrBodyRequired),
		errors.Is(err, project.ErrInvalidNodeType),
		errors.Is(err, project.ErrInvalidStatus),
		errors.Is(err, project.ErrInvalidPriority),
		errors.Is(err, project.ErrNotAnIssue),
		errors.Is(err, project.ErrNotADoc),
		errors.Is(err, project.ErrParentNotFound),
		errors.Is(err, project.ErrParentNotDoc),
		errors.Is(err, project.ErrIssueParent),
		errors.Is(err, project.ErrMoveCycle),
		errors.Is(err, project.ErrSelfLink),
		errors.Is(err, project.ErrLinkTargetGone),
		errors.Is(err, project.ErrLabelWrongScope),
		errors.Is(err, project.ErrAssigneeConflict):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}

func projectParam(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("project_id"))
	if id == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	return id, nil
}

func nodeParam(c echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("node_id"))
	if id == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "node_id is required")
	}
	return id, nil
}

// CreateProject godoc
// @Summary Create a project
// @Description Creates a team-level collaboration space holding a Wiki doc tree and an Issues kanban.
// @Tags projects
// @Accept json
// @Produce json
// @Param request body project.CreateProjectRequest true "Project"
// @Success 201 {object} project.Project
// @Failure 400 {object} ErrorResponse
// @Router /projects [post].
func (h *ProjectHandler) CreateProject(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	var req project.CreateProjectRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	created, err := h.service.CreateProject(c.Request().Context(), userID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusCreated, created)
}

// ListProjects godoc
// @Summary List projects
// @Tags projects
// @Produce json
// @Success 200 {array} project.Project
// @Router /projects [get].
func (h *ProjectHandler) ListProjects(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projects, err := h.service.ListProjects(c.Request().Context())
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, projects)
}

// GetProject godoc
// @Summary Get a project
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Success 200 {object} project.Project
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id} [get].
func (h *ProjectHandler) GetProject(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	p, err := h.service.GetProject(c.Request().Context(), projectID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, p)
}

// UpdateProject godoc
// @Summary Update a project
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param request body project.UpdateProjectRequest true "Patch"
// @Success 200 {object} project.Project
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id} [patch].
func (h *ProjectHandler) UpdateProject(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	var req project.UpdateProjectRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	updated, err := h.service.UpdateProject(c.Request().Context(), projectID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, updated)
}

// DeleteProject godoc
// @Summary Delete a project
// @Description Soft-deletes the project; its nodes become unreachable with it.
// @Tags projects
// @Param project_id path string true "Project ID"
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id} [delete].
func (h *ProjectHandler) DeleteProject(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	if err := h.service.DeleteProject(c.Request().Context(), projectID); err != nil {
		return projectHTTPError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Tree godoc
// @Summary List the doc tree
// @Description Flat listing of live doc nodes (no bodies); the client assembles the hierarchy from parent_id + rank.
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Success 200 {array} project.TreeNode
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/tree [get].
func (h *ProjectHandler) Tree(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodes, err := h.service.Tree(c.Request().Context(), projectID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, nodes)
}

// Board godoc
// @Summary List the kanban board
// @Description Every live issue with details and labels; the client groups by status.
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Success 200 {array} project.Issue
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/issues [get].
func (h *ProjectHandler) Board(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	issues, err := h.service.Board(c.Request().Context(), projectID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, issues)
}

// CreateNode godoc
// @Summary Create a doc or issue node
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param request body project.CreateNodeRequest true "Node"
// @Success 201 {object} project.NodeDetail
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes [post].
func (h *ProjectHandler) CreateNode(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	var req project.CreateNodeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	detail, err := h.service.CreateNode(c.Request().Context(), projectID, userID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusCreated, detail)
}

// GetNode godoc
// @Summary Get a node
// @Description Full node content plus issue details, labels and links.
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Success 200 {object} project.NodeDetail
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id} [get].
func (h *ProjectHandler) GetNode(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	detail, err := h.service.GetNode(c.Request().Context(), projectID, nodeID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, detail)
}

// UpdateContent godoc
// @Summary Update node title/body
// @Description Optimistic-locked content write. A stale expected_version returns 409 with the current node.
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param request body project.UpdateContentRequest true "Content patch"
// @Success 200 {object} project.Node
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id} [patch].
func (h *ProjectHandler) UpdateContent(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	var req project.UpdateContentRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	node, err := h.service.UpdateContent(c.Request().Context(), projectID, nodeID, userID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, node)
}

// MoveNode godoc
// @Summary Move a doc node
// @Description Re-parents and/or re-orders inside the doc tree. Cycles and cross-type parents are rejected.
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param request body project.MoveNodeRequest true "Move"
// @Success 200 {object} project.Node
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/move [post].
func (h *ProjectHandler) MoveNode(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	var req project.MoveNodeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	node, err := h.service.MoveNode(c.Request().Context(), projectID, nodeID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, node)
}

// DeleteNode godoc
// @Summary Delete a node
// @Description Soft-deletes the node and its whole subtree.
// @Tags projects
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id} [delete].
func (h *ProjectHandler) DeleteNode(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	if err := h.service.DeleteNode(c.Request().Context(), projectID, nodeID); err != nil {
		return projectHTTPError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// UpdateIssue godoc
// @Summary Update issue fields
// @Description Optimistic-locked issue-field write (status/assignee/priority/due_at) with the kanban rank riding along, so dragging a card to another column is one atomic call. A stale expected_revision returns 409 with the current fields.
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param request body project.UpdateIssueRequest true "Issue patch"
// @Success 200 {object} project.IssueDetails
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/issue [patch].
func (h *ProjectHandler) UpdateIssue(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	var req project.UpdateIssueRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	details, err := h.service.UpdateIssue(c.Request().Context(), projectID, nodeID, userID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, details)
}

// Activity godoc
// @Summary List issue activity
// @Description Field-change history ("who dragged this to done"), oldest first.
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Success 200 {array} project.Activity
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/activity [get].
func (h *ProjectHandler) Activity(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	activity, err := h.service.Activity(c.Request().Context(), projectID, nodeID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, activity)
}

// Versions godoc
// @Summary List version history
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Success 200 {array} project.VersionMeta
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/versions [get].
func (h *ProjectHandler) Versions(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	versions, err := h.service.Versions(c.Request().Context(), projectID, nodeID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, versions)
}

// GetVersion godoc
// @Summary Get one version snapshot
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param version path int true "Version number"
// @Success 200 {object} project.Version
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/versions/{version} [get].
func (h *ProjectHandler) GetVersion(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	version, err := strconv.Atoi(strings.TrimSpace(c.Param("version")))
	if err != nil || version < 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "version must be a positive integer")
	}
	snapshot, err := h.service.GetVersion(c.Request().Context(), projectID, nodeID, version)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, snapshot)
}

// ListComments godoc
// @Summary List comments
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Success 200 {array} project.Comment
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/comments [get].
func (h *ProjectHandler) ListComments(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	comments, err := h.service.ListComments(c.Request().Context(), projectID, nodeID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, comments)
}

// CreateComment godoc
// @Summary Post a comment
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param request body project.CommentRequest true "Comment"
// @Success 201 {object} project.Comment
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/comments [post].
func (h *ProjectHandler) CreateComment(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	var req project.CommentRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	comment, err := h.service.CreateComment(c.Request().Context(), projectID, nodeID, userID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusCreated, comment)
}

// UpdateComment godoc
// @Summary Edit a comment
// @Description Author-only.
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param comment_id path string true "Comment ID"
// @Param request body project.CommentRequest true "Comment"
// @Success 200 {object} project.Comment
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/comments/{comment_id} [patch].
func (h *ProjectHandler) UpdateComment(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	commentID := strings.TrimSpace(c.Param("comment_id"))
	if commentID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "comment_id is required")
	}
	var req project.CommentRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	comment, err := h.service.UpdateComment(c.Request().Context(), projectID, nodeID, commentID, userID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, comment)
}

// DeleteComment godoc
// @Summary Delete a comment
// @Description Author-only soft delete.
// @Tags projects
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param comment_id path string true "Comment ID"
// @Success 204
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/comments/{comment_id} [delete].
func (h *ProjectHandler) DeleteComment(c echo.Context) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	commentID := strings.TrimSpace(c.Param("comment_id"))
	if commentID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "comment_id is required")
	}
	if err := h.service.DeleteComment(c.Request().Context(), projectID, nodeID, commentID, userID); err != nil {
		return projectHTTPError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// CreateLabel godoc
// @Summary Create a label
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param request body project.LabelRequest true "Label"
// @Success 201 {object} project.Label
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/labels [post].
func (h *ProjectHandler) CreateLabel(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	var req project.LabelRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	label, err := h.service.CreateLabel(c.Request().Context(), projectID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusCreated, label)
}

// ListLabels godoc
// @Summary List labels
// @Tags projects
// @Produce json
// @Param project_id path string true "Project ID"
// @Success 200 {array} project.Label
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/labels [get].
func (h *ProjectHandler) ListLabels(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	labels, err := h.service.ListLabels(c.Request().Context(), projectID)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, labels)
}

// UpdateLabel godoc
// @Summary Update a label
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param label_id path string true "Label ID"
// @Param request body project.LabelRequest true "Label"
// @Success 200 {object} project.Label
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/labels/{label_id} [patch].
func (h *ProjectHandler) UpdateLabel(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	labelID := strings.TrimSpace(c.Param("label_id"))
	if labelID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "label_id is required")
	}
	var req project.LabelRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	label, err := h.service.UpdateLabel(c.Request().Context(), projectID, labelID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, label)
}

// DeleteLabel godoc
// @Summary Delete a label
// @Description Removes the definition; node assignments cascade away.
// @Tags projects
// @Param project_id path string true "Project ID"
// @Param label_id path string true "Label ID"
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/labels/{label_id} [delete].
func (h *ProjectHandler) DeleteLabel(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	labelID := strings.TrimSpace(c.Param("label_id"))
	if labelID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "label_id is required")
	}
	if err := h.service.DeleteLabel(c.Request().Context(), projectID, labelID); err != nil {
		return projectHTTPError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// SetNodeLabels godoc
// @Summary Replace a node's labels
// @Tags projects
// @Accept json
// @Produce json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param request body project.SetNodeLabelsRequest true "Label IDs"
// @Success 200 {array} project.Label
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/labels [put].
func (h *ProjectHandler) SetNodeLabels(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	var req project.SetNodeLabelsRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	labels, err := h.service.SetNodeLabels(c.Request().Context(), projectID, nodeID, req)
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, labels)
}

// AddLink godoc
// @Summary Add a node link
// @Description Records a node → node reference; cross-project targets are allowed.
// @Tags projects
// @Accept json
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param request body project.LinkRequest true "Target"
// @Success 204
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/links [post].
func (h *ProjectHandler) AddLink(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	var req project.LinkRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.service.AddLink(c.Request().Context(), projectID, nodeID, req); err != nil {
		return projectHTTPError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// RemoveLink godoc
// @Summary Remove a node link
// @Tags projects
// @Param project_id path string true "Project ID"
// @Param node_id path string true "Node ID"
// @Param target_node_id path string true "Target node ID"
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Router /projects/{project_id}/nodes/{node_id}/links/{target_node_id} [delete].
func (h *ProjectHandler) RemoveLink(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	projectID, err := projectParam(c)
	if err != nil {
		return err
	}
	nodeID, err := nodeParam(c)
	if err != nil {
		return err
	}
	targetID := strings.TrimSpace(c.Param("target_node_id"))
	if targetID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "target_node_id is required")
	}
	if err := h.service.RemoveLink(c.Request().Context(), projectID, nodeID, targetID); err != nil {
		return projectHTTPError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Search godoc
// @Summary Search across projects
// @Description Substring search over titles and bodies of docs and issues.
// @Tags projects
// @Produce json
// @Param q query string true "Query"
// @Param project_id query string false "Restrict to one project"
// @Param type query string false "doc or issue"
// @Param limit query int false "Max results (default 50, cap 100)"
// @Success 200 {array} project.SearchResult
// @Failure 400 {object} ErrorResponse
// @Router /projects/search [get].
func (h *ProjectHandler) Search(c echo.Context) error {
	if _, err := RequireChannelIdentityID(c); err != nil {
		return err
	}
	limit := 0
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			limit = v
		}
	}
	results, err := h.service.Search(c.Request().Context(), project.SearchRequest{
		Query:     c.QueryParam("q"),
		ProjectID: strings.TrimSpace(c.QueryParam("project_id")),
		Type:      strings.TrimSpace(c.QueryParam("type")),
		Limit:     limit,
	})
	if err != nil {
		return projectHTTPError(c, err)
	}
	return c.JSON(http.StatusOK, results)
}
