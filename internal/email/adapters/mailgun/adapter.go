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

	"github.com/felinics/memoh/internal/email"
)

const (
	InboundModeWebhook = "webhook"
	InboundModePoll    = "poll"
)

const ProviderName email.ProviderName = "mailgun"

type Adapter struct {
	logger *slog.Logger
}

func New(log *slog.Logger) *Adapter {
	return &Adapter{logger: log.With(slog.String("adapter", "mailgun"))}
}

func (*Adapter) Type() email.ProviderName { return ProviderName }

func (*Adapter) Meta() email.ProviderMeta {
	return email.ProviderMeta{
		Provider:    string(ProviderName),
		DisplayName: "Mailgun",
		ConfigSchema: email.ConfigSchema{
			Fields: []email.FieldSchema{
				{Key: "domain", Type: "string", Title: "Domain", Required: true, Example: "mg.example.com", Order: 1},
				{Key: "api_key", Type: "secret", Title: "API Key", Required: true, Order: 2},
				{Key: "region", Type: "enum", Title: "Region", Enum: []string{"us", "eu"}, Example: "us", Order: 3},
				{Key: "inbound_mode", Type: "enum", Title: "Inbound Mode", Description: "webhook requires public IP; poll does not", Enum: []string{InboundModeWebhook, InboundModePoll}, Example: InboundModePoll, Order: 4},
				{Key: "webhook_signing_key", Type: "secret", Title: "Webhook Signing Key", Description: "Required for webhook mode", Order: 5},
				{Key: "poll_interval_seconds", Type: "number", Title: "Poll Interval (seconds)", Description: "For poll mode (minimum 15)", Example: 30, Order: 6},
			},
		},
	}
}

func (*Adapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	for _, key := range []string{"domain", "api_key"} {
		if v, _ := raw[key].(string); strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	mode, _ := raw["inbound_mode"].(string)
	if mode == "" {
		raw["inbound_mode"] = InboundModePoll
	}
	if mode == InboundModeWebhook {
		if v, _ := raw["webhook_signing_key"].(string); strings.TrimSpace(v) == "" {
			return nil, errors.New("webhook_signing_key is required for webhook mode")
		}
	}
	if _, ok := raw["region"]; !ok {
		raw["region"] = "us"
	}
	if _, ok := raw["poll_interval_seconds"]; !ok {
		raw["poll_interval_seconds"] = float64(30)
	}
	return raw, nil
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

func (*Adapter) Send(ctx context.Context, config map[string]any, msg email.OutboundEmail) (string, error) {
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

func (a *Adapter) StartReceiving(ctx context.Context, config map[string]any, handler email.InboundHandler) (email.Stopper, error) {
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
	}
	go conn.run(rctx)
	return conn, nil
}

// ---- WebhookReceiver ----

func (*Adapter) HandleWebhook(_ context.Context, config map[string]any, r *http.Request) (*email.InboundEmail, error) {
	signingKey, _ := config["webhook_signing_key"].(string)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
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

	return &email.InboundEmail{
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
	handler      email.InboundHandler
	cancel       context.CancelFunc
	once         sync.Once
	lastTime     time.Time
}

func (c *pollConn) Stop(_ context.Context) error {
	c.once.Do(func() { c.cancel() })
	return nil
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
			c.logger.Error("mailgun events poll failed", slog.Any("error", err))
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

		inbound := email.InboundEmail{
			MessageID:  stored.Message.Headers.MessageID,
			From:       stored.Message.Headers.From,
			To:         []string{stored.Message.Headers.To},
			Subject:    stored.Message.Headers.Subject,
			ReceivedAt: ts,
		}

		if err := c.handler(ctx, c.providerID, inbound); err != nil {
			c.logger.Error("inbound handler failed", slog.Any("error", err))
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
	_ email.Adapter         = (*Adapter)(nil)
	_ email.Sender          = (*Adapter)(nil)
	_ email.Receiver        = (*Adapter)(nil)
	_ email.WebhookReceiver = (*Adapter)(nil)
)
