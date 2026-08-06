package project

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

// defaultMergeWindow bounds snapshot merging: consecutive saves by the same
// editor inside the window update the newest snapshot in place instead of
// piling up a version row per debounce flush.
const defaultMergeWindow = 5 * time.Minute

// Service owns Project/Wiki/Issues business rules. All identity parameters
// are human user IDs in this version; agent (bot) writes arrive in a later
// phase through the same tables' *_bot_id columns.
type Service struct {
	log         *slog.Logger
	pool        *pgxpool.Pool
	queries     dbstore.Queries
	mergeWindow time.Duration
	now         func() time.Time
}

func NewService(log *slog.Logger, pool *pgxpool.Pool, queries dbstore.Queries) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		log:         log.With(slog.String("service", "project")),
		pool:        pool,
		queries:     queries,
		mergeWindow: defaultMergeWindow,
		now:         time.Now,
	}
}

// inTx runs fn inside one transaction; without a pool (unit tests) it runs
// directly against the base queries.
func (s *Service) inTx(ctx context.Context, fn func(dbstore.Queries) error) error {
	if s.pool == nil {
		return fn(s.queries)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func notFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound)
}

// CreateProject creates a collaboration space.
func (s *Service) CreateProject(ctx context.Context, userID string, req CreateProjectRequest) (Project, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Project{}, ErrNameRequired
	}
	row, err := s.queries.CreateProject(ctx, dbsqlc.CreateProjectParams{
		Name:            name,
		Description:     strings.TrimSpace(req.Description),
		CreatedByUserID: db.ParseUUIDOrEmpty(userID),
	})
	if err != nil {
		return Project{}, err
	}
	return toProject(row), nil
}

// ListProjects returns every live project of the team.
func (s *Service) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.queries.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, toProjectWithCounts(row))
	}
	return projects, nil
}

// GetProject returns one live project.
func (s *Service) GetProject(ctx context.Context, projectID string) (Project, error) {
	row, err := s.getProjectRow(ctx, s.queries, projectID)
	if err != nil {
		return Project{}, err
	}
	return toProject(row), nil
}

// UpdateProject patches name/description.
func (s *Service) UpdateProject(ctx context.Context, projectID string, req UpdateProjectRequest) (Project, error) {
	current, err := s.getProjectRow(ctx, s.queries, projectID)
	if err != nil {
		return Project{}, err
	}
	name := current.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			return Project{}, ErrNameRequired
		}
	}
	description := current.Description
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	row, err := s.queries.UpdateProject(ctx, dbsqlc.UpdateProjectParams{
		Name:        name,
		Description: description,
		ProjectID:   current.ID,
	})
	if err != nil {
		if notFound(err) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, err
	}
	return toProject(row), nil
}

// DeleteProject soft-deletes the project. Nodes stay in place; every read
// path re-checks the project row, so they become unreachable together.
func (s *Service) DeleteProject(ctx context.Context, projectID string) error {
	id, err := db.ParseUUID(projectID)
	if err != nil {
		return ErrProjectNotFound
	}
	rows, err := s.queries.SoftDeleteProject(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrProjectNotFound
	}
	return nil
}

func (*Service) getProjectRow(ctx context.Context, q dbstore.Queries, projectID string) (dbsqlc.Project, error) {
	id, err := db.ParseUUID(projectID)
	if err != nil {
		return dbsqlc.Project{}, ErrProjectNotFound
	}
	row, err := q.GetProject(ctx, id)
	if err != nil {
		if notFound(err) {
			return dbsqlc.Project{}, ErrProjectNotFound
		}
		return dbsqlc.Project{}, err
	}
	return row, nil
}

func (*Service) getNodeRow(ctx context.Context, q dbstore.Queries, projectID, nodeID string) (dbsqlc.ProjectNode, error) {
	pid, err := db.ParseUUID(projectID)
	if err != nil {
		return dbsqlc.ProjectNode{}, ErrProjectNotFound
	}
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return dbsqlc.ProjectNode{}, ErrNodeNotFound
	}
	row, err := q.GetProjectNode(ctx, dbsqlc.GetProjectNodeParams{ProjectID: pid, NodeID: nid})
	if err != nil {
		if notFound(err) {
			return dbsqlc.ProjectNode{}, ErrNodeNotFound
		}
		return dbsqlc.ProjectNode{}, err
	}
	return row, nil
}

// requireProject gates every node-level entry point: a soft-deleted
// project must take its nodes out of reach with it.
func (s *Service) requireProject(ctx context.Context, projectID string) error {
	_, err := s.getProjectRow(ctx, s.queries, projectID)
	return err
}
