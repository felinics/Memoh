package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConfigLister lists channel configs for periodic refresh. Used by connection lifecycle.
type ConfigLister interface {
	ListConfigsByType(ctx context.Context, channelType ChannelType) ([]ChannelConfig, error)
}

// ConfigResolver resolves effective configs and user bindings. Used for outbound sending.
type ConfigResolver interface {
	ResolveEffectiveConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, error)
	GetChannelIdentityConfig(ctx context.Context, channelIdentityID string, channelType ChannelType) (ChannelIdentityBinding, error)
}

// BindingStore resolves channel-identity bindings. Used by identity resolution.
type BindingStore interface {
	ResolveChannelIdentityBinding(ctx context.Context, channelType ChannelType, criteria BindingCriteria) (string, error)
}

// ConfigStore is the full persistence interface. Components should depend on smaller
// interfaces above; ConfigStore exists as a convenience for wiring.
type ConfigStore interface {
	ConfigLister
	ConfigResolver
	BindingStore
	UpsertChannelIdentityConfig(ctx context.Context, channelIdentityID string, channelType ChannelType, req UpsertChannelIdentityConfigRequest) (ChannelIdentityBinding, error)
}

// Middleware wraps an InboundHandler to add cross-cutting behavior.
type Middleware func(next InboundHandler) InboundHandler

// ManagerStore is the minimal persistence interface required by Manager.
type ManagerStore interface {
	ConfigLister
	ConfigResolver
}

var errManagerStopped = errors.New("channel manager is stopped")

