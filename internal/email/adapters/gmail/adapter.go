package gmail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	sasl "github.com/emersion/go-sasl"
	mail "github.com/wneessen/go-mail"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/felinics/memoh/internal/email"
	"github.com/felinics/memoh/internal/oauthclients"
)

const ProviderName email.ProviderName = "gmail"

const (
	gmailScope     = "https://mail.google.com/"
	oauthClientRef = "gmail"
)

type Adapter struct {
	logger       *slog.Logger
	tokenStore   email.OAuthTokenStore
	oauthClients oauthclients.Resolver
}

func New(log *slog.Logger, tokenStore email.OAuthTokenStore, oauthClients oauthclients.Resolver) *Adapter {
	return &Adapter{
		logger:       log.With(slog.String("adapter", "gmail")),
		tokenStore:   tokenStore,
		oauthClients: oauthClients,
	}
}

func (*Adapter) Type() email.ProviderName { return ProviderName }

func (*Adapter) Meta() email.ProviderMeta {
	return email.ProviderMeta{
		Provider:    string(ProviderName),
		DisplayName: "Gmail (OAuth2)",
		ConfigSchema: email.ConfigSchema{
			Fields: []email.FieldSchema{
				{Key: "email_address", Type: "string", Title: "Gmail Address", Required: true, Example: "you@gmail.com", Order: 1},
			},
		},
	}
}

func (*Adapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	clean := make(map[string]any, len(raw))
	for key, value := range raw {
		if key == "client_id" || key == "client_secret" {
			continue
		}
		clean[key] = value
	}
	if len(clean) == 0 {
		return clean, nil
	}
	if v, _ := clean["email_address"].(string); strings.TrimSpace(v) == "" {
		return nil, errors.New("email_address is required")
	}
	return clean, nil
}

func (a *Adapter) HasOAuthClient() bool {
	client, ok := a.oauthClient()
	return ok && strings.TrimSpace(client.ClientID) != "" && strings.TrimSpace(client.ClientSecret) != ""
}

func (a *Adapter) EffectiveRedirectURI(fallback string) string {
	client, ok := a.oauthClient()
	if ok && strings.TrimSpace(client.RedirectURI) != "" {
		return strings.TrimSpace(client.RedirectURI)
	}
	return fallback
}

func (a *Adapter) AuthorizeURL(redirectURI, state string) (string, error) {
	cfg, err := a.oauth2Config(redirectURI)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")), nil
}

func (a *Adapter) ExchangeCode(ctx context.Context, config map[string]any, providerID, code, redirectURI string) error {
	emailAddress, _ := config["email_address"].(string)

	cfg, err := a.oauth2Config(redirectURI)
	if err != nil {
		return err
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("gmail token exchange: %w", err)
	}
	return a.tokenStore.Save(ctx, email.OAuthToken{
		ProviderID:   providerID,
		EmailAddress: emailAddress,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.Expiry,
		Scope:        gmailScope,
	})
}

// ---- Sender ----

