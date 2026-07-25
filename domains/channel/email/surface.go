package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// ListMailbox lists messages from a provider mailbox via the registered adapter.
func (s *Service) ListMailbox(ctx context.Context, providerID string, page, pageSize int) ([]InboundEmail, int, error) {
	providerName, config, err := s.ProviderConfig(ctx, providerID)
	if err != nil {
		return nil, 0, err
	}
	if config == nil {
		config = make(map[string]any)
	}
	config["_provider_id"] = providerID
	reader, err := s.registry.GetMailboxReader(providerName)
	if err != nil {
		return nil, 0, err
	}
	items, total, err := reader.ListMailbox(ctx, config, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	return fromPortInboundSlice(items), total, nil
}

// ReadMailbox reads one mailbox message by UID via the registered adapter.
func (s *Service) ReadMailbox(ctx context.Context, providerID string, uid uint32) (*InboundEmail, error) {
	providerName, config, err := s.ProviderConfig(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if config == nil {
		config = make(map[string]any)
	}
	config["_provider_id"] = providerID
	reader, err := s.registry.GetMailboxReader(providerName)
	if err != nil {
		return nil, err
	}
	item, err := reader.ReadMailbox(ctx, config, uid)
	if err != nil {
		return nil, err
	}
	return fromPortInboundPtr(item), nil
}

// HandleWebhook processes an inbound webhook for a provider and returns the parsed email.
func (s *Service) HandleWebhook(ctx context.Context, providerID string, r *http.Request) (*InboundEmail, error) {
	provider, err := s.GetProviderInternal(ctx, providerID)
	if err != nil {
		return nil, err
	}
	receiver, err := s.registry.GetWebhookReceiver(ProviderName(provider.Provider))
	if err != nil {
		return nil, err
	}
	var configMap map[string]any
	configBytes, _ := json.Marshal(provider.Config)
	_ = json.Unmarshal(configBytes, &configMap)
	inbound, err := receiver.HandleWebhook(ctx, configMap, r)
	if err != nil {
		return nil, err
	}
	if inbound == nil {
		return nil, errors.New("webhook produced no email")
	}
	out := fromPortInbound(*inbound)
	return &out, nil
}
