package grok

import (
	"context"
	"errors"
	"path"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/grok/grokcfg"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// authFileName is where Grok keeps a Grok account login inside its home.
// Grok writes and refreshes the file itself; the driver only deletes it, on
// disconnect or when the API key mode must not meet it.
const authFileName = "auth.json"

// cachedTokenAuthMethod is the auth method Grok offers, and makes its default,
// when its home holds a login that still works.
const cachedTokenAuthMethod = "cached_token"

var (
	_ external.BotAgentAuthPurger = (*Driver)(nil)
	_ external.AccountReader      = (*Driver)(nil)
)

func authFilePath(botAgentID string) string {
	return path.Join(grokHome(botAgentID), authFileName)
}

// prepareHome makes Grok's home agree with the Agent's auth mode before a
// process reads it. Grok prefers a stored login over XAI_API_KEY, so a login
// left from the Grok account mode would silently win over the configured key.
func prepareHome(ctx context.Context, client *bridge.Client, botAgentID string, cfg Config) error {
	if err := client.Mkdir(ctx, grokHome(botAgentID)); err != nil {
		return errs.WrapDependency(err, "")
	}
	if cfg.Auth == grokcfg.AuthGrokAccount {
		return nil
	}
	return deleteLogin(ctx, client, botAgentID)
}

func deleteLogin(ctx context.Context, client *bridge.Client, botAgentID string) error {
	err := client.DeleteFile(ctx, authFilePath(botAgentID), false)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil
	}
	return errs.WrapDependency(err, "")
}

// Account implements external.AccountReader. Grok checks, and refreshes, its
// login when it starts, and offers its cached login as the default auth
// method only when that login works.
func (d *Driver) Account(ctx context.Context, botID, botAgentID string) (external.Account, error) {
	cfg, err := d.agentConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.Account{}, external.Unavailable(err)
	}
	if cfg.Auth != grokcfg.AuthGrokAccount {
		return external.Account{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	srv, release, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return external.Account{}, serverFailure(err)
	}
	defer release()
	method, _ := srv.conn.AgentMeta()["defaultAuthMethodId"].(string)
	return external.Account{SignedIn: method == cachedTokenAuthMethod}, nil
}

// PurgeBotAgentAuth implements external.BotAgentAuthPurger: disconnecting
// removes the login Grok holds in the Agent's home.
func (d *Driver) PurgeBotAgentAuth(ctx context.Context, botID, botAgentID string) error {
	key := serverKey(botID, botAgentID)
	agent := d.processes(key)
	select {
	case agent.starting <- struct{}{}:
		defer func() { <-agent.starting }()
	case <-ctx.Done():
		return ctx.Err()
	}
	running := d.runningServers(key)
	for _, srv := range running {
		if srv.hasActiveTurns() {
			return external.Fail(external.FailureCredentialBusy, nil)
		}
	}
	// A sign-in under way would put a login back after the Agent let go of
	// its own, so it ends here.
	d.cancelAgentSignIns(botID, botAgentID)
	d.servers.Recycle(key)
	// A process left running could still refresh, and so rewrite, the login.
	for _, srv := range running {
		_ = srv.Close() //nolint:contextcheck // teardown is not the request's to cancel.
	}
	client, err := d.bridges.MCPClient(ctx, botID)
	if err != nil {
		return err
	}
	return deleteLogin(ctx, client, botAgentID)
}

// apiKeyFor returns the configuration with the API key an API key Agent
// stores. A Grok account Agent stores nothing: Grok keeps its login.
func (d *Driver) apiKeyFor(ctx context.Context, botID, botAgentID string, cfg Config) (Config, error) {
	if cfg.Auth == grokcfg.AuthGrokAccount {
		return cfg, nil
	}
	credential, err := d.credentials.ResolveForBotAgent(ctx, botID, botAgentID)
	if err != nil {
		return Config{}, external.CredentialError(err)
	}
	if credential.AuthKind != agentcredential.AuthKindXAIAPIKey {
		return Config{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	cfg.APIKey = credential.Secret["api_key"]
	return cfg, nil
}