func (a *Adapter) Send(ctx context.Context, config map[string]any, msg email.OutboundEmail) (string, error) {
	providerID, _ := config["_provider_id"].(string)
	if providerID == "" {
		return "", errors.New("gmail adapter: _provider_id missing from config")
	}

	accessToken, emailAddr, err := a.validToken(ctx, config, providerID)
	if err != nil {
		return "", err
	}

	m := mail.NewMsg()
	if err := m.From(emailAddr); err != nil {
		return "", fmt.Errorf("set from: %w", err)
	}
	if err := m.To(msg.To...); err != nil {
		return "", fmt.Errorf("set to: %w", err)
	}
	m.Subject(msg.Subject)
	if msg.HTML {
		m.SetBodyString(mail.TypeTextHTML, msg.Body)
	} else {
		m.SetBodyString(mail.TypeTextPlain, msg.Body)
	}
	m.SetMessageID()

	client, err := mail.NewClient(
		"smtp.gmail.com",
		mail.WithPort(587),
		mail.WithTLSPolicy(mail.TLSMandatory),
		mail.WithSMTPAuth(mail.SMTPAuthXOAUTH2),
		mail.WithUsername(emailAddr),
		mail.WithPassword(accessToken),
	)
	if err != nil {
		return "", fmt.Errorf("create gmail smtp client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return "", fmt.Errorf("gmail send: %w", err)
	}
	return m.GetMessageID(), nil
}

// ---- Receiver (IMAP IDLE + poll fallback) ----

func (a *Adapter) StartReceiving(ctx context.Context, config map[string]any, handler email.InboundHandler) (email.Stopper, error) {
	providerID, _ := config["_provider_id"].(string)
	rctx, cancel := context.WithCancel(ctx) //nolint:gosec // G118: cancel is stored in conn.cancel and called by Stop()
	conn := &gmailImapConn{
		adapter:    a,
		config:     config,
		providerID: providerID,
		handler:    handler,
		cancel:     cancel,
		logger:     a.logger,
	}
	go conn.run(rctx)
	return conn, nil
}

type gmailImapConn struct {
	adapter    *Adapter
	config     map[string]any
	providerID string
	handler    email.InboundHandler
	cancel     context.CancelFunc
	once       sync.Once
	lastUID    imap.UID
	logger     *slog.Logger
}

func (c *gmailImapConn) Stop(_ context.Context) error {
	c.once.Do(func() { c.cancel() })
	return nil
}

func (c *gmailImapConn) run(ctx context.Context) {
	for {
		if err := c.connectAndReceive(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Error("gmail imap error, retrying in 60s", slog.Any("error", err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(60 * time.Second):
			}
		}
	}
}

func (c *gmailImapConn) connectAndReceive(ctx context.Context) error {
	client, err := c.adapter.dialIMAP(ctx, c.config)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	newMailCh := make(chan struct{}, 1)
	notifyNewMail := func() {
		select {
		case newMailCh <- struct{}{}:
		default:
		}
	}

	_ = notifyNewMail

	c.logger.Info("gmail imap connected, fetching initial messages")
	c.fetchNewMessages(ctx, client)

	idleCmd, idleErr := client.Idle()
	if idleErr != nil {
		c.logger.Warn("gmail IDLE not available, falling back to polling", slog.Any("error", idleErr))
		return c.pollLoop(ctx, client)
	}
	c.logger.Info("gmail IDLE mode active")

	checkInterval := 2 * time.Minute

	for {
		select {
		case <-ctx.Done():
			_ = idleCmd.Close()
			return nil
		case <-newMailCh:
			_ = idleCmd.Close()
			c.fetchNewMessages(ctx, client)
			idleCmd, idleErr = client.Idle()
			if idleErr != nil {
				return c.pollLoop(ctx, client)
			}
		case <-time.After(checkInterval):
			_ = idleCmd.Close()
			c.fetchNewMessages(ctx, client)
			idleCmd, idleErr = client.Idle()
			if idleErr != nil {
				return c.pollLoop(ctx, client)
			}
		}
	}
}

func (c *gmailImapConn) pollLoop(ctx context.Context, client *imapclient.Client) error {
	for {
		c.fetchNewMessages(ctx, client)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Minute):
		}
	}
}

func (c *gmailImapConn) fetchNewMessages(ctx context.Context, client *imapclient.Client) {
	var uidSet imap.UIDSet
	if c.lastUID > 0 {
		uidSet.AddRange(c.lastUID+1, 0)
	} else {
		uidSet.AddRange(1, 0)
	}

	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}
	fetchCmd := client.Fetch(uidSet, fetchOpts)
	defer func() { _ = fetchCmd.Close() }()

	isFirstRun := c.lastUID == 0
	processed := 0

	for {
		msgData := fetchCmd.Next()
		if msgData == nil {
			break
		}
		buf, err := msgData.Collect()
		if err != nil || buf.Envelope == nil {
			continue
		}
		if buf.UID > c.lastUID {
			c.lastUID = buf.UID
		}
		if isFirstRun {
			continue
		}
		inbound := bufToInbound(buf)
		if inbound == nil {
			continue
		}
		processed++
		if err := c.handler(ctx, c.providerID, *inbound); err != nil {
			c.logger.Error("inbound handler failed", slog.Any("error", err))
		}
	}

	c.logger.Info("gmail imap fetch completed", slog.Int("processed", processed), slog.Uint64("last_uid", uint64(c.lastUID)))
}

