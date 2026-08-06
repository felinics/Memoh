package project

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
)

// Human-facing references. Agents (and humans typing into a tool call) address
// a project by NAME and a node by its issue number or its doc path — a UUID is
// 36 characters of noise in a prompt. Every resolver still accepts a UUID, so
// an id copied out of an API response always works.
//
//	project: "产品设计"                    or a UUID
//	node:    "#12"                        (issue number, scoped to the project)
//	         "需求文档/数据模型"            (doc path, titles separated by "/")
//	         a UUID
//
// Names and titles are NOT unique — the product deliberately lets two docs in
// one folder share a title. So resolution can be ambiguous, and an ambiguous
// reference is an error that lists the candidates (with ids) rather than a
// silent pick: choosing the "first" match would make an agent edit a document
// the user never meant.

// AmbiguousError reports a reference that matched more than one row. Candidates
// carries enough to disambiguate — the caller re-issues with an id.
type AmbiguousError struct {
	Ref        string
	Kind       string // "project" or "node"
	Candidates []Candidate
}

// Candidate identifies one of several rows a reference matched.
type Candidate struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path,omitempty"`
}

func (e *AmbiguousError) Error() string {
	labels := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		label := c.Path
		if label == "" {
			label = c.Title
		}
		labels = append(labels, fmt.Sprintf("%s (%s)", label, c.ID))
	}
	return fmt.Sprintf("%s reference %q is ambiguous: %s", e.Kind, e.Ref, strings.Join(labels, ", "))
}

func isUUID(s string) bool {
	_, err := uuid.Parse(strings.TrimSpace(s))
	return err == nil
}

// ResolveProject accepts a project name or id.
func (s *Service) ResolveProject(ctx context.Context, ref string) (Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Project{}, ErrProjectNotFound
	}
	if isUUID(ref) {
		return s.GetProject(ctx, ref)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return Project{}, err
	}
	var matches []Project
	for _, p := range projects {
		if strings.EqualFold(strings.TrimSpace(p.Name), ref) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return Project{}, ErrProjectNotFound
	case 1:
		return matches[0], nil
	default:
		candidates := make([]Candidate, 0, len(matches))
		for _, p := range matches {
			candidates = append(candidates, Candidate{ID: p.ID, Title: p.Name})
		}
		return Project{}, &AmbiguousError{Ref: ref, Kind: "project", Candidates: candidates}
	}
}

// ResolveNode accepts "#<number>" for an issue, a "/"-separated doc path, or a
// node id. projectID must already be resolved.
func (s *Service) ResolveNode(ctx context.Context, projectID, ref string) (Node, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Node{}, ErrNodeNotFound
	}
	if isUUID(ref) {
		row, err := s.getNodeRow(ctx, s.queries, projectID, ref)
		if err != nil {
			return Node{}, err
		}
		return toNode(row), nil
	}
	if strings.HasPrefix(ref, "#") {
		return s.resolveIssueByNumber(ctx, projectID, strings.TrimPrefix(ref, "#"))
	}
	return s.resolveDocByPath(ctx, projectID, ref)
}

func (s *Service) resolveIssueByNumber(ctx context.Context, projectID, raw string) (Node, error) {
	number, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || number <= 0 {
		return Node{}, ErrNodeNotFound
	}
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return Node{}, ErrProjectNotFound
	}
	row, err := s.queries.GetProjectIssueByNumber(ctx, dbsqlc.GetProjectIssueByNumberParams{
		ProjectID: pid,
		Number:    int32ToPg(number),
	})
	if err != nil {
		if notFound(err) {
			return Node{}, ErrNodeNotFound
		}
		return Node{}, err
	}
	return toNode(row), nil
}

// resolveDocByPath walks the doc tree segment by segment. The whole tree is one
// query, so a deep path costs no more than a shallow one.
func (s *Service) resolveDocByPath(ctx context.Context, projectID, path string) (Node, error) {
	segments := make([]string, 0, 4)
	for _, part := range strings.Split(path, "/") {
		part = strings.TrimSpace(part)
		if part != "" {
			segments = append(segments, part)
		}
	}
	if len(segments) == 0 {
		return Node{}, ErrNodeNotFound
	}

	tree, err := s.Tree(ctx, projectID)
	if err != nil {
		return Node{}, err
	}
	childrenOf := make(map[string][]TreeNode, len(tree))
	for _, node := range tree {
		childrenOf[node.ParentID] = append(childrenOf[node.ParentID], node)
	}

	parentID := ""
	var current TreeNode
	for depth, segment := range segments {
		var matches []TreeNode
		for _, candidate := range childrenOf[parentID] {
			if strings.EqualFold(strings.TrimSpace(candidate.Title), segment) {
				matches = append(matches, candidate)
			}
		}
		switch len(matches) {
		case 0:
			return Node{}, ErrNodeNotFound
		case 1:
			current = matches[0]
			parentID = current.ID
		default:
			prefix := strings.Join(segments[:depth], "/")
			candidates := make([]Candidate, 0, len(matches))
			for _, m := range matches {
				candidates = append(candidates, Candidate{
					ID:    m.ID,
					Title: m.Title,
					Path:  strings.TrimPrefix(prefix+"/"+m.Title, "/"),
				})
			}
			return Node{}, &AmbiguousError{Ref: path, Kind: "node", Candidates: candidates}
		}
	}

	row, err := s.getNodeRow(ctx, s.queries, projectID, current.ID)
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

// DocPath renders a doc node's "/"-separated path, the form ResolveNode reads
// back. Returns an empty string for issues (they are addressed by number).
func (s *Service) DocPath(ctx context.Context, projectID, nodeID string) (string, error) {
	tree, err := s.Tree(ctx, projectID)
	if err != nil {
		return "", err
	}
	byID := make(map[string]TreeNode, len(tree))
	for _, node := range tree {
		byID[node.ID] = node
	}
	segments := make([]string, 0, 4)
	for cursor, depth := nodeID, 0; cursor != "" && depth < maxTreeDepth; depth++ {
		node, ok := byID[cursor]
		if !ok {
			break
		}
		segments = append([]string{node.Title}, segments...)
		cursor = node.ParentID
	}
	return strings.Join(segments, "/"), nil
}
