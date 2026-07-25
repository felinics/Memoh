package application

import (
	"context"

	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/iam/account"
)

// BotReader supplies the bot profile needed to assemble an agent run.
type BotReader interface {
	GetForAccess(context.Context, string) (bot.Bot, error)
}

// AccountReader supplies account profile data and existence checks.
type AccountReader interface {
	Get(context.Context, string) (account.Account, error)
}

// ChannelIdentityReader supplies inbound identity display data and existence checks.
type ChannelIdentityReader interface {
	GetByID(context.Context, string) (ChannelIdentity, error)
}

// ChannelIdentity is the minimal inbound identity projection required by Agent.
type ChannelIdentity struct {
	ID          string
	DisplayName string
}

// LatestSessionModelReader supplies the model used by the latest persisted session round.
type LatestSessionModelReader interface {
	LatestSessionModelID(context.Context, string) (string, error)
}

// CompactionMessageRefReader supplies legacy message coverage for a compaction artifact.
type CompactionMessageRefReader interface {
	ListCompactionMessageRefs(context.Context, string) ([]string, error)
}

// SettingsReader supplies bot settings snapshots for agent runs.
type SettingsReader interface {
	GetBot(context.Context, string) (setting.Settings, error)
}
