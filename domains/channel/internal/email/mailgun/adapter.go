package mailgun

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	mg "github.com/mailgun/mailgun-go/v5"
	"github.com/mailgun/mailgun-go/v5/events"

	emailcatalog "github.com/memohai/memoh/domains/channel/internal/email/catalog"
	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

const (
	InboundModeWebhook = emailcatalog.MailgunInboundModeWebhook
	InboundModePoll    = emailcatalog.MailgunInboundModePoll
)

const ProviderName emailport.ProviderName = emailcatalog.ProviderMailgun

var providerDescriptor = emailcatalog.Mailgun()

type Adapter struct {
	logger *slog.Logger
}

func New(log *slog.Logger) *Adapter {
	return &Adapter{logger: log.With(slog.String("adapter", "mailgun"))}
}

func (*Adapter) Type() emailport.ProviderName { return providerDescriptor.Type() }

func (*Adapter) Meta() emailport.ProviderMeta { return providerDescriptor.Meta() }

func (*Adapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return providerDescriptor.NormalizeConfig(raw)
}

func newClient(config map[string]any) *mg.Client {
	apiKey, _ := config["api_key"].(string)
	client := mg.NewMailgun(apiKey)
	region, _ := config["region"].(string)
	if region == "eu" {
		_ = client.SetAPIBase(mg.APIBaseEU)
	}
	return client
}

// ---- Sender ----

func (*Adapter) Send(ctx context.Context, config map[string]any, msg emailport.OutboundEmail) (string, error) {
	client := newClient(config)
	domain, _ := config["domain"].(string)

	from := fmt.Sprintf("noreply@%s", domain)

	m := mg.NewMessage(domain, from, msg.Subject, msg.Body, msg.To...)
	if msg.HTML {
		m.SetHTML(msg.Body)
	}

	resp, err := client.Send(ctx, m)
	if err != nil {
		return "", fmt.Errorf("mailgun send: %w", err)
	}
	return resp.ID, nil
}

// ---- Receiver (poll mode) ----

func (a *Adapter) StartReceiving(ctx context.Context, config map[string]any, handler emailport.InboundHandler) (emailport.Stopper, error) {
	mode, _ := config["inbound_mode"].(string)
	if mode == InboundModeWebhook {
		return &noopStopper{}, nil
	}

	pollInterval := intVal(config["poll_interval_seconds"], 30)
	if pollInterval < 15 {
		pollInterval = 15
	}
	providerID, _ := config["_provider_id"].(string)
	domain, _ := config["domain"].(string)

	rctx, cancel := context.WithCancel(ctx) //nolint:gosec // G118: cancel is stored in conn.cancel and called by Stop()
	conn := &pollConn{
		logger:       a.logger,
		client:       newClient(config),
		domain:       domain,
		pollInterval: time.Duration(pollInterval) * time.Second,
		providerID:   providerID,
		handler:      handler,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go func() {
		defer close(conn.done)
		conn.run(rctx)
	}()
	return conn, nil
}

// ---- WebhookReceiver ----

const maxWebhookBodyBytes = 10 << 20

func (*Adapter) HandleWebhook(_ context.Context, config map[string]any, r *http.Request) (*emailport.InboundEmail, error) {
	signingKey, _ := config["webhook_signing_key"].(string)

	// ParseMultipartForm bounds only the in-memory part of a multipart body,
	// and the urlencoded fallback below is unbounded on its own, so cap the
	// request body itself before either parser reads it.
	r.Body = http.MaxBytesReader(nil, r.Body, maxWebhookBodyBytes)
	if err := r.ParseMultipartForm(maxWebhookBodyBytes); err != nil { //nolint:gosec // G120: MaxBytesReader above caps the body; gosec cannot see it
		if err2 := r.ParseForm(); err2 != nil {
			return nil, fmt.Errorf("parse form: %w", err2)
		}
	}

	timestamp := r.FormValue("timestamp")
	token := r.FormValue("token")
	signature := r.FormValue("signature")
	if signingKey != "" {
		mac := hmac.New(sha256.New, []byte(signingKey))
		mac.Write([]byte(timestamp + token))
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(signature)) {
			return nil, errors.New("webhook signature verification failed")
		}
	}

	toAddrs := strings.Split(r.FormValue("recipient"), ",")
	for i := range toAddrs {
		toAddrs[i] = strings.TrimSpace(toAddrs[i])
	}

	return &emailport.InboundEmail{
		MessageID:  r.FormValue("Message-Id"),
		From:       r.FormValue("sender"),
		To:         toAddrs,
		Subject:    r.FormValue("subject"),
		BodyText:   r.FormValue("body-plain"),
		BodyHTML:   r.FormValue("body-html"),
		ReceivedAt: time.Now(),
	}, nil
}

// ---- Poll connection ----

type pollConn struct {
	logger       *slog.Logger
	client       *mg.Client
	domain       string
	pollInterval time.Duration
	providerID   string
	handler      emailport.InboundHandler
	cancel       context.CancelFunc
	done         chan struct{}
	once         sync.Once
	lastTime     time.Time
}

func (c *pollConn) Stop(ctx context.Context) error {
	c.once.Do(func() { c.cancel() })
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *pollConn) run(ctx context.Context) {
	c.lastTime = time.Now().Add(-1 * time.Hour)
	for {
		c.pollEvents(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.pollInterval):
		}
	}
}

func (c *pollConn) pollEvents(ctx context.Context) {
	opts := &mg.ListEventOptions{
		Begin:  c.lastTime,
		End:    time.Now(),
		Limit:  100,
		Filter: map[string]string{"event": "stored"},
	}

	iter := c.client.ListEvents(c.domain, opts)
	var evts []events.Event
	if !iter.Next(ctx, &evts) {
		if err := iter.Err(); err != nil {
			c.logger.ErrorContext(ctx, "mailgun events poll failed", slog.Any("error", err))
		}
		return
	}

	for _, evt := range evts {
		stored, ok := evt.(*events.Stored)
		if !ok {
			continue
		}

		ts := stored.GetTimestamp()
		if ts.After(c.lastTime) {
			c.lastTime = ts.Add(time.Millisecond)
		}

		inbound := emailport.InboundEmail{
			MessageID:  stored.Message.Headers.MessageID,
			From:       stored.Message.Headers.From,
			To:         []string{stored.Message.Headers.To},
			Subject:    stored.Message.Headers.Subject,
			ReceivedAt: ts,
		}

		if err := c.handler(ctx, c.providerID, inbound); err != nil {
			c.logger.ErrorContext(ctx, "inbound handler failed", slog.Any("error", err))
		}
	}
}

type noopStopper struct{}

func (*noopStopper) Stop(_ context.Context) error { return nil }

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

var (
	_ emailport.Adapter         = (*Adapter)(nil)
	_ emailport.Sender          = (*Adapter)(nil)
	_ emailport.Receiver        = (*Adapter)(nil)
	_ emailport.WebhookReceiver = (*Adapter)(nil)
)
