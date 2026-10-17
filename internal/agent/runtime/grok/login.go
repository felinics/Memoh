package grok

import (
	"context"
	"log/slog"
	"net/url"
	"path"
	"time"

	"github.com/google/uuid"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/agentprocess"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/grok/grokcfg"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// A Grok account signs in inside the Agent's workspace: ACP authenticate runs
// Grok's device-code flow and Grok writes its login file itself, so the file
// is always in the shape Grok produces. Memoh copies the finished file into
// the credential store and starts the Agent's next process from it.
//
// The sign-in runs on a process of its own with an empty home. On a home
// that already holds a login, authenticate answers from that login without
// asking anyone, and the Agent's current login must stay in place until the
// new one is stored. authenticate returns only once the user has confirmed,
// so the URL they must open is read through Grok's own request while it
// waits.
const (
	// authMethodID is the interactive sign-in Grok advertises. The other
	// method it may list, cached_token, names a login it already holds.
	authMethodID = "grok.com"
	// signInURLMethod answers with the sign-in's URL once one is under way,
	// and with nothing before.
	signInURLMethod = "_x.ai/auth/get_url"
	// signInURLTimeout bounds the wait for Grok to obtain the device code.
	signInURLTimeout = 20 * time.Second
	signInURLPoll    = 500 * time.Millisecond
	// signInTTL ends a sign-in nobody polled or cancelled to its end, with
	// the process and login file it holds; Grok's device codes expire well
	// within it.
	signInTTL = 30 * time.Minute
	// signInHomePrefix is where a sign-in's throwaway home lives; it holds
	// nothing but what Grok writes during the sign-in.
	signInHomePrefix = "/tmp/memoh-grok-signin-"
)

var _ external.DeviceLoginProvider = (*Driver)(nil)

// signIn is one account sign-in under way on its own Grok process.
type signIn struct {
	id         string
	botID      string
	botAgentID string
	home       string
	client     *bridge.Client
	proc       *agentprocess.Process
	conn       *acpclient.Conn
	cancel     context.CancelFunc
	// expiry ends the sign-in at signInTTL; endSignIn stops it.
	expiry *time.Timer
	// done closes when authenticate has answered.
	done chan struct{}
	// meta is the account Grok signed in, as authenticate reported it.
	meta map[string]any
	err  error
}

// StartDeviceLogin implements external.DeviceLoginProvider.
func (d *Driver) StartDeviceLogin(ctx context.Context, botID, botAgentID string) (external.DeviceLoginStart, error) {
	cfg, err := d.agentConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStart{}, external.Unavailable(err)
	}
	if cfg.Auth != grokcfg.AuthGrokAccount {
		return external.DeviceLoginStart{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	s, err := d.launchSignIn(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStart{}, serverFailure(err)
	}
	start, err := s.begin(ctx)
	if err != nil {
		d.endSignIn(ctx, s)
		return external.DeviceLoginStart{}, err
	}
	d.signInsMu.Lock()
	d.signIns[s.id] = s
	s.expiry = time.AfterFunc(signInTTL, func() { //nolint:contextcheck // the expiry has no request to answer.
		d.endSignIn(context.Background(), s)
	})
	d.signInsMu.Unlock()
	return start, nil
}

// launchSignIn starts a Grok process on an empty home of its own.
func (d *Driver) launchSignIn(ctx context.Context, botID, botAgentID string) (*signIn, error) {
	info, err := d.bridges.WorkspaceInfo(ctx, botID)
	if err != nil {
		return nil, err
	}
	if err := external.RequireContainerWorkspace(info, RuntimeType); err != nil {
		return nil, err
	}
	client, err := d.bridges.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	launcher, err := d.resolveLauncher(ctx, botID)
	if err != nil {
		return nil, err
	}
	s := &signIn{id: uuid.NewString(), botID: botID, botAgentID: botAgentID, client: client, done: make(chan struct{})}
	s.home = signInHomePrefix + s.id
	if err := client.Mkdir(ctx, s.home); err != nil {
		return nil, errs.WrapDependency(err, "")
	}
	// The sign-in outlives the request that started it.
	s.proc, s.conn, err = launch(context.WithoutCancel(ctx), client, s.home, "", launcher, d.logger)
	if err != nil {
		s.removeHome(ctx)
		return nil, err
	}
	return s, nil
}

// PollDeviceLogin implements external.DeviceLoginProvider.
func (d *Driver) PollDeviceLogin(ctx context.Context, ownerUserID, botID, botAgentID, loginID string) (external.DeviceLoginStatus, error) {
	s := d.signIn(loginID, botAgentID)
	if s == nil {
		return external.DeviceLoginStatus{Status: "unknown"}, nil
	}
	select {
	case <-s.done:
	default:
		return external.DeviceLoginStatus{Status: "pending"}, nil
	}
	if s.err != nil {
		d.endSignIn(ctx, s)
		d.logger.WarnContext(ctx, "grok sign-in failed", slog.String("bot_id", botID), slog.Any("error", s.err))
		return external.DeviceLoginStatus{Status: "error", Error: s.err.Error()}, nil
	}
	// A store that failed on the way leaves the sign-in for the next poll.
	if err := d.storeSignIn(ctx, ownerUserID, s); err != nil {
		return external.DeviceLoginStatus{}, err
	}
	d.endSignIn(ctx, s)
	// The Agent's process still runs on the login it was started from; the
	// next turn starts one from the stored credential.
	d.servers.Recycle(serverKey(botID, botAgentID))
	return external.DeviceLoginStatus{Status: "success"}, nil
}

// storeSignIn copies the login Grok wrote into the credential store,
// replacing the Agent's credential.
func (d *Driver) storeSignIn(ctx context.Context, ownerUserID string, s *signIn) error {
	content, err := readLoginFile(ctx, s.client, path.Join(s.home, authFileName))
	if err != nil {
		return err
	}
	meta := map[string]any{}
	// The label reads "Grok <email>", as the Codex one reads the account.
	if email, _ := s.meta["email"].(string); email != "" {
		meta["account_id"] = email
	}
	if team, _ := s.meta["team_id"].(string); team != "" {
		meta["team_id"] = team
	}
	_, err = d.credentials.AttachToBotAgent(ctx, ownerUserID, s.botID, s.botAgentID, agentcredential.CreateRequest{
		Provider:        agentcredential.ProviderXAI,
		AuthKind:        agentcredential.AuthKindXAIGrokOAuth,
		Secret:          map[string]string{"auth_json": content},
		AccountMetadata: meta,
	})
	if err != nil {
		return external.CredentialError(err)
	}
	return nil
}

// CancelDeviceLogin implements external.DeviceLoginProvider. Grok keeps
// polling the device code until it expires, so the process goes with the
// sign-in; the Agent's own process is untouched.
func (d *Driver) CancelDeviceLogin(ctx context.Context, _, botAgentID, loginID string) error {
	if s := d.signIn(loginID, botAgentID); s != nil {
		d.endSignIn(ctx, s)
	}
	return nil
}

func (d *Driver) signIn(loginID, botAgentID string) *signIn {
	d.signInsMu.Lock()
	defer d.signInsMu.Unlock()
	s := d.signIns[loginID]
	if s == nil || s.botAgentID != botAgentID {
		return nil
	}
	return s
}

// endSignIn forgets a sign-in and takes its process and home with it.
func (d *Driver) endSignIn(ctx context.Context, s *signIn) {
	d.signInsMu.Lock()
	delete(d.signIns, s.id)
	if s.expiry != nil {
		s.expiry.Stop()
	}
	d.signInsMu.Unlock()
	s.close()
	s.removeHome(ctx)
}

// closeSignIns ends every sign-in for Server shutdown.
func (d *Driver) closeSignIns() {
	d.signInsMu.Lock()
	all := make([]*signIn, 0, len(d.signIns))
	for _, s := range d.signIns {
		all = append(all, s)
	}
	d.signInsMu.Unlock()
	for _, s := range all {
		d.endSignIn(context.Background(), s) //nolint:contextcheck // shutdown has no request to cancel.
	}
}

// begin starts the sign-in and returns what the user must open.
func (s *signIn) begin(ctx context.Context) (external.DeviceLoginStart, error) {
	authCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	go func() {
		defer close(s.done)
		s.meta, s.err = s.conn.Authenticate(authCtx, authMethodID)
	}()
	return s.awaitURL(ctx)
}

// awaitURL waits for Grok to publish the device URL of the sign-in that has
// just started. The user code travels in the URL's query.
func (s *signIn) awaitURL(ctx context.Context) (external.DeviceLoginStart, error) {
	deadline := time.Now().Add(signInURLTimeout)
	for {
		var answer struct {
			AuthURL          string `json:"auth_url"`
			ExternalProvider bool   `json:"external_provider"`
			Mode             string `json:"mode"`
		}
		if err := s.conn.Call(ctx, signInURLMethod, map[string]any{"methodId": authMethodID}, &answer); err != nil {
			return external.DeviceLoginStart{}, err
		}
		if answer.AuthURL != "" {
			if answer.ExternalProvider || answer.Mode != "device" {
				return external.DeviceLoginStart{}, errs.NewDependency("grok sign-in is not a device-code flow", slog.String("mode", answer.Mode))
			}
			return external.DeviceLoginStart{LoginID: s.id, UserCode: userCode(answer.AuthURL), VerificationURL: answer.AuthURL}, nil
		}
		select {
		case <-s.done:
			if s.err != nil {
				return external.DeviceLoginStart{}, s.err
			}
			return external.DeviceLoginStart{}, errs.NewDependency("grok ended the sign-in before asking the user")
		case <-ctx.Done():
			return external.DeviceLoginStart{}, ctx.Err()
		case <-time.After(signInURLPoll):
		}
		if time.Now().After(deadline) {
			return external.DeviceLoginStart{}, errs.NewDependency("grok did not provide a sign-in URL")
		}
	}
}

func (s *signIn) close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.proc != nil {
		_ = s.proc.Close()
	}
}

// removeHome deletes the sign-in's home, login file included. A cancelled
// request must still take it away: it holds a login.
func (s *signIn) removeHome(ctx context.Context) {
	if s.client == nil || s.home == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginSyncTimeout)
	defer cancel()
	_ = s.client.DeleteFile(ctx, s.home, true)
}

func userCode(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("user_code")
}
