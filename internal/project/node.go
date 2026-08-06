package project

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

// maxTreeDepth bounds the ancestor walk in cycle detection. Any real doc
// tree is a handful of levels deep; hitting this means corrupted data, and
// failing closed beats walking a loop forever.
const maxTreeDepth = 10000

// Tree returns the flat live doc-node listing; the client assembles the
// hierarchy from parent_id + rank.
func (s *Service) Tree(ctx context.Context, projectID string) ([]TreeNode, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return nil, ErrProjectNotFound
	}
	rows, err := s.queries.ListProjectDocNodes(ctx, pid)
	if err != nil {
		return nil, err
	}
	nodes := make([]TreeNode, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, toTreeNode(row))
	}
	return nodes, nil
}

// CreateNode creates a doc (optionally under a parent) or an issue
// (optionally straight into a kanban column). The initial version snapshot
// and, for issues, the details row commit atomically with the node.
func (s *Service) CreateNode(ctx context.Context, projectID, userID string, req CreateNodeRequest) (NodeDetail, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return NodeDetail{}, ErrTitleRequired
	}
	if req.Type != NodeTypeDoc && req.Type != NodeTypeIssue {
		return NodeDetail{}, ErrInvalidNodeType
	}
	status := StatusTodo
	if req.Type == NodeTypeIssue {
		if req.ParentID != nil && strings.TrimSpace(*req.ParentID) != "" {
			return NodeDetail{}, ErrIssueParent
		}
		if strings.TrimSpace(req.Status) != "" {
			status = strings.TrimSpace(req.Status)
			if !validStatus(status) {
				return NodeDetail{}, ErrInvalidStatus
			}
		}
	}
	if err := s.requireProject(ctx, projectID); err != nil {
		return NodeDetail{}, err
	}

	var parentID pgtype.UUID
	if req.Type == NodeTypeDoc && req.ParentID != nil && strings.TrimSpace(*req.ParentID) != "" {
		parent, err := s.getNodeRow(ctx, s.queries, projectID, *req.ParentID)
		if err != nil {
			return NodeDetail{}, ErrParentNotFound
		}
		if parent.Type != NodeTypeDoc {
			return NodeDetail{}, ErrParentNotDoc
		}
		parentID = parent.ID
	}

	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return NodeDetail{}, ErrProjectNotFound
	}
	userUUID := db.ParseUUIDOrEmpty(userID)

	var detail NodeDetail
	err = s.inTx(ctx, func(q dbstore.Queries) error {
		var maxRank string
		var err error
		if req.Type == NodeTypeDoc {
			maxRank, err = q.MaxProjectDocSiblingRank(ctx, dbsqlc.MaxProjectDocSiblingRankParams{
				ProjectID: pid,
				ParentID:  parentID,
			})
		} else {
			maxRank, err = q.MaxProjectIssueRank(ctx, dbsqlc.MaxProjectIssueRankParams{
				ProjectID: pid,
				Status:    status,
			})
		}
		if err != nil {
			return err
		}
		rank, err := rankAfter(maxRank)
		if err != nil {
			return err
		}

		node, err := q.CreateProjectNode(ctx, dbsqlc.CreateProjectNodeParams{
			ProjectID:       pid,
			Type:            req.Type,
			ParentID:        parentID,
			Rank:            rank,
			Title:           title,
			Body:            req.Body,
			CreatedByUserID: userUUID,
			UpdatedByUserID: userUUID,
		})
		if err != nil {
			return err
		}
		if err := q.InsertProjectNodeVersion(ctx, dbsqlc.InsertProjectNodeVersionParams{
			NodeID:       node.ID,
			Version:      node.Version,
			Title:        node.Title,
			Body:         node.Body,
			EditorUserID: userUUID,
		}); err != nil {
			return err
		}
		detail = NodeDetail{Node: toNode(node), Labels: []Label{}}
		if req.Type == NodeTypeIssue {
			details, err := q.CreateProjectIssueDetails(ctx, dbsqlc.CreateProjectIssueDetailsParams{
				NodeID: node.ID,
				Status: status,
			})
			if err != nil {
				return err
			}
			issue := toIssueDetails(details)
			detail.Issue = &issue
		}
		return nil
	})
	if err != nil {
		return NodeDetail{}, err
	}
	return detail, nil
}

