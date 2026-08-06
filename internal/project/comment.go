package project

import (
	"context"
	"strings"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
)

// CreateComment posts a comment on a doc or issue node.
func (s *Service) CreateComment(ctx context.Context, projectID, nodeID, userID string, req CommentRequest) (Comment, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return Comment{}, ErrBodyRequired
	}
	if err := s.requireProject(ctx, projectID); err != nil {
		return Comment{}, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return Comment{}, err
	}
	row, err := s.queries.CreateProjectComment(ctx, dbsqlc.CreateProjectCommentParams{
		NodeID:       node.ID,
		AuthorUserID: db.ParseUUIDOrEmpty(userID),
		Body:         body,
	})
	if err != nil {
		return Comment{}, err
	}
	return toComment(row), nil
}

// ListComments returns the node's live comments, oldest first.
func (s *Service) ListComments(ctx context.Context, projectID, nodeID string) ([]Comment, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListProjectComments(ctx, node.ID)
	if err != nil {
		return nil, err
	}
	comments := make([]Comment, 0, len(rows))
	for _, row := range rows {
		comments = append(comments, toComment(row))
	}
	return comments, nil
}

// UpdateComment edits a comment body; author-only.
func (s *Service) UpdateComment(ctx context.Context, projectID, nodeID, commentID, userID string, req CommentRequest) (Comment, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return Comment{}, ErrBodyRequired
	}
	comment, err := s.requireOwnComment(ctx, projectID, nodeID, commentID, userID)
	if err != nil {
		return Comment{}, err
	}
	row, err := s.queries.UpdateProjectComment(ctx, dbsqlc.UpdateProjectCommentParams{
		Body:      body,
		CommentID: comment.ID,
	})
	if err != nil {
		if notFound(err) {
			return Comment{}, ErrCommentNotFound
		}
		return Comment{}, err
	}
	return toComment(row), nil
}

// DeleteComment soft-deletes a comment; author-only.
func (s *Service) DeleteComment(ctx context.Context, projectID, nodeID, commentID, userID string) error {
	comment, err := s.requireOwnComment(ctx, projectID, nodeID, commentID, userID)
	if err != nil {
		return err
	}
	rows, err := s.queries.SoftDeleteProjectComment(ctx, comment.ID)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrCommentNotFound
	}
	return nil
}

// requireOwnComment loads a live comment, checks it hangs off the given
// node in the given project, and that the caller wrote it.
func (s *Service) requireOwnComment(ctx context.Context, projectID, nodeID, commentID, userID string) (dbsqlc.ProjectComment, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return dbsqlc.ProjectComment{}, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return dbsqlc.ProjectComment{}, err
	}
	cid, err := db.ParseUUID(commentID)
	if err != nil {
		return dbsqlc.ProjectComment{}, ErrCommentNotFound
	}
	comment, err := s.queries.GetProjectComment(ctx, cid)
	if err != nil {
		if notFound(err) {
			return dbsqlc.ProjectComment{}, ErrCommentNotFound
		}
		return dbsqlc.ProjectComment{}, err
	}
	if comment.NodeID != node.ID {
		return dbsqlc.ProjectComment{}, ErrCommentNotFound
	}
	author := db.ParseUUIDOrEmpty(userID)
	if !author.Valid || !comment.AuthorUserID.Valid || comment.AuthorUserID.Bytes != author.Bytes {
		return dbsqlc.ProjectComment{}, ErrNotCommentAuthor
	}
	return comment, nil
}
