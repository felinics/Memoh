package project

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

// Board returns every live issue of the project with labels attached; the
// client groups by status into columns.
func (s *Service) Board(ctx context.Context, projectID string) ([]Issue, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return nil, ErrProjectNotFound
	}
	rows, err := s.queries.ListProjectIssues(ctx, pid)
	if err != nil {
		return nil, err
	}
	labelRows, err := s.queries.ListProjectNodeLabelsByProject(ctx, pid)
	if err != nil {
		return nil, err
	}
	labelsByNode := make(map[string][]Label, len(labelRows))
	for _, row := range labelRows {
		nodeID := row.NodeID.String()
		labelsByNode[nodeID] = append(labelsByNode[nodeID], Label{
			ID:        row.LabelID.String(),
			ProjectID: projectID,
			Name:      row.Name,
			Color:     row.Color,
		})
	}
	issues := make([]Issue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, toIssue(row, labelsByNode[row.ID.String()]))
	}
	return issues, nil
}

// resolvedIssueFields is the merge of the current details row with an
// UpdateIssueRequest, computed in Go so partial-update semantics stay out
// of SQL.
type resolvedIssueFields struct {
	status   string
	assignee pgtype.UUID
	bot      pgtype.UUID
	priority pgtype.Text
	dueAt    pgtype.Timestamptz
}

// UpdateIssue patches issue fields (and optionally the card rank) under
// the revision optimistic lock, logging one activity row per changed
// field.
func (s *Service) UpdateIssue(ctx context.Context, projectID, nodeID, userID string, req UpdateIssueRequest) (IssueDetails, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return IssueDetails{}, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return IssueDetails{}, err
	}
	if node.Type != NodeTypeIssue {
		return IssueDetails{}, ErrNotAnIssue
	}
	current, err := s.queries.GetProjectIssueDetails(ctx, node.ID)
	if err != nil {
		if notFound(err) {
			return IssueDetails{}, ErrNodeNotFound
		}
		return IssueDetails{}, err
	}

	fields, err := resolveIssueFields(current, req)
	if err != nil {
		return IssueDetails{}, err
	}
	actor := db.ParseUUIDOrEmpty(userID)

	var updated dbsqlc.ProjectIssueDetail
	err = s.inTx(ctx, func(q dbstore.Queries) error {
		var err error
		updated, err = q.UpdateProjectIssueDetails(ctx, dbsqlc.UpdateProjectIssueDetailsParams{
			Status:           fields.status,
			AssigneeUserID:   fields.assignee,
			AssigneeBotID:    fields.bot,
			Priority:         fields.priority,
			DueAt:            fields.dueAt,
			NodeID:           node.ID,
			ExpectedRevision: safeInt32(req.ExpectedRevision),
		})
		if err != nil {
			if notFound(err) {
				return s.revisionConflict(ctx, q, node.ID)
			}
			return err
		}
		if err := s.recordIssueActivity(ctx, q, node.ID, actor, current, updated); err != nil {
			return err
		}
		if req.Rank != nil && strings.TrimSpace(*req.Rank) != "" {
			rank := strings.TrimSpace(*req.Rank)
			if _, err := q.UpdateProjectNodeRank(ctx, dbsqlc.UpdateProjectNodeRankParams{
				Rank:   rank,
				NodeID: node.ID,
			}); err != nil {
				return err
			}
			if rankNeedsRebalance(rank) {
				return s.rebalanceIssueColumn(ctx, q, node.ProjectID, updated.Status)
			}
		}
		return nil
	})
	if err != nil {
		return IssueDetails{}, err
	}
	return toIssueDetails(updated), nil
}

func (*Service) revisionConflict(ctx context.Context, q dbstore.Queries, nodeID pgtype.UUID) error {
	row, err := q.GetProjectIssueDetails(ctx, nodeID)
	if err != nil {
		if notFound(err) {
			return ErrNodeNotFound
		}
		return err
	}
	return &RevisionConflictError{Current: toIssueDetails(row)}
}

