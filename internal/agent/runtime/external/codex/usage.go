package codex

import (
	"context"
	"errors"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocol"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/errs"
)

// ErrUsageUnavailable: Codex could not read the account's usage limits.
var ErrUsageUnavailable = errors.New("codex usage unavailable")

var _ external.AccountReader = (*Driver)(nil)

// AccountUsage is the ChatGPT account's Codex usage: one entry per rolling
// window (the short window first, then the weekly one when the plan has it).
type AccountUsage struct {
	LimitReached bool
	Windows      []UsageWindow
}

type UsageWindow struct {
	UsedPercent   int
	WindowMinutes int
	ResetsAt      *time.Time
}

// Account asks the Agent's Codex which ChatGPT account it is signed in to.
func (d *Driver) Account(ctx context.Context, botID, botAgentID string) (external.Account, error) {
	srv, release, err := d.chatGPTServer(ctx, botID, botAgentID)
	if err != nil {
		return external.Account{}, err
	}
	defer release()
	var response protocol.GetAccountResponse
	if err := srv.conn.Call(ctx, protocol.MethodAccountRead, protocol.GetAccountParams{}, &response); err != nil {
		return external.Account{}, external.Unavailable(err)
	}
	if response.Account == nil || response.Account.Chatgpt == nil {
		return external.Account{}, nil
	}
	account := external.Account{SignedIn: true}
	if response.Account.Chatgpt.Email != nil {
		account.Email = *response.Account.Chatgpt.Email
	}
	return account, nil
}

// AccountUsage asks the Agent's Codex for its ChatGPT account's usage limits.
func (d *Driver) AccountUsage(ctx context.Context, botID, botAgentID string) (AccountUsage, error) {
	srv, release, err := d.chatGPTServer(ctx, botID, botAgentID)
	if err != nil {
		return AccountUsage{}, err
	}
	defer release()
	var response protocol.GetAccountRateLimitsResponse
	if err := srv.conn.Call(ctx, protocol.MethodAccountRateLimitsRead, nil, &response); err != nil {
		return AccountUsage{}, errs.WrapDependency(errors.Join(ErrUsageUnavailable, err), "")
	}
	return accountUsage(response), nil
}

func (d *Driver) chatGPTServer(ctx context.Context, botID, botAgentID string) (*appServer, func(), error) {
	cfg, err := d.resolveAgentConfig(ctx, botID, botAgentID, false)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Auth != AuthChatGPT {
		return nil, nil, external.CredentialError(agentcredential.ErrIncompatible)
	}
	srv, release, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return nil, nil, wrapServerError(err)
	}
	return srv, release, nil
}

func accountUsage(response protocol.GetAccountRateLimitsResponse) AccountUsage {
	limits := response.RateLimits
	usage := AccountUsage{
		LimitReached: limits.RateLimitReachedType != nil ||
			(response.OrdinaryUsageAllowed != nil && !*response.OrdinaryUsageAllowed),
		Windows: []UsageWindow{},
	}
	for _, window := range []*protocol.RateLimitWindow{limits.Primary, limits.Secondary} {
		if window == nil {
			continue
		}
		entry := UsageWindow{UsedPercent: int(window.UsedPercent)}
		if window.WindowDurationMins != nil {
			entry.WindowMinutes = int(*window.WindowDurationMins)
		}
		if window.ResetsAt != nil {
			resetsAt := time.Unix(*window.ResetsAt, 0).UTC()
			entry.ResetsAt = &resetsAt
		}
		usage.Windows = append(usage.Windows, entry)
	}
	return usage
}
