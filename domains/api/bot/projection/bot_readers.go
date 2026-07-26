// Package projection adapts other domains' contracts to the Bot ports.
//
// These are business translations, not wiring: each one maps a foreign
// domain's records and sentinel errors onto Bot's vocabulary. They live in a
// named package rather than an assembly seam because error mapping is real
// behaviour that readers should not have to find inside "wiring".
package projection

import (
	"context"
	"errors"

	"github.com/memohai/memoh/domains/api/bot"
	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/domains/runtime/workspace"
)

// NewBotUserReader adapts the IAM account service to the bots UserReader port.
//
// It consumes the business surface rather than IAM persistence so bot owner
// lookups go through the same account rules as every other caller.
func NewBotUserReader(accounts *account.Service) botpersistence.UserReader {
	return botUserReader{accounts: accounts}
}

type botUserReader struct {
	accounts *account.Service
}

func (r botUserReader) GetUser(ctx context.Context, userID string) (botpersistence.UserRecord, error) {
	if r.accounts == nil {
		return botpersistence.UserRecord{}, bot.ErrOwnerUserNotFound
	}
	row, err := r.accounts.Get(ctx, userID)
	if err != nil {
		if errors.Is(err, account.ErrAccountNotFound) {
			return botpersistence.UserRecord{}, bot.ErrOwnerUserNotFound
		}
		return botpersistence.UserRecord{}, err
	}
	return botpersistence.UserRecord{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		AvatarURL:   row.AvatarURL,
	}, nil
}

// NewBotContainerReader adapts Runtime container persistence to the bots ContainerReader port.
func NewBotContainerReader(store workspace.ContainerStore) botpersistence.ContainerReader {
	return botContainerReader{store: store}
}

type botContainerReader struct {
	store workspace.ContainerStore
}

func (r botContainerReader) GetContainerByBotID(ctx context.Context, botID string) (botpersistence.ContainerRecord, error) {
	if r.store == nil {
		return botpersistence.ContainerRecord{}, bot.ErrContainerNotFound
	}
	row, err := r.store.FindContainer(ctx, botID)
	if err != nil {
		if errors.Is(err, workspace.ErrContainerNotFound) || errors.Is(err, workspace.ErrRecordNotFound) {
			return botpersistence.ContainerRecord{}, bot.ErrContainerNotFound
		}
		return botpersistence.ContainerRecord{}, err
	}
	return botpersistence.ContainerRecord{
		ContainerID: row.ContainerID,
		Namespace:   row.Namespace,
		Image:       row.Image,
		Status:      row.Status,
	}, nil
}
