package grok

import (
	"context"
	"errors"
	"path"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp"
	"github.com/felinics/memoh/internal/agent/runtime/external/grok/grokcfg"
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
		return external.Account{}, err
	}
	if cfg.Auth != grokcfg.AuthGrokAccount {
		return external.Account{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	proc, release, err := d.host.Process(ctx, acp.PromptInput{BotID: botID, BotAgentID: botAgentID})
	if err != nil {
		return external.Account{}, err
	}
	defer release()
	method, _ := proc.Conn().AgentMeta()["defaultAuthMethodId"].(string)
	return external.Account{SignedIn: method == cachedTokenAuthMethod}, nil
}

// PurgeBotAgentAuth implements external.BotAgentAuthPurger: disconnecting
// removes the login Grok holds in the Agent's home. A sign-in under way would
// put a login back, so it ends first.
func (d *Driver) PurgeBotAgentAuth(ctx context.Context, botID, botAgentID string) error {
	return d.procs.SignOut(ctx, d.workspaces, RuntimeType, botID, botAgentID, authFilePath(botAgentID), func() {
		d.cancelAgentSignIns(botID, botAgentID)
	})
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
