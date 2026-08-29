package generic

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	mail "github.com/wneessen/go-mail"

	"github.com/felinics/memoh/internal/email"
)

const ProviderName email.ProviderName = "generic"

type Adapter struct {
	logger *slog.Logger
}

func New(log *slog.Logger) *Adapter {
	return &Adapter{logger: log.With(slog.String("adapter", "generic"))}
}

func (*Adapter) Type() email.ProviderName { return ProviderName }

func (*Adapter) Meta() email.ProviderMeta {
	return email.ProviderMeta{
		Provider:    string(ProviderName),
		DisplayName: "Generic (SMTP/IMAP)",
		ConfigSchema: email.ConfigSchema{
			Fields: []email.FieldSchema{
				{Key: "username", Type: "string", Title: "Username", Required: true, Example: "user@gmail.com", Order: 1},
				{Key: "password", Type: "secret", Title: "Password", Required: true, Order: 2},
				{Key: "smtp_host", Type: "string", Title: "SMTP Host", Required: true, Example: "smtp.gmail.com", Order: 3},
				{Key: "smtp_port", Type: "number", Title: "SMTP Port", Required: true, Example: 587, Order: 4},
				{Key: "smtp_security", Type: "enum", Title: "SMTP Security", Enum: []string{"tls", "starttls", "none"}, Example: "starttls", Order: 5},
				{Key: "imap_host", Type: "string", Title: "IMAP Host", Required: true, Example: "imap.gmail.com", Order: 6},
				{Key: "imap_port", Type: "number", Title: "IMAP Port", Required: true, Example: 993, Order: 7},
				{Key: "imap_security", Type: "enum", Title: "IMAP Security", Enum: []string{"tls", "starttls", "none"}, Example: "tls", Order: 8},
				{Key: "poll_interval_seconds", Type: "number", Title: "Poll Interval (seconds)", Description: "Fallback poll interval when IDLE is not supported", Example: 300, Order: 9},
			},
		},
	}
}