func resolveIssueFields(current dbsqlc.ProjectIssueDetail, req UpdateIssueRequest) (resolvedIssueFields, error) {
	fields := resolvedIssueFields{
		status:   current.Status,
		assignee: current.AssigneeUserID,
		bot:      current.AssigneeBotID,
		priority: current.Priority,
		dueAt:    current.DueAt,
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if !validStatus(status) {
			return fields, ErrInvalidStatus
		}
		fields.status = status
	}
	if req.AssigneeUserID != nil {
		fields.assignee = db.ParseUUIDOrEmpty(*req.AssigneeUserID)
		if fields.assignee.Valid {
			fields.bot = pgtype.UUID{}
		}
	}
	if req.AssigneeBotID != nil {
		fields.bot = db.ParseUUIDOrEmpty(*req.AssigneeBotID)
		if fields.bot.Valid {
			fields.assignee = pgtype.UUID{}
		}
	}
	if fields.assignee.Valid && fields.bot.Valid {
		return fields, ErrAssigneeConflict
	}
	if req.Priority != nil {
		priority := strings.TrimSpace(*req.Priority)
		if priority == "" {
			fields.priority = pgtype.Text{}
		} else {
			if !validPriority(priority) {
				return fields, ErrInvalidPriority
			}
			fields.priority = pgtype.Text{String: priority, Valid: true}
		}
	}
	if req.DueAt != nil {
		raw := strings.TrimSpace(*req.DueAt)
		if raw == "" {
			fields.dueAt = pgtype.Timestamptz{}
		} else {
			due, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return fields, err
			}
			fields.dueAt = pgtype.Timestamptz{Time: due, Valid: true}
		}
	}
	return fields, nil
}

// recordIssueActivity diffs the pre/post rows and writes one activity row
// per changed field, inside the update transaction.
func (*Service) recordIssueActivity(ctx context.Context, q dbstore.Queries, nodeID, actor pgtype.UUID, before, after dbsqlc.ProjectIssueDetail) error {
	type change struct {
		field    string
		old, new string
	}
	var changes []change
	if before.Status != after.Status {
		changes = append(changes, change{"status", before.Status, after.Status})
	}
	if assigneeValue(before.AssigneeUserID, before.AssigneeBotID) != assigneeValue(after.AssigneeUserID, after.AssigneeBotID) {
		changes = append(changes, change{
			"assignee",
			assigneeValue(before.AssigneeUserID, before.AssigneeBotID),
			assigneeValue(after.AssigneeUserID, after.AssigneeBotID),
		})
	}
	if textString(before.Priority) != textString(after.Priority) {
		changes = append(changes, change{"priority", textString(before.Priority), textString(after.Priority)})
	}
	if dueValue(before.DueAt) != dueValue(after.DueAt) {
		changes = append(changes, change{"due_at", dueValue(before.DueAt), dueValue(after.DueAt)})
	}
	for _, c := range changes {
		if err := q.InsertProjectIssueActivity(ctx, dbsqlc.InsertProjectIssueActivityParams{
			NodeID:      nodeID,
			ActorUserID: actor,
			Field:       c.field,
			OldValue:    pgtype.Text{String: c.old, Valid: c.old != ""},
			NewValue:    pgtype.Text{String: c.new, Valid: c.new != ""},
		}); err != nil {
			return err
		}
	}
	return nil
}

func assigneeValue(user, bot pgtype.UUID) string {
	if user.Valid {
		return "user:" + user.String()
	}
	if bot.Valid {
		return "bot:" + bot.String()
	}
	return ""
}

func dueValue(v pgtype.Timestamptz) string {
	if !v.Valid {
		return ""
	}
	return v.Time.UTC().Format(time.RFC3339)
}

// rebalanceIssueColumn re-spreads one kanban column's rank keys.
func (*Service) rebalanceIssueColumn(ctx context.Context, q dbstore.Queries, projectID pgtype.UUID, status string) error {
	cards, err := q.ListProjectIssueRanksByStatus(ctx, dbsqlc.ListProjectIssueRanksByStatusParams{
		ProjectID: projectID,
		Status:    status,
	})
	if err != nil {
		return err
	}
	ranks := rebalancedRanks(len(cards))
	for i, card := range cards {
		if _, err := q.UpdateProjectNodeRank(ctx, dbsqlc.UpdateProjectNodeRankParams{
			Rank:   ranks[i],
			NodeID: card.ID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Activity lists the issue's field-change history, oldest first.
func (s *Service) Activity(ctx context.Context, projectID, nodeID string) ([]Activity, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	node, err := s.getNodeRow(ctx, s.queries, projectID, nodeID)
	if err != nil {
		return nil, err
	}
	if node.Type != NodeTypeIssue {
		return nil, ErrNotAnIssue
	}
	rows, err := s.queries.ListProjectIssueActivity(ctx, node.ID)
	if err != nil {
		return nil, err
	}
	activities := make([]Activity, 0, len(rows))
	for _, row := range rows {
		activities = append(activities, toActivity(row))
	}
	return activities, nil
}
