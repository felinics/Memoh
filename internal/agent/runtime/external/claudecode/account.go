package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/errs"
)

// A Claude account signs in on the Agent's own Claude Code, into the Agent's
// config directory. claude_authenticate returns the page to open; that page
// shows a code, and claude_oauth_callback hands it back to the CLI, which
// exchanges it and keeps the login. Memoh never sees a token. The CLI holds
// the sign-in's PKCE verifier, so its process lives until the code is back.
const signInTTL = 15 * time.Minute

var (
	_ external.DeviceLoginProvider = (*Driver)(nil)
	_ external.LoginCodeSubmitter  = (*Driver)(nil)
	_ external.AccountReader       = (*Driver)(nil)
	_ external.QuotaReader         = (*Driver)(nil)
	_ external.BotAgentAuthPurger  = (*Driver)(nil)
)

// signIn is one account sign-in waiting for its code. It ends with its
// process, so a sign-out, which closes every process of the Agent, ends it.
type signIn struct {
	id         string
	botID      string
	botAgentID string
	cli        *controlCLI
	expiry     *time.Timer
	ended      sync.Once
}

// StartDeviceLogin implements external.DeviceLoginProvider.
func (d *Driver) StartDeviceLogin(ctx context.Context, botID, botAgentID string) (external.DeviceLoginStart, error) {
	cfg, err := d.accountConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStart{}, err
	}
	cli, err := d.startControlCLI(ctx, cfg, botID, botAgentID, defaultProjectPath)
	if err != nil {
		return external.DeviceLoginStart{}, err
	}
	var urls struct {
		ManualURL string `json:"manualUrl"`
	}
	turn := cli.turn(ctx)
	if _, err = turn.initialize(ctx); err == nil {
		var raw json.RawMessage
		raw, err = turn.callControl(ctx, "claude_authenticate", map[string]any{"loginWithClaudeAi": true})
		if err == nil {
			err = json.Unmarshal(raw, &urls)
		}
	}
	turn.close()
	if err == nil && urls.ManualURL == "" {
		err = errs.NewDependency("claude did not provide a sign-in URL")
	}
	if err != nil {
		cli.close()
		return external.DeviceLoginStart{}, external.Upstream(err)
	}
	s := &signIn{id: uuid.NewString(), botID: botID, botAgentID: botAgentID, cli: cli}
	d.addSignIn(s)
	return external.DeviceLoginStart{LoginID: s.id, VerificationURL: urls.ManualURL, CodeRequired: true}, nil
}

// SubmitLoginCode implements external.LoginCodeSubmitter. The page shows the
// code as code#state, which is how the CLI's own prompt takes it too.
func (d *Driver) SubmitLoginCode(ctx context.Context, _, botAgentID, loginID, code string) error {
	authorizationCode, state, ok := strings.Cut(strings.TrimSpace(code), "#")
	if !ok || authorizationCode == "" || state == "" {
		return external.ErrLoginCodeInvalid
	}
	// The CLI's sign-in ends with this answer either way, so the first
	// submission takes it and any other finds it gone.
	s := d.takeSignIn(loginID, botAgentID)
	if s == nil {
		return external.ErrLoginCodeInvalid
	}
	defer d.endSignIn(s)
	turn := s.cli.turn(ctx)
	_, err := turn.callControl(ctx, "claude_oauth_callback", map[string]any{"authorizationCode": authorizationCode, "state": state})
	turn.close()
	var rejected *controlRejection
	if errors.As(err, &rejected) {
		d.logger.WarnContext(ctx, "claude sign-in code rejected", slog.String("bot_id", s.botID), slog.Any("error", err))
		return external.ErrLoginCodeInvalid
	}
	if err != nil {
		return external.Upstream(err)
	}
	// The Agent's running conversations started without this login.
	d.procs.Retire(agentProcesses(s.botID, botAgentID))
	return nil
}

// PollDeviceLogin implements external.DeviceLoginProvider. A Claude sign-in
// completes with its code, so a known one is still waiting for it.
func (d *Driver) PollDeviceLogin(_ context.Context, _, _, botAgentID, loginID string) (external.DeviceLoginStatus, error) {
	if d.signIn(loginID, botAgentID) == nil {
		return external.DeviceLoginStatus{Status: external.DeviceLoginUnknown}, nil
	}
	return external.DeviceLoginStatus{Status: external.DeviceLoginPending}, nil
}

// CancelDeviceLogin implements external.DeviceLoginProvider.
func (d *Driver) CancelDeviceLogin(_ context.Context, _, botAgentID, loginID string) error {
	if s := d.signIn(loginID, botAgentID); s != nil {
		d.endSignIn(s)
	}
	return nil
}

func (d *Driver) signIn(loginID, botAgentID string) *signIn {
	d.signInsMu.Lock()
	defer d.signInsMu.Unlock()
	return d.signInLocked(loginID, botAgentID)
}

func (d *Driver) takeSignIn(loginID, botAgentID string) *signIn {
	d.signInsMu.Lock()
	defer d.signInsMu.Unlock()
	s := d.signInLocked(loginID, botAgentID)
	if s != nil {
		delete(d.signIns, loginID)
	}
	return s
}

func (d *Driver) signInLocked(loginID, botAgentID string) *signIn {
	s := d.signIns[loginID]
	if s == nil || s.botAgentID != botAgentID {
		return nil
	}
	return s
}

