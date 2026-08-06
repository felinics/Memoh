package project

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
)

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

func textString(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func timePtrFromPg(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func toProject(row dbsqlc.Project) Project {
	return Project{
		ID:              row.ID.String(),
		Name:            row.Name,
		Description:     row.Description,
		CreatedByUserID: uuidString(row.CreatedByUserID),
		CreatedAt:       db.TimeFromPg(row.CreatedAt),
		UpdatedAt:       db.TimeFromPg(row.UpdatedAt),
	}
}

func toProjectWithCounts(row dbsqlc.ListProjectsRow) Project {
	return Project{
		ID:               row.ID.String(),
		Name:             row.Name,
		Description:      row.Description,
		CreatedByUserID:  uuidString(row.CreatedByUserID),
		OpenIssueCount:   int(row.OpenIssueCount),
		ClosedIssueCount: int(row.ClosedIssueCount),
		CreatedAt:        db.TimeFromPg(row.CreatedAt),
		UpdatedAt:        db.TimeFromPg(row.UpdatedAt),
	}
}

func toNode(row dbsqlc.ProjectNode) Node {
	return Node{
		ID:              row.ID.String(),
		ProjectID:       row.ProjectID.String(),
		Type:            row.Type,
		ParentID:        uuidString(row.ParentID),
		Rank:            row.Rank,
		Title:           row.Title,
		Body:            row.Body,
		Version:         int(row.Version),
		CreatedByUserID: uuidString(row.CreatedByUserID),
		CreatedByBotID:  uuidString(row.CreatedByBotID),
		UpdatedByUserID: uuidString(row.UpdatedByUserID),
		UpdatedByBotID:  uuidString(row.UpdatedByBotID),
		CreatedAt:       db.TimeFromPg(row.CreatedAt),
		UpdatedAt:       db.TimeFromPg(row.UpdatedAt),
	}
}

func toTreeNode(row dbsqlc.ListProjectDocNodesRow) TreeNode {
	return TreeNode{
		ID:        row.ID.String(),
		ParentID:  uuidString(row.ParentID),
		Rank:      row.Rank,
		Title:     row.Title,
		Version:   int(row.Version),
		CreatedAt: db.TimeFromPg(row.CreatedAt),
		UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
}

func toIssueDetails(row dbsqlc.ProjectIssueDetail) IssueDetails {
	return IssueDetails{
		NodeID:         row.NodeID.String(),
		Status:         row.Status,
		AssigneeUserID: uuidString(row.AssigneeUserID),
		AssigneeBotID:  uuidString(row.AssigneeBotID),
		Priority:       textString(row.Priority),
		DueAt:          timePtrFromPg(row.DueAt),
		Revision:       int(row.Revision),
		UpdatedAt:      db.TimeFromPg(row.UpdatedAt),
	}
}

func toIssue(row dbsqlc.ListProjectIssuesRow, labels []Label) Issue {
	if labels == nil {
		labels = []Label{}
	}
	return Issue{
		ID:             row.ID.String(),
		ProjectID:      row.ProjectID.String(),
		Rank:           row.Rank,
		Title:          row.Title,
		Version:        int(row.Version),
		Status:         row.Status,
		AssigneeUserID: uuidString(row.AssigneeUserID),
		AssigneeBotID:  uuidString(row.AssigneeBotID),
		Priority:       textString(row.Priority),
		DueAt:          timePtrFromPg(row.DueAt),
		Revision:       int(row.Revision),
		Labels:         labels,
		CreatedAt:      db.TimeFromPg(row.CreatedAt),
		UpdatedAt:      db.TimeFromPg(row.UpdatedAt),
	}
}

func toLabel(row dbsqlc.ProjectLabel) Label {
	return Label{
		ID:        row.ID.String(),
		ProjectID: row.ProjectID.String(),
		Name:      row.Name,
		Color:     row.Color,
		CreatedAt: db.TimeFromPg(row.CreatedAt),
	}
}

func toComment(row dbsqlc.ProjectComment) Comment {
	return Comment{
		ID:           row.ID.String(),
		NodeID:       row.NodeID.String(),
		AuthorUserID: uuidString(row.AuthorUserID),
		AuthorBotID:  uuidString(row.AuthorBotID),
		Body:         row.Body,
		CreatedAt:    db.TimeFromPg(row.CreatedAt),
		UpdatedAt:    db.TimeFromPg(row.UpdatedAt),
	}
}

func toVersionMeta(row dbsqlc.ListProjectNodeVersionsRow) VersionMeta {
	return VersionMeta{
		NodeID:       row.NodeID.String(),
		Version:      int(row.Version),
		Title:        row.Title,
		EditorUserID: uuidString(row.EditorUserID),
		EditorBotID:  uuidString(row.EditorBotID),
		CreatedAt:    db.TimeFromPg(row.CreatedAt),
		UpdatedAt:    db.TimeFromPg(row.UpdatedAt),
	}
}

func toVersion(row dbsqlc.ProjectNodeVersion) Version {
	return Version{
		VersionMeta: VersionMeta{
			NodeID:       row.NodeID.String(),
			Version:      int(row.Version),
			Title:        row.Title,
			EditorUserID: uuidString(row.EditorUserID),
			EditorBotID:  uuidString(row.EditorBotID),
			CreatedAt:    db.TimeFromPg(row.CreatedAt),
			UpdatedAt:    db.TimeFromPg(row.UpdatedAt),
		},
		Body: row.Body,
	}
}

func toActivity(row dbsqlc.ProjectIssueActivity) Activity {
	return Activity{
		ID:          row.ID.String(),
		NodeID:      row.NodeID.String(),
		ActorUserID: uuidString(row.ActorUserID),
		ActorBotID:  uuidString(row.ActorBotID),
		Field:       row.Field,
		OldValue:    textString(row.OldValue),
		NewValue:    textString(row.NewValue),
		CreatedAt:   db.TimeFromPg(row.CreatedAt),
	}
}

func toSearchResult(row dbsqlc.SearchProjectNodesRow) SearchResult {
	return SearchResult{
		ID:          row.ID.String(),
		ProjectID:   row.ProjectID.String(),
		ProjectName: row.ProjectName,
		Type:        row.Type,
		Title:       row.Title,
		Snippet:     row.Snippet,
		UpdatedAt:   db.TimeFromPg(row.UpdatedAt),
	}
}
