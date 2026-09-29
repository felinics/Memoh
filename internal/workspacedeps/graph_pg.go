package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (s *postgresStore) SavePlan(ctx context.Context, bot string, plan Plan) error {
	id, err := parseBotID(bot)
	if err != nil {
		return err
	}
	// Definitions are already stored as verified immutable archives. Persist
	// their identities, never a second executable script or injected environment.
	plan.Nodes = slices.Clone(plan.Nodes)
	for i := range plan.Nodes {
		plan.Nodes[i].Script = nil
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return s.q.SaveDependencyPlan(ctx, dbsqlc.SaveDependencyPlanParams{BotID: id, ID: plan.ID, Plan: data})
}

func (s *postgresStore) LoadPlan(ctx context.Context, bot, id string) (Plan, error) {
	bid, err := parseBotID(bot)
	if err != nil {
		return Plan{}, err
	}
	data, err := s.q.GetDependencyPlan(ctx, dbsqlc.GetDependencyPlanParams{BotID: bid, ID: id})
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	err = json.Unmarshal(data, &plan)
	return plan, err
}

func (s *postgresStore) ReadGraph(ctx context.Context, bot string) (DependencyGraph, error) {
	id, err := parseBotID(bot)
	if err != nil {
		return nil, err
	}
	data, err := s.q.GetDependencyGraph(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DependencyGraph{}, nil
	}
	if err != nil {
		return nil, err
	}
	var graph DependencyGraph
	err = json.Unmarshal(data, &graph)
	return graph, err
}

func (s *postgresStore) ClaimGraph(ctx context.Context, bot, owner string) (DependencyGraph, error) {
	id, err := parseBotID(bot)
	if err != nil {
		return nil, err
	}
	var data []byte
	tx, ok := s.q.(interface {
		InTx(context.Context, func(dbstore.Queries) error) error
		SupportsTransactions() bool
	})
	if !ok || !tx.SupportsTransactions() {
		return nil, ErrGraphUnresolved
	}
	err = tx.InTx(ctx, func(q dbstore.Queries) error {
		if err := q.EnsureDependencyGraph(ctx, id); err != nil {
			return err
		}
		if _, err := q.LockDependencyGraph(ctx, id); err != nil {
			return err
		}
		var err error
		data, err = q.ClaimDependencyGraph(ctx, dbsqlc.ClaimDependencyGraphParams{BotID: id, Owner: owner})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	var graph DependencyGraph
	err = json.Unmarshal(data, &graph)
	return graph, err
}

func (s *postgresStore) RenewGraph(ctx context.Context, bot, owner string) error {
	id, err := parseBotID(bot)
	if err != nil {
		return err
	}
	n, err := s.q.RenewDependencyGraph(ctx, dbsqlc.RenewDependencyGraphParams{BotID: id, Owner: owner})
	if err == nil && n == 0 {
		return ErrBusy
	}
	return err
}

func (s *postgresStore) WriteGraph(ctx context.Context, bot, owner string, graph DependencyGraph) error {
	id, err := parseBotID(bot)
	if err != nil {
		return err
	}
	data, err := json.Marshal(graph)
	if err != nil {
		return err
	}
	n, err := s.q.WriteDependencyGraph(ctx, dbsqlc.WriteDependencyGraphParams{BotID: id, Owner: owner, Graph: data})
	if err == nil && n == 0 {
		return ErrBusy
	}
	return err
}

func (s *postgresStore) ReleaseGraph(ctx context.Context, bot, owner string) error {
	id, err := parseBotID(bot)
	if err != nil {
		return err
	}
	return s.q.ReleaseDependencyGraph(ctx, dbsqlc.ReleaseDependencyGraphParams{BotID: id, Owner: owner})
}

func (s *postgresStore) AppUsers(ctx context.Context, bot, dep string) ([]AppDependencyUser, error) {
	id, err := parseBotID(bot)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListDependencyAppUsers(ctx, dbsqlc.ListDependencyAppUsersParams{BotID: id, DependencyID: dep})
	if err != nil {
		return nil, err
	}
	users := make([]AppDependencyUser, 0, len(rows))
	for _, r := range rows {
		users = append(users, AppDependencyUser{r.InstallationID, r.AppID})
	}
	return users, nil
}
