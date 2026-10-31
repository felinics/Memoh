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

// Account asks the Agent's Codex which ChatGPT account it is signed in to.
func (d *Driver) Account(ctx context.Context, botID, botAgentID string) (external.Account, error) {
	srv, release, err := d.chatGPTServer(ctx, botID, botAgentID)
	if err != nil {
		return external.Account{}, err
	}
	defer release()
	var response protocol.GetAccountResponse
	if err := srv.conn.Call(ctx, protocol.MethodAccountRead, protocol.GetAccountParams{}, &response); err != nil {
		return external.Account{}, external.Upstream(err)
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

// Quota asks the Agent's Codex for its ChatGPT account's usage limits.
func (d *Driver) Quota(ctx context.Context, botID, botAgentID string) (external.Quota, error) {
	srv, release, err := d.chatGPTServer(ctx, botID, botAgentID)
	if err != nil {
		return external.Quota{}, err
	}
	defer release()
	var response protocol.GetAccountRateLimitsResponse
	if err := srv.conn.Call(ctx, protocol.MethodAccountRateLimitsRead, nil, &response); err != nil {
		return external.Quota{}, errs.WrapDependency(errors.Join(external.ErrQuotaUnavailable, err), "")
	}
	return accountQuota(response), nil
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
		return nil, nil, err
	}
	return srv, release, nil
}

func accountQuota(response protocol.GetAccountRateLimitsResponse) external.Quota {
	limits := response.RateLimits
	usage := external.Quota{
		LimitReached: limits.RateLimitReachedType != nil ||
			(response.OrdinaryUsageAllowed != nil && !*response.OrdinaryUsageAllowed),
		Windows: []external.QuotaWindow{},
	}
	for _, window := range []*protocol.RateLimitWindow{limits.Primary, limits.Secondary} {
		if window == nil {
			continue
		}
		entry := external.QuotaWindow{UsedPercent: int(window.UsedPercent)}
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