// ---- MailboxReader ----

func (a *Adapter) ListMailbox(ctx context.Context, config map[string]any, page, pageSize int) ([]email.InboundEmail, int, error) {
	client, err := a.dialIMAP(ctx, config)
	if err != nil {
		return nil, 0, fmt.Errorf("gmail imap connect: %w", err)
	}
	defer func() { _ = client.Close() }()

	statusData, err := client.Status("INBOX", &imap.StatusOptions{NumMessages: true}).Wait()
	if err != nil {
		return nil, 0, fmt.Errorf("imap status: %w", err)
	}
	var total int
	if statusData.NumMessages != nil {
		total = int(*statusData.NumMessages)
	}
	if total == 0 {
		return nil, 0, nil
	}

	end := total - (page * pageSize)
	start := end - pageSize + 1
	if start < 1 {
		start = 1
	}
	if end < 1 {
		return nil, total, nil
	}

	seqSet := imap.SeqSet{}
	if start > math.MaxUint32 || end > math.MaxUint32 {
		return nil, 0, fmt.Errorf("mail sequence range out of bounds: start=%d end=%d", start, end)
	}
	seqSet.AddRange(uint32(start), uint32(end)) //nolint:gosec // bounds checked above

	fetchOpts := &imap.FetchOptions{Envelope: true, UID: true}
	fetchCmd := client.Fetch(seqSet, fetchOpts)
	defer func() { _ = fetchCmd.Close() }()

	var results []email.InboundEmail
	for {
		msgData := fetchCmd.Next()
		if msgData == nil {
			break
		}
		buf, err := msgData.Collect()
		if err != nil || buf.Envelope == nil {
			continue
		}
		env := buf.Envelope
		from := ""
		if len(env.From) > 0 {
			from = env.From[0].Addr()
		}
		results = append(results, email.InboundEmail{
			MessageID:  fmt.Sprintf("%d", buf.UID),
			From:       from,
			Subject:    env.Subject,
			ReceivedAt: env.Date,
		})
	}

	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	return results, total, nil
}

func (a *Adapter) ReadMailbox(ctx context.Context, config map[string]any, uid uint32) (*email.InboundEmail, error) {
	client, err := a.dialIMAP(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("gmail imap connect: %w", err)
	}
	defer func() { _ = client.Close() }()

	uidSet := imap.UIDSet{}
	uidSet.AddNum(imap.UID(uid))

	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}
	fetchCmd := client.Fetch(uidSet, fetchOpts)
	defer func() { _ = fetchCmd.Close() }()

	msgData := fetchCmd.Next()
	if msgData == nil {
		return nil, fmt.Errorf("email not found: UID %d", uid)
	}
	buf, err := msgData.Collect()
	if err != nil || buf.Envelope == nil {
		return nil, fmt.Errorf("failed to parse email UID %d", uid)
	}

	return bufToInbound(buf), nil
}

// ---- helpers ----

