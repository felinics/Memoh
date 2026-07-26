package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

// Manager manages the lifecycle of all email receiving connections.
type Manager struct {
	logger  *slog.Logger
	service *Service
	trigger *Trigger
	outbox  *OutboxService

	mu      sync.Mutex
	conns   map[string]emailport.Stopper // provider_id -> stopper
	runCtx  context.Context
	cancel  context.CancelFunc
	started bool
	stopped bool
}

func NewManager(log *slog.Logger, service *Service, trigger *Trigger, outbox *OutboxService) *Manager {
	return &Manager{
		logger:  log.With(slog.String("component", "email_manager")),
		service: service,
		trigger: trigger,
		outbox:  outbox,
		conns:   make(map[string]emailport.Stopper),
	}
}

// Start initializes receiving for all providers that have readable bindings.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.startRuntime(ctx); err != nil {
		return err
	}
	if m.trigger != nil {
		if err := m.trigger.Start(ctx); err != nil {
			m.stopRuntime()
			return err
		}
	}

	providers, err := m.service.ListProvidersInternal(ctx, "")
	if err != nil {
		if m.trigger != nil {
			_ = m.trigger.Shutdown(ctx)
		}
		m.stopRuntime()
		return fmt.Errorf("list email providers: %w", err)
	}

	for _, p := range providers {
		bindings, err := m.service.ListReadableBindingsByProvider(ctx, p.ID)
		if err != nil {
			m.logger.ErrorContext(ctx, "failed to list bindings", slog.String("provider", p.ID), slog.Any("error", err))
			continue
		}
		if len(bindings) == 0 {
			continue
		}
		if err := m.startProvider(p); err != nil {
			m.logger.ErrorContext(ctx, "failed to start provider", slog.String("provider", p.ID), slog.Any("error", err))
		}
	}
	return nil
}

func (m *Manager) startRuntime(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return errors.New("manager is stopped")
	}
	if m.started {
		return nil
	}
	m.runCtx, m.cancel = context.WithCancel(context.WithoutCancel(ctx))
	m.started = true
	return nil
}

func (m *Manager) stopRuntime() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.runCtx = nil
	m.started = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) startProvider(p ProviderResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return errors.New("manager is stopped")
	}
	if !m.started || m.runCtx == nil {
		return errors.New("manager is not started")
	}
	if _, exists := m.conns[p.ID]; exists {
		return nil
	}

	receiver, err := m.service.registry.GetReceiver(ProviderName(p.Provider))
	if err != nil {
		return err
	}

	config := p.Config
	if config == nil {
		config = make(map[string]any)
	}
	config["_provider_id"] = p.ID

	stopper, err := receiver.StartReceiving(m.runCtx, config, func(ctx context.Context, providerID string, mail emailport.InboundEmail) error {
		return m.trigger.HandleInbound(ctx, providerID, fromPortInbound(mail))
	})
	if err != nil {
		return fmt.Errorf("start receiving for %s: %w", p.ID, err)
	}

	m.conns[p.ID] = stopper
	m.logger.InfoContext(m.runCtx, "started email receiving", slog.String("provider_id", p.ID), slog.String("type", p.Provider))
	return nil
}

// RefreshProvider restarts receiving for a specific provider.
func (m *Manager) RefreshProvider(ctx context.Context, providerID string) error {
	m.stopProvider(ctx, providerID)

	p, err := m.service.GetProviderInternal(ctx, providerID)
	if err != nil {
		return err
	}

	bindings, err := m.service.ListReadableBindingsByProvider(ctx, providerID)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}

	return m.startProvider(p)
}

func (m *Manager) stopProvider(ctx context.Context, providerID string) {
	m.mu.Lock()
	stopper, exists := m.conns[providerID]
	if exists {
		delete(m.conns, providerID)
	}
	m.mu.Unlock()

	if exists && stopper != nil {
		if err := stopper.Stop(ctx); err != nil {
			m.logger.ErrorContext(ctx, "failed to stop provider", slog.String("provider_id", providerID), slog.Any("error", err))
		}
	}
}

// Stop gracefully shuts down all receiving connections.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	cancel := m.cancel
	m.cancel = nil
	m.runCtx = nil
	m.started = false
	conns := make(map[string]emailport.Stopper, len(m.conns))
	for k, v := range m.conns {
		conns[k] = v
	}
	m.conns = make(map[string]emailport.Stopper)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	var errs []error
	for id, stopper := range conns {
		if err := stopper.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop email provider %s: %w", id, err))
		}
	}
	if m.trigger != nil {
		if err := m.trigger.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop email triggers: %w", err))
		}
	}
	return errors.Join(errs...)
}

// SendEmail sends an email through the specified provider, recording to outbox.
// If providerID is empty, it falls back to the first writable binding for the bot.
func (m *Manager) SendEmail(ctx context.Context, botID string, providerID string, msg OutboundEmail) (string, error) {
	if providerID == "" {
		binding, err := m.service.GetBotBinding(ctx, botID)
		if err != nil {
			return "", err
		}
		if !binding.CanWrite {
			return "", fmt.Errorf("email write permission denied for bot %s", botID)
		}
		providerID = binding.EmailProviderID
	}

	providerName, config, err := m.service.ProviderConfig(ctx, providerID)
	if err != nil {
		return "", err
	}
	if config == nil {
		config = make(map[string]any)
	}
	config["_provider_id"] = providerID

	sender, err := m.service.registry.GetSender(providerName)
	if err != nil {
		return "", err
	}

	fromAddr, _ := config["username"].(string)

	outboxID, err := m.outbox.Create(ctx, providerID, botID, msg, fromAddr)
	if err != nil {
		return "", fmt.Errorf("record outbox: %w", err)
	}

	messageID, err := sender.Send(ctx, config, toPortOutbound(msg))
	if err != nil {
		_ = m.outbox.MarkFailed(ctx, outboxID, err.Error())
		return "", fmt.Errorf("send email: %w", err)
	}

	_ = m.outbox.MarkSent(ctx, outboxID, messageID)
	return messageID, nil
}
