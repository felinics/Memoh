package project

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
)

// AddLink records a node → node reference. Cross-project targets are
// allowed by design.
func (s *Service) AddLink(ctx context.Context, projectID, nodeID string, req LinkRequest) error {
	targetID := strings.TrimSpace(req.TargetNodeID)
	if targetID == "" {
		return ErrLinkTargetGone
	}
	if targetID == nodeID {
		return ErrSelfLink
	}
	if err := s.requireProject(ctx, projectID); err != nil {
		return err
	}
	source, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return err
	}
	tid, err := db.ParseUUID(targetID)
	if err != nil {
		return ErrLinkTargetGone
	}
	if _, err := s.queries.GetProjectNodeByID(ctx, tid); err != nil {
		if notFound(err) {
			return ErrLinkTargetGone
		}
		return err
	}
	return s.queries.CreateProjectNodeLink(ctx, dbsqlc.CreateProjectNodeLinkParams{
		SourceNodeID: source.ID,
		TargetNodeID: tid,
	})
}

// RemoveLink deletes a node → node reference.
func (s *Service) RemoveLink(ctx context.Context, projectID, nodeID, targetNodeID string) error {
	if err := s.requireProject(ctx, projectID); err != nil {
		return err
	}
	source, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return err
	}
	tid, err := db.ParseUUID(targetNodeID)
	if err != nil {
		return ErrLinkNotFound
	}
	rows, err := s.queries.DeleteProjectNodeLink(ctx, dbsqlc.DeleteProjectNodeLinkParams{
		SourceNodeID: source.ID,
		TargetNodeID: tid,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrLinkNotFound
	}
	return nil
}

func (s *Service) nodeLinks(ctx context.Context, nodeID pgtype.UUID) (NodeLinks, error) {
	links := NodeLinks{Outgoing: []LinkedNode{}, Incoming: []LinkedNode{}}
	outgoing, err := s.queries.ListProjectNodeLinks(ctx, nodeID)
	if err != nil {
		return links, err
	}
	for _, row := range outgoing {
		links.Outgoing = append(links.Outgoing, LinkedNode{
			ID:        row.ID.String(),
			ProjectID: row.ProjectID.String(),
			Type:      row.Type,
			Title:     row.Title,
		})
	}
	incoming, err := s.queries.ListProjectNodeBacklinks(ctx, nodeID)
	if err != nil {
		return links, err
	}
	for _, row := range incoming {
		links.Incoming = append(links.Incoming, LinkedNode{
			ID:        row.ID.String(),
			ProjectID: row.ProjectID.String(),
			Type:      row.Type,
			Title:     row.Title,
		})
	}
	return links, nil
}