func (a *Adapter) dialIMAP(ctx context.Context, config map[string]any) (*imapclient.Client, error) {
	providerID, _ := config["_provider_id"].(string)
	if providerID == "" {
		return nil, errors.New("gmail adapter: _provider_id missing from config")
	}

	accessToken, emailAddr, err := a.validToken(ctx, config, providerID)
	if err != nil {
		return nil, err
	}

	opts := &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: "imap.gmail.com", MinVersion: tls.VersionTLS12},
	}
	client, err := imapclient.DialTLS("imap.gmail.com:993", opts)
	if err != nil {
		return nil, fmt.Errorf("dial imap.gmail.com: %w", err)
	}

	saslClient := sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
		Username: emailAddr,
		Token:    accessToken,
	})
	if err := client.Authenticate(saslClient); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("gmail imap xoauth2: %w", err)
	}

	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("select inbox: %w", err)
	}

	return client, nil
}

func (a *Adapter) validToken(ctx context.Context, config map[string]any, providerID string) (accessToken, emailAddr string, err error) {
	stored, err := a.tokenStore.Get(ctx, providerID)
	if err != nil {
		return "", "", fmt.Errorf("gmail: no oauth token found (run OAuth authorization first): %w", err)
	}

	emailAddr = stored.EmailAddress
	if emailAddr == "" {
		emailAddr, _ = config["email_address"].(string)
	}

	if stored.AccessToken == "" || (!stored.ExpiresAt.IsZero() && time.Until(stored.ExpiresAt) < 2*time.Minute) {
		newTok, refreshErr := a.refresh(ctx, config, stored.RefreshToken)
		if refreshErr != nil {
			return "", "", fmt.Errorf("gmail token refresh: %w", refreshErr)
		}
		_ = a.tokenStore.Save(ctx, email.OAuthToken{
			ProviderID:   providerID,
			EmailAddress: emailAddr,
			AccessToken:  newTok.AccessToken,
			RefreshToken: newTok.RefreshToken,
			ExpiresAt:    newTok.Expiry,
			Scope:        gmailScope,
		})
		return newTok.AccessToken, emailAddr, nil
	}

	return stored.AccessToken, emailAddr, nil
}

func (a *Adapter) refresh(ctx context.Context, _ map[string]any, refreshToken string) (*oauth2.Token, error) {
	cfg, err := a.oauth2Config("")
	if err != nil {
		return nil, err
	}
	src := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	return src.Token()
}

func (a *Adapter) oauth2Config(redirectURI string) (*oauth2.Config, error) {
	client, ok := a.oauthClient()
	if !ok || strings.TrimSpace(client.ClientID) == "" || strings.TrimSpace(client.ClientSecret) == "" {
		return nil, errors.New("gmail oauth client is not configured")
	}
	return &oauth2.Config{
		ClientID:     strings.TrimSpace(client.ClientID),
		ClientSecret: strings.TrimSpace(client.ClientSecret),
		Scopes:       []string{gmailScope},
		Endpoint:     google.Endpoint,
		RedirectURL:  a.EffectiveRedirectURI(redirectURI),
	}, nil
}

func (a *Adapter) oauthClient() (oauthclients.Client, bool) {
	if a.oauthClients == nil {
		return oauthclients.Client{}, false
	}
	return a.oauthClients.Get(oauthClientRef)
}

func bufToInbound(buf *imapclient.FetchMessageBuffer) *email.InboundEmail {
	env := buf.Envelope
	if env == nil {
		return nil
	}
	var bodyText string
	if len(buf.BodySection) > 0 {
		bodyText = string(buf.BodySection[0].Bytes)
	}
	from := ""
	if len(env.From) > 0 {
		from = env.From[0].Addr()
	}
	var to []string
	for _, addr := range env.To {
		to = append(to, addr.Addr())
	}
	return &email.InboundEmail{
		MessageID:  fmt.Sprintf("%d", buf.UID),
		From:       from,
		To:         to,
		Subject:    env.Subject,
		BodyText:   bodyText,
		ReceivedAt: env.Date,
	}
}

var (
	_ email.Adapter       = (*Adapter)(nil)
	_ email.Sender        = (*Adapter)(nil)
	_ email.Receiver      = (*Adapter)(nil)
	_ email.MailboxReader = (*Adapter)(nil)
)
