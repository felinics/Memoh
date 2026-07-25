package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

type fakeWebhookAdapter struct {
	channelType        ChannelType
	ackDisabledWebhook bool
	calls              []struct {
		cfg    ChannelConfig
		method string
	}
}

func (a *fakeWebhookAdapter) Type() ChannelType { return a.channelType }

func (a *fakeWebhookAdapter) Descriptor() Descriptor {
	return Descriptor{
		Type:               a.channelType,
		DisplayName:        strings.ToUpper(a.channelType.String()),
		AckDisabledWebhook: a.ackDisabledWebhook,
	}
}

func (a *fakeWebhookAdapter) HandleWebhook(ctx context.Context, cfg ChannelConfig, handler InboundHandler, r *http.Request, w http.ResponseWriter) error {
	a.calls = append(a.calls, struct {
		cfg    ChannelConfig
		method string
	}{cfg: cfg, method: r.Method})
	if handler != nil {
		if err := handler(ctx, cfg, InboundMessage{
			Channel: cfg.ChannelType,
			BotID:   cfg.BotID,
			Message: Message{Format: MessageFormatPlain, Text: "hello"},
		}); err != nil {
			return err
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type fakeWebhookStore struct {
	configs []ChannelConfig
	err     error
}

func (s *fakeWebhookStore) ListConfigsByType(_ context.Context, channelType ChannelType) ([]ChannelConfig, error) {
	if s.err != nil {
		return nil, s.err
	}
	items := make([]ChannelConfig, 0, len(s.configs))
	for _, item := range s.configs {
		if item.ChannelType == channelType {
			items = append(items, item)
		}
	}
	return items, nil
}

type fakeWebhookManager struct {
	registry *Registry
	calls    []struct {
		cfg ChannelConfig
		msg InboundMessage
	}
}

func (m *fakeWebhookManager) HandleInbound(_ context.Context, cfg ChannelConfig, msg InboundMessage) error {
	m.calls = append(m.calls, struct {
		cfg ChannelConfig
		msg InboundMessage
	}{cfg: cfg, msg: msg})
	return nil
}

func (m *fakeWebhookManager) Registry() *Registry { return m.registry }

func TestGenericWebhookHandlerDispatchesToAdapter(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	adapter := &fakeWebhookAdapter{channelType: ChannelType("testhook")}
	registry.MustRegister(adapter)

	store := &fakeWebhookStore{
		configs: []ChannelConfig{{
			ID:          "cfg-1",
			BotID:       "bot-1",
			ChannelType: adapter.channelType,
		}},
	}
	manager := &fakeWebhookManager{registry: registry}
	h := NewWebhookServerHandler(nil, (*Store)(nil), (*Manager)(nil))
	h.store = store
	h.manager = manager
	h.registry = registry

	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/testhook/webhook/cfg-1", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("platform", "config_id")
	c.SetParamValues("testhook", "cfg-1")

	if err := h.Handle(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}
	if len(adapter.calls) != 1 {
		t.Fatalf("expected adapter to be called once, got %d", len(adapter.calls))
	}
	if len(manager.calls) != 1 {
		t.Fatalf("expected inbound manager to be called once, got %d", len(manager.calls))
	}
}

func TestGenericWebhookHandlerRejectsUnknownConfig(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.MustRegister(&fakeWebhookAdapter{channelType: ChannelType("testhook")})
	manager := &fakeWebhookManager{registry: registry}
	h := NewWebhookServerHandler(nil, (*Store)(nil), (*Manager)(nil))
	h.store = &fakeWebhookStore{}
	h.manager = manager
	h.registry = registry

	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/testhook/webhook/missing", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("platform", "config_id")
	c.SetParamValues("testhook", "missing")

	err := h.Handle(c)
	if err == nil {
		t.Fatal("expected not found error")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status code: %d", httpErr.Code)
	}
}

func TestGenericWebhookHandlerAcksDisabledConfigWhenDescriptorRequestsIt(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	adapter := &fakeWebhookAdapter{channelType: ChannelType("ackhook"), ackDisabledWebhook: true}
	registry.MustRegister(adapter)

	store := &fakeWebhookStore{
		configs: []ChannelConfig{{
			ID:          "cfg-1",
			BotID:       "bot-1",
			ChannelType: adapter.channelType,
			Disabled:    true,
		}},
	}
	manager := &fakeWebhookManager{registry: registry}
	h := NewWebhookServerHandler(nil, (*Store)(nil), (*Manager)(nil))
	h.store = store
	h.manager = manager
	h.registry = registry

	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/ackhook/webhook/cfg-1", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("platform", "config_id")
	c.SetParamValues("ackhook", "cfg-1")

	if err := h.Handle(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}
	if len(adapter.calls) != 0 {
		t.Fatalf("expected adapter not to be called, got %d calls", len(adapter.calls))
	}
	if len(manager.calls) != 0 {
		t.Fatalf("expected inbound manager not to be called, got %d calls", len(manager.calls))
	}
}

func TestGenericWebhookHandlerRejectsDisabledConfigByDefault(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	adapter := &fakeWebhookAdapter{channelType: ChannelType("testhook")}
	registry.MustRegister(adapter)

	store := &fakeWebhookStore{
		configs: []ChannelConfig{{
			ID:          "cfg-1",
			BotID:       "bot-1",
			ChannelType: adapter.channelType,
			Disabled:    true,
		}},
	}
	manager := &fakeWebhookManager{registry: registry}
	h := NewWebhookServerHandler(nil, (*Store)(nil), (*Manager)(nil))
	h.store = store
	h.manager = manager
	h.registry = registry

	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/testhook/webhook/cfg-1", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("platform", "config_id")
	c.SetParamValues("testhook", "cfg-1")

	err := h.Handle(c)
	if err == nil {
		t.Fatal("expected disabled config error")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status code: %d", httpErr.Code)
	}
	if len(adapter.calls) != 0 {
		t.Fatalf("expected adapter not to be called, got %d calls", len(adapter.calls))
	}
}
