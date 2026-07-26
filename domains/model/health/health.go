// Package health publishes the Model connection health checker.
//
// The concrete checker stays owner-private under domains/model/internal/health
// because it depends on Model catalogs and execution. This package is the
// public seam: composition roots construct the checker here instead of
// reaching into owner-private packages.
package health

import (
	"context"
	"log/slog"

	modelhealth "github.com/memohai/memoh/domains/model/internal/health"
	"github.com/memohai/memoh/internal/healthcheck"
)

// NewBotModelLookup adapts an API-owned bot projection to the Model health port.
func NewBotModelLookup(
	getBot func(context.Context, string) (ownerUserID, chatModelID string, err error),
) modelhealth.BotModelLookup {
	return botModelLookupFunc(getBot)
}

type botModelLookupFunc func(context.Context, string) (ownerUserID, chatModelID string, err error)

func (f botModelLookupFunc) GetBotModelIDs(ctx context.Context, botID string) (modelhealth.BotModels, error) {
	ownerUserID, chatModelID, err := f(ctx, botID)
	if err != nil {
		return modelhealth.BotModels{}, err
	}
	return modelhealth.BotModels{
		OwnerUserID: ownerUserID,
		ChatModelID: chatModelID,
	}, nil
}

// NewHealthChecker constructs the Model connection health checker from
// domain-typed dependencies. cmd must call this constructor rather than
// importing owner-private health packages.
func NewHealthChecker(log *slog.Logger, lookup modelhealth.BotModelLookup, prober modelhealth.ModelProber) healthcheck.Checker {
	return modelhealth.NewChecker(log, lookup, prober)
}
