package project

import (
	"context"
	"strings"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 100
)

// ilikeEscaper neutralizes ILIKE wildcards in user queries so "50%" finds
// the literal string.
var ilikeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Search runs a substring search over titles and bodies across every
// project (or one, when filtered).
func (s *Service) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return []SearchResult{}, nil
	}
	if req.Type != "" && req.Type != NodeTypeDoc && req.Type != NodeTypeIssue {
		return nil, ErrInvalidNodeType
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	params := dbsqlc.SearchProjectNodesParams{
		Pattern:    "%" + ilikeEscaper.Replace(query) + "%",
		LimitCount: int32(limit),
	}
	if req.ProjectID != "" {
		pid, err := db.ParseUUID(req.ProjectID)
		if err != nil {
			return nil, ErrProjectNotFound
		}
		params.ProjectID = pid
	}
	if req.Type != "" {
		params.NodeType.String = req.Type
		params.NodeType.Valid = true
	}
	rows, err := s.queries.SearchProjectNodes(ctx, params)
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, toSearchResult(row))
	}
	return results, nil
}