// GetNode returns one node with its issue details, labels and links.
func (s *Service) GetNode(ctx context.Context, projectID, nodeID string) (NodeDetail, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return NodeDetail{}, err
	}
	row, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return NodeDetail{}, err
	}
	detail := NodeDetail{Node: toNode(row), Labels: []Label{}}

	if row.Type == NodeTypeIssue {
		details, err := s.queries.GetProjectIssueDetails(ctx, row.ID)
		if err != nil && !notFound(err) {
			return NodeDetail{}, err
		}
		if err == nil {
			issue := toIssueDetails(details)
			detail.Issue = &issue
		}
	}

	labelRows, err := s.queries.ListProjectNodeLabelsForNode(ctx, row.ID)
	if err != nil {
		return NodeDetail{}, err
	}
	for _, l := range labelRows {
		detail.Labels = append(detail.Labels, toLabel(l))
	}

	links, err := s.nodeLinks(ctx, row.ID)
	if err != nil {
		return NodeDetail{}, err
	}
	detail.Links = links
	return detail, nil
}

// UpdateContent writes title/body under the content optimistic lock and
// lands the snapshot — merged into the newest one inside the merge window,
// appended as a new immutable row otherwise.
func (s *Service) UpdateContent(ctx context.Context, projectID, nodeID, userID string, req UpdateContentRequest) (Node, error) {
	if req.Title == nil && req.Body == nil {
		return Node{}, ErrTitleRequired
	}
	if err := s.requireProject(ctx, projectID); err != nil {
		return Node{}, err
	}
	current, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return Node{}, err
	}
	title := current.Title
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
		if title == "" {
			return Node{}, ErrTitleRequired
		}
	}
	body := current.Body
	if req.Body != nil {
		body = *req.Body
	}
	userUUID := db.ParseUUIDOrEmpty(userID)

	var updated dbsqlc.ProjectNode
	err = s.inTx(ctx, func(q dbstore.Queries) error {
		var err error
		updated, err = q.UpdateProjectNodeContent(ctx, dbsqlc.UpdateProjectNodeContentParams{
			Title:           title,
			Body:            body,
			UpdatedByUserID: userUUID,
			ProjectID:       current.ProjectID,
			NodeID:          current.ID,
			ExpectedVersion: safeInt32(req.ExpectedVersion),
		})
		if err != nil {
			if notFound(err) {
				return s.contentConflict(ctx, q, projectID, nodeID)
			}
			return err
		}
		return s.landSnapshot(ctx, q, updated, req.ExpectedVersion, userUUID)
	})
	if err != nil {
		return Node{}, err
	}
	return toNode(updated), nil
}

// contentConflict distinguishes "node gone" from "version raced" after a
// guarded update matched zero rows.
func (s *Service) contentConflict(ctx context.Context, q dbstore.Queries, projectID, nodeID string) error {
	row, err := s.getNodeRow(ctx, q, projectID, nodeID)
	if err != nil {
		return err
	}
	return &VersionConflictError{Current: toNode(row)}
}

// landSnapshot merges into the newest snapshot when the same editor keeps
// saving inside the merge window, and appends a new immutable row
// otherwise. Runs inside the content-update transaction: the row lock from
// the guarded UPDATE serializes concurrent editors.
func (s *Service) landSnapshot(ctx context.Context, q dbstore.Queries, node dbsqlc.ProjectNode, expectedVersion int, editor pgtype.UUID) error {
	latest, err := q.GetLatestProjectNodeVersion(ctx, node.ID)
	if err != nil && !notFound(err) {
		return err
	}
	if err == nil &&
		int(latest.Version) == expectedVersion &&
		latest.EditorUserID.Valid == editor.Valid && latest.EditorUserID.Bytes == editor.Bytes &&
		!latest.EditorBotID.Valid &&
		s.now().Sub(db.TimeFromPg(latest.UpdatedAt)) < s.mergeWindow {
		_, err := q.RenumberProjectNodeVersion(ctx, dbsqlc.RenumberProjectNodeVersionParams{
			NewVersion: node.Version,
			Title:      node.Title,
			Body:       node.Body,
			NodeID:     node.ID,
			OldVersion: latest.Version,
		})
		return err
	}
	return q.InsertProjectNodeVersion(ctx, dbsqlc.InsertProjectNodeVersionParams{
		NodeID:       node.ID,
		Version:      node.Version,
		Title:        node.Title,
		Body:         node.Body,
		EditorUserID: editor,
	})
}