func (*Adapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	for _, key := range []string{"smtp_host", "imap_host", "username", "password"} {
		if v, _ := raw[key].(string); strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	if _, ok := raw["smtp_port"]; !ok {
		raw["smtp_port"] = float64(587)
	}
	if _, ok := raw["imap_port"]; !ok {
		raw["imap_port"] = float64(993)
	}
	if _, ok := raw["smtp_security"]; !ok {
		raw["smtp_security"] = "starttls"
	}
	if _, ok := raw["imap_security"]; !ok {
		raw["imap_security"] = "tls"
	}
	if _, ok := raw["poll_interval_seconds"]; !ok {
		raw["poll_interval_seconds"] = float64(300)
	}
	return raw, nil
}

// ---- Sender ----

func (*Adapter) Send(ctx context.Context, config map[string]any, msg email.OutboundEmail) (string, error) {
	host, _ := config["smtp_host"].(string)
	port := intVal(config["smtp_port"], 587)
	username, _ := config["username"].(string)
	password, _ := config["password"].(string)
	smtpSecurity, _ := config["smtp_security"].(string)

	m := mail.NewMsg()
	if err := m.From(username); err != nil {
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

	opts := []mail.Option{
		mail.WithPort(port),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(username),
		mail.WithPassword(password),
	}
	switch smtpSecurity {
	case "tls":
		opts = append(opts, mail.WithSSLPort(false), mail.WithTLSPolicy(mail.TLSMandatory))
	case "starttls":
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	default:
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	}

	client, err := mail.NewClient(host, opts...)
	if err != nil {
		return "", fmt.Errorf("create smtp client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return "", fmt.Errorf("send email: %w", err)
	}

	return m.GetMessageID(), nil
}

// ---- Receiver (IMAP IDLE + poll fallback) ----

func (a *Adapter) StartReceiving(ctx context.Context, config map[string]any, handler email.InboundHandler) (email.Stopper, error) {
	host, _ := config["imap_host"].(string)
	port := intVal(config["imap_port"], 993)
	username, _ := config["username"].(string)
	password, _ := config["password"].(string)
	imapSecurity, _ := config["imap_security"].(string)
	pollInterval := time.Duration(intVal(config["poll_interval_seconds"], 300)) * time.Second

	providerID, _ := config["_provider_id"].(string)

	rctx, cancel := context.WithCancel(ctx) //nolint:gosec // G118: cancel is stored in conn.cancel and called by Stop()
	conn := &imapConn{
		logger:       a.logger,
		host:         host,
		port:         port,
		username:     username,
		password:     password,
		security:     imapSecurity,
		pollInterval: pollInterval,
		providerID:   providerID,
		handler:      handler,
		cancel:       cancel,
	}
	go conn.run(rctx)
	return conn, nil
}

type imapConn struct {
	logger       *slog.Logger
	host         string
	port         int
	username     string
	password     string
	security     string
	pollInterval time.Duration
	providerID   string
	handler      email.InboundHandler
	cancel       context.CancelFunc
	once         sync.Once
	lastUID      imap.UID
}

func (c *imapConn) Stop(_ context.Context) error {
	c.once.Do(func() { c.cancel() })
	return nil
}

func (c *imapConn) run(ctx context.Context) {
	for {
		if err := c.connectAndReceive(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Error("imap connection error, retrying in 30s", slog.Any("error", err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}
}

func (c *imapConn) connectAndReceive(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", c.host, c.port)

	// Channel to receive "new mail" signals from IMAP unilateral notifications
	newMailCh := make(chan struct{}, 1)
	notifyNewMail := func() {
		select {
		case newMailCh <- struct{}{}:
		default:
		}
	}

	opts := &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					notifyNewMail()
				}
			},
		},
	}
	var client *imapclient.Client
	var err error
	switch c.security {
	case "starttls":
		client, err = imapclient.DialStartTLS(addr, opts)
	case "none":
		client, err = imapclient.DialInsecure(addr, opts)
	default:
		client, err = imapclient.DialTLS(addr, opts)
	}
	if err != nil {
		return fmt.Errorf("dial imap (%s): %w", c.security, err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Login(c.username, c.password).Wait(); err != nil {
		return fmt.Errorf("imap login: %w", err)
	}
	defer client.Logout()

	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		return fmt.Errorf("select inbox: %w", err)
	}

	c.logger.Info("imap connected, fetching initial messages", slog.String("host", c.host), slog.Int("port", c.port))
	c.fetchNewMessages(ctx, client)

	idleCmd, idleErr := client.Idle()
	if idleErr != nil {
		c.logger.Warn("IDLE not supported, falling back to polling", slog.Any("error", idleErr))
		return c.pollLoop(ctx, client)
	}
	c.logger.Info("IDLE mode active")

	// Even with IDLE, periodically check for new mail as a safety net
	// (some servers accept IDLE but don't push EXISTS notifications)
	checkInterval := c.pollInterval
	if checkInterval > 2*time.Minute {
		checkInterval = 2 * time.Minute
	}

	for {
		select {
		case <-ctx.Done():
			_ = idleCmd.Close()
			return nil
		case <-newMailCh:
			c.logger.Info("IDLE: new mail notification received")
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

func (c *imapConn) pollLoop(ctx context.Context, client *imapclient.Client) error {
	for {
		c.fetchNewMessages(ctx, client)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(c.pollInterval):
		}
	}
}

func (c *imapConn) fetchNewMessages(ctx context.Context, client *imapclient.Client) {
	// Use UID range to find messages newer than the last processed one.
	// This works regardless of \Seen flag, so other clients reading mail won't interfere.
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

		// On first run, just record the highest UID without processing old messages
		if isFirstRun {
			continue
		}

		inbound := c.bufToInbound(buf)
		if inbound == nil {
			continue
		}
		processed++

		if err := c.handler(ctx, c.providerID, *inbound); err != nil {
			c.logger.Error("inbound handler failed", slog.Any("error", err))
		}
	}

	c.logger.Info("imap fetch completed", slog.Int("processed", processed), slog.Uint64("last_uid", uint64(c.lastUID)))
}

func (*imapConn) bufToInbound(buf *imapclient.FetchMessageBuffer) *email.InboundEmail {
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
		MessageID:  env.MessageID,
		From:       from,
		To:         to,
		Subject:    env.Subject,
		BodyText:   bodyText,
		ReceivedAt: env.Date,
	}
}

// ---- MailboxReader (on-demand IMAP queries) ----

func (*Adapter) dialIMAP(config map[string]any) (*imapclient.Client, error) {
	host, _ := config["imap_host"].(string)
	port := intVal(config["imap_port"], 993)
	username, _ := config["username"].(string)
	password, _ := config["password"].(string)
	security, _ := config["imap_security"].(string)

	addr := fmt.Sprintf("%s:%d", host, port)
	opts := &imapclient.Options{TLSConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}

	var client *imapclient.Client
	var err error
	switch security {
	case "starttls":
		client, err = imapclient.DialStartTLS(addr, opts)
	case "none":
		client, err = imapclient.DialInsecure(addr, opts)
	default:
		client, err = imapclient.DialTLS(addr, opts)
	}
	if err != nil {
		return nil, err
	}
	if err := client.Login(username, password).Wait(); err != nil {
		_ = client.Close()
		return nil, err
	}
	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (a *Adapter) ListMailbox(_ context.Context, config map[string]any, page, pageSize int) ([]email.InboundEmail, int, error) {
	client, err := a.dialIMAP(config)
	if err != nil {
		return nil, 0, fmt.Errorf("imap connect: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Get total message count via STATUS
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

	// Calculate sequence range for the requested page (newest first)
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

	fetchOpts := &imap.FetchOptions{
		Envelope: true,
		UID:      true,
	}
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

	// Reverse to show newest first
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	return results, total, nil
}

func (a *Adapter) ReadMailbox(_ context.Context, config map[string]any, uid uint32) (*email.InboundEmail, error) {
	client, err := a.dialIMAP(config)
	if err != nil {
		return nil, fmt.Errorf("imap connect: %w", err)
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

	env := buf.Envelope
	from := ""
	if len(env.From) > 0 {
		from = env.From[0].Addr()
	}
	var to []string
	for _, addr := range env.To {
		to = append(to, addr.Addr())
	}
	var bodyText string
	if len(buf.BodySection) > 0 {
		bodyText = string(buf.BodySection[0].Bytes)
	}

	return &email.InboundEmail{
		MessageID:  fmt.Sprintf("%d", buf.UID),
		From:       from,
		To:         to,
		Subject:    env.Subject,
		BodyText:   bodyText,
		ReceivedAt: env.Date,
	}, nil
}

func intVal(v any, fallback int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return fallback
	}
}
