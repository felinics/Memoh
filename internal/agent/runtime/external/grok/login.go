package grok

import (
	"context"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/grok/grokcfg"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/errs"
)

// A Grok account signs in on the Agent's own process, in the Agent's home:
// ACP authenticate runs Grok's device-code flow, and Grok writes and keeps
// its login file there. On a home that already holds a login, authenticate
// answers from that login without asking anyone, so signing in again first
// removes it. authenticate returns only once the user has confirmed, so the
// URL they must open is read through Grok's own request while it waits.
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
	// the process it holds; Grok's device codes expire well within it.
	signInTTL = 30 * time.Minute
)

var _ external.DeviceLoginProvider = (*Driver)(nil)

// signIn is one account sign-in under way on the Agent's process.
type signIn struct {
	id         string
	botID      string
	botAgentID string
	srv        *server
	release    func()
	cancel     context.CancelFunc
	// expiry ends the sign-in at signInTTL; endSignIn stops it.
	expiry *time.Timer
	// done closes when authenticate has answered.
	done chan struct{}
	err  error
	// ended makes endSignIn safe to repeat.
	ended sync.Once
}

// StartDeviceLogin implements external.DeviceLoginProvider.
func (d *Driver) StartDeviceLogin(ctx context.Context, botID, botAgentID string) (external.DeviceLoginStart, error) {
	release, err := d.lockSignIn(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStart{}, err
	}
	defer release()
	cfg, err := d.agentConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStart{}, external.Unavailable(err)
	}
	if cfg.Auth != grokcfg.AuthGrokAccount {
		return external.DeviceLoginStart{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	// A login already in the home would answer for the new one.
	if err := d.PurgeBotAgentAuth(ctx, botID, botAgentID); err != nil {
		return external.DeviceLoginStart{}, err
	}
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStart{}, serverFailure(err)
	}
	s := &signIn{id: uuid.NewString(), botID: botID, botAgentID: botAgentID, srv: srv, release: releaseServer, done: make(chan struct{})}
	start, err := s.begin(ctx)
	if err != nil {
		d.endSignIn(s)
		return external.DeviceLoginStart{}, err
	}
	d.signInsMu.Lock()
	d.signIns[s.id] = s
	s.expiry = time.AfterFunc(signInTTL, func() { //nolint:contextcheck // the expiry has no request to answer.
		_ = d.CancelDeviceLogin(context.Background(), botID, botAgentID, s.id)
	})
	d.signInsMu.Unlock()
	return start, nil
}

// PollDeviceLogin implements external.DeviceLoginProvider.
func (d *Driver) PollDeviceLogin(ctx context.Context, _, botID, botAgentID, loginID string) (external.DeviceLoginStatus, error) {
	release, err := d.lockSignIn(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStatus{}, err
	}
	defer release()
	s := d.signIn(loginID, botAgentID)
	if s == nil {
		return external.DeviceLoginStatus{Status: "unknown"}, nil
	}
	select {
	case <-s.done:
	default:
		return external.DeviceLoginStatus{Status: "pending"}, nil
	}
	d.endSignIn(s)
	if s.err != nil {
		d.logger.WarnContext(ctx, "grok sign-in failed", slog.String("bot_id", botID), slog.Any("error", s.err))
		return external.DeviceLoginStatus{Status: "error", Error: s.err.Error()}, nil
	}
	return external.DeviceLoginStatus{Status: "success"}, nil
}

// CancelDeviceLogin implements external.DeviceLoginProvider.
func (d *Driver) CancelDeviceLogin(ctx context.Context, botID, botAgentID, loginID string) error {
	release, err := d.lockSignIn(ctx, botID, botAgentID)
	if err != nil {
		return err
	}
	defer release()
	if s := d.signIn(loginID, botAgentID); s != nil {
		d.endSignIn(s)
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

// endSignIn forgets a sign-in and lets its process go. Grok keeps polling a
// device code until it expires, and a process that signed in still reports
// the login state it started with, so either way the Agent's next work starts
// a new one.
func (d *Driver) endSignIn(s *signIn) {
	s.ended.Do(func() {
		d.signInsMu.Lock()
		delete(d.signIns, s.id)
		if s.expiry != nil {
			s.expiry.Stop()
		}
		d.signInsMu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		d.procs.MarkBroken(serverKey(s.botID, s.botAgentID), s.srv)
		s.release()
	})
}

// begin starts the sign-in and returns what the user must open.
func (s *signIn) begin(ctx context.Context) (external.DeviceLoginStart, error) {
	authCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	go func() {
		defer close(s.done)
		_, s.err = s.srv.conn.Authenticate(authCtx, authMethodID)
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
		if err := s.srv.conn.Call(ctx, signInURLMethod, map[string]any{"methodId": authMethodID}, &answer); err != nil {
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

func userCode(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("user_code")
}

// lockSignIn covers the whole login lifecycle operation, not only map writes.
// A cancelled tab must not race a poll that is finishing the same sign-in.
func (d *Driver) lockSignIn(ctx context.Context, botID, botAgentID string) (func(), error) {
	key := serverKey(botID, botAgentID)
	d.signInsMu.Lock()
	gate := d.signInGates[key]
	if gate == nil {
		gate = make(chan struct{}, 1)
		d.signInGates[key] = gate
	}
	d.signInsMu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// cancelAgentSignIns ends every sign-in of the Agent. A new sign-in and a
// disconnect both call it; endSignIn is safe to repeat. Other Agents are
// unaffected.
func (d *Driver) cancelAgentSignIns(botID, botAgentID string) {
	d.signInsMu.Lock()
	var previous []*signIn
	for _, s := range d.signIns {
		if s.botID == botID && s.botAgentID == botAgentID {
			previous = append(previous, s)
		}
	}
	d.signInsMu.Unlock()
	for _, s := range previous {
		d.endSignIn(s)
	}
}