// ConnectionStatus describes runtime status for one configured channel connection.
type ConnectionStatus struct {
	ConfigID    string      `json:"config_id"`
	BotID       string      `json:"bot_id"`
	ChannelType ChannelType `json:"channel_type"`
	Running     bool        `json:"running"`
	LastError   string      `json:"last_error,omitempty"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// Manager coordinates channel adapters, connection lifecycle, and message dispatch.
// Connection lifecycle lives in connection.go, inbound dispatch in inbound.go,
// and outbound pipeline in outbound.go.
type Manager struct {
	registry        *Registry
	service         ManagerStore
	processor       InboundProcessor
	attachmentStore OutboundAttachmentStore
	refreshInterval time.Duration
	logger          *slog.Logger
	middlewares     []Middleware

	inboundQueue   chan inboundTask
	inboundWorkers int
	inboundOnce    sync.Once
	inboundCtx     context.Context
	inboundCancel  context.CancelFunc
	inboundDone    <-chan struct{}
	refreshOnce    sync.Once
	runCtx         context.Context
	refreshCancel  context.CancelFunc
	refreshDone    <-chan struct{}
	lifecycleMu    sync.Mutex
	mu             sync.Mutex
	stopped        bool
	refreshMu      sync.Mutex
	connections    map[string]*connectionEntry
	connectionMeta map[string]ConnectionStatus
}

// ManagerOption configures a Manager during construction.
type ManagerOption func(*Manager)

// WithInboundQueueSize sets the capacity of the inbound message queue.
// The default is 256. Larger values trade memory for lower drop rate under burst load.
func WithInboundQueueSize(size int) ManagerOption {
	return func(m *Manager) {
		if size > 0 {
			m.inboundQueue = make(chan inboundTask, size)
		}
	}
}

// WithInboundWorkers sets the number of goroutines that process inbound messages
// concurrently. The default is 4.
func WithInboundWorkers(n int) ManagerOption {
	return func(m *Manager) {
		if n > 0 {
			m.inboundWorkers = n
		}
	}
}

// WithRefreshInterval overrides the periodic connection reconcile interval.
// The default is 5 minutes.
func WithRefreshInterval(d time.Duration) ManagerOption {
	return func(m *Manager) {
		if d > 0 {
			m.refreshInterval = d
		}
	}
}

// NewManager creates a Manager with the given logger, registry, config store, and inbound processor.
func NewManager(log *slog.Logger, registry *Registry, service ManagerStore, processor InboundProcessor, opts ...ManagerOption) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if registry == nil {
		registry = NewRegistry()
	}
	m := &Manager{
		registry:        registry,
		service:         service,
		processor:       processor,
		refreshInterval: 5 * time.Minute,
		connections:     map[string]*connectionEntry{},
		connectionMeta:  map[string]ConnectionStatus{},
		logger:          log.With(slog.String("component", "channel")),
		middlewares:     []Middleware{},
		inboundQueue:    make(chan inboundTask, 256),
		inboundWorkers:  4,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Registry returns the adapter registry used by this manager.
func (m *Manager) Registry() *Registry {
	return m.registry
}

// Use appends middleware to the inbound processing chain.
func (m *Manager) Use(mw ...Middleware) {
	m.middlewares = append(m.middlewares, mw...)
}

// SetAttachmentStore wires the shared outbound attachment store used by the
// prepared outbound layer.
func (m *Manager) SetAttachmentStore(store OutboundAttachmentStore) {
	m.attachmentStore = store
}

// RegisterAdapter adds an adapter to the registry and logs the registration.
func (m *Manager) RegisterAdapter(adapter Adapter) {
	m.registerAdapter(adapter)
}

func (m *Manager) registerAdapter(adapter Adapter) bool {
	if adapter == nil {
		return false
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return false
	}
	err := m.registry.Register(adapter)
	m.mu.Unlock()
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("adapter registration failed", slog.String("channel", adapter.Type().String()), slog.Any("error", err))
		}
		return false
	}
	if m.logger != nil {
		m.logger.Info("adapter registered", slog.String("channel", adapter.Type().String()))
	}
	return true
}

// AddAdapter registers an adapter and triggers an immediate refresh for hot-plug support.
func (m *Manager) AddAdapter(ctx context.Context, adapter Adapter) {
	if m.registerAdapter(adapter) && ctx != nil {
		m.refresh(ctx)
	}
}

// RemoveAdapter unregisters an adapter and stops all its active connections.
func (m *Manager) RemoveAdapter(ctx context.Context, channelType ChannelType) {
	m.mu.Lock()
	for id, entry := range m.connections {
		if entry != nil && entry.config.ChannelType == channelType {
			if entry.connection != nil {
				if err := entry.connection.Stop(ctx); err != nil && !errors.Is(err, ErrStopNotSupported) && m.logger != nil {
					m.logger.WarnContext(ctx, "adapter stop failed", slog.String("config_id", id), slog.Any("error", err))
				}
			}
			delete(m.connections, id)
			delete(m.connectionMeta, id)
		}
	}
	m.mu.Unlock()
	m.registry.Unregister(channelType)
}

// Refresh performs a full reconcile of all adapter connections against the DB.
// Prefer EnsureConnection / RemoveConnection for targeted changes after API operations.
// Refresh is mainly used at startup and as a periodic safety net.
func (m *Manager) Refresh(ctx context.Context) {
	if ctx != nil && !m.isStopped() {
		m.refresh(ctx)
	}
}

// Start begins the periodic config refresh loop and inbound worker pool.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.startInboundWorkersLocked(ctx)
	m.refreshOnce.Do(func() {
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan struct{})
		m.lifecycleMu.Lock()
		m.runCtx = runCtx
		m.refreshCancel = cancel
		m.refreshDone = done
		m.lifecycleMu.Unlock()
		go func() {
			defer close(done)
			m.refresh(runCtx)
			ticker := time.NewTicker(m.refreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-runCtx.Done():
					if m.logger != nil {
						m.logger.InfoContext(runCtx, "manager stop")
					}
					return
				case <-ticker.C:
					m.refresh(runCtx)
				}
			}
		}()
	})
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.InfoContext(ctx, "manager start")
	}
}

// Send delivers an outbound message to the specified channel, resolving target and config automatically.
func (m *Manager) Send(ctx context.Context, botID string, channelType ChannelType, req SendRequest) error {
	if m.isStopped() {
		return errManagerStopped
	}
	if m.service == nil {
		return errors.New("channel manager not configured")
	}
	sender, ok := m.registry.GetSender(channelType)
	if !ok {
		return fmt.Errorf("unsupported channel type: %s", channelType)
	}
	config, err := m.service.ResolveEffectiveConfig(ctx, botID, channelType)
	if err != nil {
		return err
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		targetChannelIdentityID := strings.TrimSpace(req.ChannelIdentityID)
		if targetChannelIdentityID == "" {
			return errors.New("target or channel_identity_id is required")
		}
		userCfg, err := m.service.GetChannelIdentityConfig(ctx, targetChannelIdentityID, channelType)
		if err != nil {
			if m.logger != nil {
				m.logger.WarnContext(ctx, "channel binding missing", slog.String("channel", channelType.String()), slog.String("channel_identity_id", targetChannelIdentityID))
			}
			return errors.New("channel binding required")
		}
		target, err = m.registry.ResolveTargetFromUserConfig(channelType, userCfg.Config)
		if err != nil {
			return err
		}
	}
	target, err = m.resolveOutboundTarget(ctx, channelType, config, target)
	if err != nil {
		return err
	}
	if req.Message.IsEmpty() {
		return errors.New("message is required")
	}
	if m.logger != nil {
		m.logger.InfoContext(ctx, "send outbound", slog.String("channel", channelType.String()), slog.String("bot_id", botID))
	}
	policy := m.resolveOutboundPolicy(channelType)
	caps, hasCaps := m.registry.GetOutboundCapabilities(channelType, config, target)
	outbound, err := buildOutboundMessagesWithCaps(OutboundMessage{
		Target:  target,
		Message: req.Message,
	}, policy, caps, hasCaps)
	if err != nil {
		return err
	}
	if err := m.sendAllWithConfig(ctx, sender, config, outbound, policy); err != nil {
		if m.logger != nil {
			m.logger.ErrorContext(ctx, "send outbound failed", slog.String("channel", channelType.String()), slog.String("bot_id", botID), slog.Any("error", err))
		}
		return err
	}
	return nil
}

func (m *Manager) resolveOutboundTarget(ctx context.Context, channelType ChannelType, cfg ChannelConfig, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("target is required")
	}
	if normalized, ok := m.registry.NormalizeTarget(channelType, target); ok {
		target = normalized
	}
	if resolved, ok, err := m.registry.ResolveOutboundTarget(ctx, channelType, cfg, target); err != nil {
		return "", err
	} else if ok {
		target = resolved
	}
	return target, nil
}

// React adds or removes an emoji reaction on a channel message.
func (m *Manager) React(ctx context.Context, botID string, channelType ChannelType, req ReactRequest) error {
	if m.isStopped() {
		return errManagerStopped
	}
	if m.service == nil {
		return errors.New("channel manager not configured")
	}
	reactor, ok := m.registry.GetReactor(channelType)
	if !ok {
		return fmt.Errorf("channel %s does not support reactions", channelType)
	}
	config, err := m.service.ResolveEffectiveConfig(ctx, botID, channelType)
	if err != nil {
		return err
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		return errors.New("target is required for reactions")
	}
	if normalized, ok := m.registry.NormalizeTarget(channelType, target); ok {
		target = normalized
	}
	messageID := strings.TrimSpace(req.MessageID)
	if messageID == "" {
		return errors.New("message_id is required for reactions")
	}
	emoji := strings.TrimSpace(req.Emoji)
	if !req.Remove && emoji == "" {
		return errors.New("emoji is required when adding a reaction")
	}
	if m.logger != nil {
		m.logger.InfoContext(ctx, "react outbound",
			slog.String("channel", channelType.String()),
			slog.String("bot_id", botID),
			slog.String("message_id", messageID),
			slog.Bool("remove", req.Remove),
		)
	}
	if req.Remove {
		return reactor.Unreact(ctx, config, target, messageID, emoji)
	}
	return reactor.React(ctx, config, target, messageID, emoji)
}

// Shutdown cancels the inbound worker pool and stops all active connections.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	m.lifecycleMu.Lock()
	refreshCancel := m.refreshCancel
	refreshDone := m.refreshDone
	inboundCancel := m.inboundCancel
	inboundDone := m.inboundDone
	m.lifecycleMu.Unlock()
	if refreshCancel != nil {
		refreshCancel()
	}
	if inboundCancel != nil {
		inboundCancel()
	}
	var errs []error
	// Reconciliation can create connections, so join it before taking the
	// final connection snapshot. Connections are inbound producers and must be
	// stopped before waiting for the workers that consume their messages.
	if err := waitForManagerTask(ctx, refreshDone); err != nil {
		errs = append(errs, fmt.Errorf("wait for channel refresh: %w", err))
	}
	if err := m.stopAll(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := waitForManagerTask(ctx, inboundDone); err != nil {
		errs = append(errs, fmt.Errorf("wait for channel inbound workers: %w", err))
	}
	return errors.Join(errs...)
}

func (m *Manager) isStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

func waitForManagerTask(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ConnectionStatusesByBot returns observed channel connection statuses for a bot.
func (m *Manager) ConnectionStatusesByBot(botID string) []ConnectionStatus {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return []ConnectionStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]ConnectionStatus, 0, len(m.connectionMeta))
	for _, status := range m.connectionMeta {
		if status.BotID == botID {
			items = append(items, status)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ChannelType == items[j].ChannelType {
			return items[i].ConfigID < items[j].ConfigID
		}
		return items[i].ChannelType < items[j].ChannelType
	})
	return items
}
