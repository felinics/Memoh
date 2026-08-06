package project

import (
	"context"
	"strings"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

// CreateLabel defines a label inside the project.
func (s *Service) CreateLabel(ctx context.Context, projectID string, req LabelRequest) (Label, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Label{}, ErrNameRequired
	}
	if err := s.requireProject(ctx, projectID); err != nil {
		return Label{}, err
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return Label{}, ErrProjectNotFound
	}
	row, err := s.queries.CreateProjectLabel(ctx, dbsqlc.CreateProjectLabelParams{
		ProjectID: pid,
		Name:      name,
		Color:     strings.TrimSpace(req.Color),
	})
	if err != nil {
		return Label{}, err
	}
	return toLabel(row), nil
}

// ListLabels returns the project's label definitions.
func (s *Service) ListLabels(ctx context.Context, projectID string) ([]Label, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return nil, ErrProjectNotFound
	}
	rows, err := s.queries.ListProjectLabels(ctx, pid)
	if err != nil {
		return nil, err
	}
	labels := make([]Label, 0, len(rows))
	for _, row := range rows {
		labels = append(labels, toLabel(row))
	}
	return labels, nil
}

// UpdateLabel renames/recolors a label.
func (s *Service) UpdateLabel(ctx context.Context, projectID, labelID string, req LabelRequest) (Label, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Label{}, ErrNameRequired
	}
	if err := s.requireProject(ctx, projectID); err != nil {
		return Label{}, err
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return Label{}, ErrProjectNotFound
	}
	lid, err := db.ParseUUID(labelID)
	if err != nil {
		return Label{}, ErrLabelNotFound
	}
	row, err := s.queries.UpdateProjectLabel(ctx, dbsqlc.UpdateProjectLabelParams{
		Name:      name,
		Color:     strings.TrimSpace(req.Color),
		ProjectID: pid,
		LabelID:   lid,
	})
	if err != nil {
		if notFound(err) {
			return Label{}, ErrLabelNotFound
		}
		return Label{}, err
	}
	return toLabel(row), nil
}

// DeleteLabel removes a label definition; assignments cascade away.
func (s *Service) DeleteLabel(ctx context.Context, projectID, labelID string) error {
	if err := s.requireProject(ctx, projectID); err != nil {
		return err
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return ErrProjectNotFound
	}
	lid, err := db.ParseUUID(labelID)
	if err != nil {
		return ErrLabelNotFound
	}
	rows, err := s.queries.DeleteProjectLabel(ctx, dbsqlc.DeleteProjectLabelParams{
		ProjectID: pid,
		LabelID:   lid,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrLabelNotFound
	}
	return nil
}

// SetNodeLabels replaces the node's label set. Every label must belong to
// the node's project.
func (s *Service) SetNodeLabels(ctx context.Context, projectID, nodeID string, req SetNodeLabelsRequest) ([]Label, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return nil, err
	}
	defined, err := s.ListLabels(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Label, len(defined))
	for _, label := range defined {
		byID[label.ID] = label
	}
	selected := make([]Label, 0, len(req.LabelIDs))
	seen := make(map[string]bool, len(req.LabelIDs))
	for _, raw := range req.LabelIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		label, ok := byID[id]
		if !ok {
			return nil, ErrLabelWrongScope
		}
		seen[id] = true
		selected = append(selected, label)
	}

	err = s.inTx(ctx, func(q dbstore.Queries) error {
		if err := q.DeleteProjectNodeLabels(ctx, node.ID); err != nil {
			return err
		}
		for _, label := range selected {
			lid, err := db.ParseUUID(label.ID)
			if err != nil {
				return ErrLabelNotFound
			}
			if err := q.InsertProjectNodeLabel(ctx, dbsqlc.InsertProjectNodeLabelParams{
				NodeID:  node.ID,
				LabelID: lid,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return selected, nil
}
