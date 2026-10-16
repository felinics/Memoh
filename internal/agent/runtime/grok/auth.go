package grok

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/grok/grokcfg"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// authFileName is where Grok keeps a Grok account login inside its home.
// The driver treats the file as opaque: it hands Grok the stored bytes and
// stores back whatever Grok rewrote, so the login survives a rebuilt
// workspace volume.
const authFileName = "auth.json"

var _ external.BotAgentAuthPurger = (*Driver)(nil)

func authFilePath(botAgentID string) string {
	return path.Join(grokHome(botAgentID), authFileName)
}

// prepareHome makes Grok's home agree with the Agent's auth mode before a
// process reads it. Grok prefers a stored login over XAI_API_KEY, so a login
// left from an earlier mode would silently win over the configured key.
//
// loginInUse says a process started from the same credential is still
// running. The file is then what that process last refreshed, and the stored
// copy can only be older.
func prepareHome(ctx context.Context, client *bridge.Client, botAgentID string, cfg Config, loginInUse bool) error {
	if err := client.Mkdir(ctx, grokHome(botAgentID)); err != nil {
		return errs.WrapDependency(err, "")
	}
	switch {
	case cfg.Auth != grokcfg.AuthGrokAccount:
		return deleteLogin(ctx, client, botAgentID)
	case loginInUse:
		return nil
	default:
		return errs.WrapDependency(client.WriteFile(ctx, authFilePath(botAgentID), []byte(cfg.AuthJSON)), "")
	}
}

func deleteLogin(ctx context.Context, client *bridge.Client, botAgentID string) error {
	err := client.DeleteFile(ctx, authFilePath(botAgentID), false)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil
	}
	return errs.WrapDependency(err, "")
}

// readLogin returns the login Grok currently holds in the Agent's home.
func readLogin(ctx context.Context, client *bridge.Client, botAgentID string) (string, error) {
	return readLoginFile(ctx, client, authFilePath(botAgentID))
}

// readLoginFile reads one of Grok's login files. Grok rewrites the file when
// it refreshes its tokens, so a read may meet a half-written file; only a
// complete JSON object counts.
func readLoginFile(ctx context.Context, client *bridge.Client, filePath string) (string, error) {
	response, err := client.ReadFile(ctx, filePath, 0, 0)
	if err != nil {
		return "", errs.WrapDependency(err, "")
	}
	content := strings.TrimSpace(response.GetContent())
	var entries map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &entries); err != nil || len(entries) == 0 {
		return "", errs.NewDependency("grok auth.json does not contain a login")
	}
	return content, nil
}

// loginStore is the slice of the credential service write-back needs.
type loginStore interface {
	ResolveForBotAgent(ctx context.Context, botID, botAgentID string) (agentcredential.ResolvedCredential, error)
	UpdateSecretCAS(ctx context.Context, credentialID string, expectedVersion int64, secret map[string]string, accountMetadata map[string]any, expiresAt *time.Time) (agentcredential.PublicCredential, error)
}

// loginSync writes the login Grok refreshes back to the credential a process
// was started from. The stored copy is what the next process starts on, so a
// refresh that never reached it would bring back tokens Grok has replaced.
//
// It is tied to that one credential on purpose. A sign-in made while the
// process runs stores a new credential, and this process's file is then the
// older login: writing it to whatever the Agent holds now would undo the
// sign-in.
type loginSync struct {
	store      loginStore
	read       func(context.Context) (string, error)
	botID      string
	botAgentID string
	logger     *slog.Logger

	mu sync.Mutex
	// stored is the credential as this process last knew it.
	stored agentcredential.ResolvedCredential
	// ended stops write-back for good: the Agent holds another credential, or
	// the process is gone and the file belongs to whichever starts next.
	ended bool
}

// finish is a closed process's last write-back.
func (l *loginSync) finish() {
	if l == nil {
		return
	}
	l.sync(context.Background())
	l.mu.Lock()
	l.ended = true
	l.mu.Unlock()
}

func (l *loginSync) sync(ctx context.Context) {
	if l == nil {
		return
	}
	// A turn that was stopped still has its refresh to store.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginSyncTimeout)
	defer cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	for range 2 {
		// Read again after losing the version: the sibling that moved it may
		// have stored a later refresh than the one read before.
		current, err := l.read(ctx)
		if err != nil {
			l.logger.WarnContext(ctx, "read refreshed grok login failed", slog.Any("error", err))
			return
		}
		if current == strings.TrimSpace(l.stored.Secret["auth_json"]) {
			return
		}
		updated, err := l.store.UpdateSecretCAS(ctx, l.stored.ID, l.stored.CredentialVersion,
			map[string]string{"auth_json": current}, l.stored.AccountMetadata, l.stored.ExpiresAt)
		if err == nil {
			l.stored.CredentialVersion = updated.CredentialVersion
			l.stored.Secret = map[string]string{"auth_json": current}
			return
		}
		if !errors.Is(err, agentcredential.ErrNotFound) {
			l.logger.WarnContext(ctx, "persist refreshed grok login failed", slog.Any("error", err))
			return
		}
		// The version moved. A sibling process of this Agent sharing the home
		// may have stored the same refresh first, which is the same
		// credential one version on; a new sign-in is a different one.
		latest, err := l.store.ResolveForBotAgent(ctx, l.botID, l.botAgentID)
		if err != nil {
			// Not knowing is not a replacement: the next write-back asks again.
			l.logger.WarnContext(ctx, "resolve grok credential for write-back failed", slog.Any("error", err))
			return
		}
		if latest.ID != l.stored.ID {
			l.ended = true
			return
		}
		l.stored = latest
	}
}

// PurgeBotAgentAuth implements external.BotAgentAuthPurger: a removed
// credential must not keep working through the file Grok still holds.
func (d *Driver) PurgeBotAgentAuth(ctx context.Context, botID, botAgentID string) error {
	key := serverKey(botID, botAgentID)
	// A start under way has read the credential and writes its login only
	// later. It finishes first, so its process is among the ones closed here.
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
	if credential, err := d.credentials.GetForBotAgent(ctx, botID, botAgentID); err == nil {
		agent.purged = credential.ID
	}
	d.servers.Recycle(key)
	// A process left running would write the login back after it is deleted.
	for _, srv := range running {
		_ = srv.Close() //nolint:contextcheck // teardown is not the request's to cancel.
	}
	client, err := d.bridges.MCPClient(ctx, botID)
	if err == nil {
		err = deleteLogin(ctx, client, botAgentID)
	}
	if err != nil {
		// The login file and the credential both remain: nothing was
		// disconnected, so starts must not be refused for it.
		agent.purged = ""
	}
	return err
}

// credentialFor returns the stored credential the Agent's auth mode calls for.
func credentialFor(cfg Config, credential agentcredential.ResolvedCredential) (Config, error) {
	switch cfg.Auth {
	case grokcfg.AuthGrokAccount:
		if credential.AuthKind != agentcredential.AuthKindXAIGrokOAuth {
			return Config{}, external.CredentialError(agentcredential.ErrIncompatible)
		}
		cfg.AuthJSON = credential.Secret["auth_json"]
	default:
		if credential.AuthKind != agentcredential.AuthKindXAIAPIKey {
			return Config{}, external.CredentialError(agentcredential.ErrIncompatible)
		}
		cfg.APIKey = credential.Secret["api_key"]
	}
	return cfg, nil
}