// addSignIn makes s the Agent's sign-in. An earlier one ends: its code would
// sign in over this one.
func (d *Driver) addSignIn(s *signIn) {
	d.signInsMu.Lock()
	var replaced []*signIn
	for _, other := range d.signIns {
		if other.botAgentID == s.botAgentID {
			replaced = append(replaced, other)
		}
	}
	d.signIns[s.id] = s
	s.expiry = time.AfterFunc(signInTTL, func() { d.endSignIn(s) })
	d.signInsMu.Unlock()
	for _, other := range replaced {
		d.endSignIn(other)
	}
	go func() {
		<-s.cli.sess.Done()
		d.endSignIn(s)
	}()
}

func (d *Driver) endSignIn(s *signIn) {
	s.ended.Do(func() {
		d.signInsMu.Lock()
		delete(d.signIns, s.id)
		s.expiry.Stop()
		d.signInsMu.Unlock()
		s.cli.close()
	})
}

// Account implements external.AccountReader.
func (d *Driver) Account(ctx context.Context, botID, botAgentID string) (external.Account, error) {
	cfg, err := d.accountConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.Account{}, err
	}
	turn, stop, err := d.controlTurn(ctx, cfg, botID, botAgentID, defaultProjectPath)
	if err != nil {
		return external.Account{}, err
	}
	defer stop()
	response, err := turn.initialize(ctx)
	if err != nil {
		return external.Account{}, external.Upstream(err)
	}
	if !response.Account.signedIn() {
		return external.Account{}, nil
	}
	return external.Account{SignedIn: true, Email: response.Account.Email}, nil
}

// usageResponse is the part of get_usage a quota needs: the account's plan
// windows, absent unless a Claude account with its profile scope is signed in.
type usageResponse struct {
	RateLimitsAvailable bool `json:"rate_limits_available"`
	RateLimits          *struct {
		FiveHour    *usageWindow `json:"five_hour"`
		SevenDay    *usageWindow `json:"seven_day"`
		ModelScoped []struct {
			DisplayName string `json:"display_name"`
			usageWindow
		} `json:"model_scoped"`
	} `json:"rate_limits"`
}

type usageWindow struct {
	// Utilization is the share of the window used, 0-100.
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

// Quota implements external.QuotaReader with Claude Code's own /usage data,
// which reaches no model.
func (d *Driver) Quota(ctx context.Context, botID, botAgentID string) (external.Quota, error) {
	cfg, err := d.accountConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.Quota{}, err
	}
	turn, stop, err := d.controlTurn(ctx, cfg, botID, botAgentID, defaultProjectPath)
	if err != nil {
		return external.Quota{}, err
	}
	defer stop()
	var usage usageResponse
	if _, err = turn.initialize(ctx); err == nil {
		var raw json.RawMessage
		// The behaviors scan reads every recent transcript; a quota needs none of it.
		raw, err = turn.callControl(ctx, "get_usage", map[string]any{"skip_behaviors": true})
		if err == nil {
			err = json.Unmarshal(raw, &usage)
		}
	}
	if err != nil {
		return external.Quota{}, errs.WrapDependency(errors.Join(external.ErrQuotaUnavailable, err), "")
	}
	if !usage.RateLimitsAvailable || usage.RateLimits == nil {
		return external.Quota{}, errs.WrapDependency(external.ErrQuotaUnavailable, "")
	}
	return accountQuota(usage), nil
}

func accountQuota(usage usageResponse) external.Quota {
	limits := usage.RateLimits
	quota := external.Quota{Windows: []external.QuotaWindow{}}
	add := func(window *usageWindow, minutes int, model string) {
		if window == nil || window.Utilization == nil {
			return
		}
		entry := external.QuotaWindow{UsedPercent: int(math.Round(*window.Utilization)), WindowMinutes: minutes, Model: model}
		if window.ResetsAt != nil {
			if resetsAt, err := time.Parse(time.RFC3339, *window.ResetsAt); err == nil {
				entry.ResetsAt = &resetsAt
			}
		}
		// A model's own window stops only that model.
		if model == "" && entry.UsedPercent >= 100 {
			quota.LimitReached = true
		}
		quota.Windows = append(quota.Windows, entry)
	}
	add(limits.FiveHour, 5*60, "")
	add(limits.SevenDay, 7*24*60, "")
	for _, window := range limits.ModelScoped {
		add(&window.usageWindow, 7*24*60, window.DisplayName)
	}
	return quota
}

// PurgeBotAgentAuth implements external.BotAgentAuthPurger: disconnecting
// removes the login Claude Code keeps in the Agent's config directory, once
// every process of the Agent, a sign-in's included, has exited.
func (d *Driver) PurgeBotAgentAuth(ctx context.Context, botID, botAgentID string) error {
	return d.procs.SignOut(ctx, d.workspaces, RuntimeType, botID, botAgentID, path.Join(claudeHome(botAgentID), credentialsFile), nil)
}

// accountConfig is the configuration of an Agent that signs in to a Claude
// account; any other Agent is refused.
func (d *Driver) accountConfig(ctx context.Context, botID, botAgentID string) (claudecfg.Config, error) {
	cfg, err := d.resolveAgentConfig(ctx, botID, botAgentID)
	if err != nil {
		return cfg, err
	}
	if cfg.Auth != claudecfg.AuthClaudeAccount {
		return cfg, external.CredentialError(agentcredential.ErrIncompatible)
	}
	return cfg, nil
}

func agentProcesses(botID, botAgentID string) func(agentproc.Key) bool {
	return func(k agentproc.Key) bool {
		return k.Runtime == RuntimeType && k.BotID == botID && k.BotAgentID == botAgentID
	}
}