// MoveNode re-parents and/or re-orders a doc node. The per-project tree
// lock makes the cycle walk race-free against concurrent moves.
func (s *Service) MoveNode(ctx context.Context, projectID, nodeID string, req MoveNodeRequest) (Node, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return Node{}, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return Node{}, err
	}
	if node.Type != NodeTypeDoc {
		return Node{}, ErrNotADoc
	}

	newParent := ""
	if req.ParentID != nil {
		newParent = strings.TrimSpace(*req.ParentID)
	}

	var moved dbsqlc.ProjectNode
	err = s.inTx(ctx, func(q dbstore.Queries) error {
		if err := q.AcquireProjectTreeLock(ctx, projectID); err != nil {
			return err
		}
		var parentID pgtype.UUID
		if newParent != "" {
			if newParent == nodeID {
				return ErrMoveCycle
			}
			parent, err := s.getNodeRow(ctx, q, projectID, newParent)
			if err != nil {
				return ErrParentNotFound
			}
			if parent.Type != NodeTypeDoc {
				return ErrParentNotDoc
			}
			if err := s.ensureNoCycle(ctx, q, nodeID, newParent); err != nil {
				return err
			}
			parentID = parent.ID
		}

		rank := strings.TrimSpace(req.Rank)
		if rank == "" {
			maxRank, err := q.MaxProjectDocSiblingRank(ctx, dbsqlc.MaxProjectDocSiblingRankParams{
				ProjectID: node.ProjectID,
				ParentID:  parentID,
			})
			if err != nil {
				return err
			}
			if rank, err = rankAfter(maxRank); err != nil {
				return err
			}
		}

		var err error
		moved, err = q.MoveProjectNode(ctx, dbsqlc.MoveProjectNodeParams{
			ParentID:  parentID,
			Rank:      rank,
			ProjectID: node.ProjectID,
			NodeID:    node.ID,
		})
		if err != nil {
			if notFound(err) {
				return ErrNodeNotFound
			}
			return err
		}
		if rankNeedsRebalance(rank) {
			return s.rebalanceDocSiblings(ctx, q, node.ProjectID, parentID)
		}
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return toNode(moved), nil
}

// ensureNoCycle walks from the candidate parent up to the root; meeting
// the moving node on the way means the move would create a loop.
// Soft-deleted ancestors participate — they still hang off the tree.
func (*Service) ensureNoCycle(ctx context.Context, q dbstore.Queries, movingID, candidateParentID string) error {
	cursor, err := db.ParseUUID(candidateParentID)
	if err != nil {
		return ErrParentNotFound
	}
	moving, err := db.ParseUUID(movingID)
	if err != nil {
		return ErrNodeNotFound
	}
	for range maxTreeDepth {
		row, err := q.GetProjectNodeParent(ctx, cursor)
		if err != nil {
			if notFound(err) {
				return nil
			}
			return err
		}
		if row.ID == moving {
			return ErrMoveCycle
		}
		if !row.ParentID.Valid {
			return nil
		}
		cursor = row.ParentID
	}
	return ErrMoveCycle
}

// rebalanceDocSiblings re-spreads one degenerated sibling group with
// short, evenly spaced keys. Runs under the project tree lock.
func (*Service) rebalanceDocSiblings(ctx context.Context, q dbstore.Queries, projectID, parentID pgtype.UUID) error {
	siblings, err := q.ListProjectDocSiblingRanks(ctx, dbsqlc.ListProjectDocSiblingRanksParams{
		ProjectID: projectID,
		ParentID:  parentID,
	})
	if err != nil {
		return err
	}
	ranks := rebalancedRanks(len(siblings))
	for i, sibling := range siblings {
		if _, err := q.UpdateProjectNodeRank(ctx, dbsqlc.UpdateProjectNodeRankParams{
			Rank:   ranks[i],
			NodeID: sibling.ID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteNode soft-deletes the node and its whole subtree.
func (s *Service) DeleteNode(ctx context.Context, projectID, nodeID string) error {
	if err := s.requireProject(ctx, projectID); err != nil {
		return err
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return ErrProjectNotFound
	}
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return ErrNodeNotFound
	}
	return s.inTx(ctx, func(q dbstore.Queries) error {
		if err := q.AcquireProjectTreeLock(ctx, projectID); err != nil {
			return err
		}
		rows, err := q.SoftDeleteProjectNodeSubtree(ctx, dbsqlc.SoftDeleteProjectNodeSubtreeParams{
			ProjectID: pid,
			NodeID:    nid,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return ErrNodeNotFound
		}
		return nil
	})
}

// Versions lists the snapshot history metadata, newest first.
func (s *Service) Versions(ctx context.Context, projectID, nodeID string) ([]VersionMeta, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListProjectNodeVersions(ctx, node.ID)
	if err != nil {
		return nil, err
	}
	versions := make([]VersionMeta, 0, len(rows))
	for _, row := range rows {
		versions = append(versions, toVersionMeta(row))
	}
	return versions, nil
}

// GetVersion returns one full snapshot.
func (s *Service) GetVersion(ctx context.Context, projectID, nodeID string, version int) (Version, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return Version{}, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return Version{}, err
	}
	row, err := s.queries.GetProjectNodeVersion(ctx, dbsqlc.GetProjectNodeVersionParams{
		NodeID:  node.ID,
		Version: safeInt32(version),
	})
	if err != nil {
		if notFound(err) {
			return Version{}, ErrVersionNotFound
		}
		return Version{}, err
	}
	return toVersion(row), nil
}
