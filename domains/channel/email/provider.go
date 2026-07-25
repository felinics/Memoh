package email

import (
	"fmt"
	"sync"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

// Registry holds all registered email adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[emailport.ProviderName]emailport.Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[emailport.ProviderName]emailport.Adapter)}
}

func (r *Registry) Register(a emailport.Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Type()] = a
}

func (r *Registry) Get(name ProviderName) (emailport.Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[emailport.ProviderName(name)]
	if !ok {
		return nil, fmt.Errorf("email adapter not found: %s", name)
	}
	return a, nil
}

func (r *Registry) GetSender(name ProviderName) (emailport.Sender, error) {
	a, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	s, ok := a.(emailport.Sender)
	if !ok {
		return nil, fmt.Errorf("email adapter %s does not support sending", name)
	}
	return s, nil
}

func (r *Registry) GetReceiver(name ProviderName) (emailport.Receiver, error) {
	a, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	recv, ok := a.(emailport.Receiver)
	if !ok {
		return nil, fmt.Errorf("email adapter %s does not support receiving", name)
	}
	return recv, nil
}

func (r *Registry) GetMailboxReader(name ProviderName) (emailport.MailboxReader, error) {
	a, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	reader, ok := a.(emailport.MailboxReader)
	if !ok {
		return nil, fmt.Errorf("email adapter %s does not support mailbox reading", name)
	}
	return reader, nil
}

func (r *Registry) GetWebhookReceiver(name ProviderName) (emailport.WebhookReceiver, error) {
	a, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	receiver, ok := a.(emailport.WebhookReceiver)
	if !ok {
		return nil, fmt.Errorf("email adapter %s does not support webhooks", name)
	}
	return receiver, nil
}

func (r *Registry) ListMeta() []ProviderMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	metas := make([]ProviderMeta, 0, len(r.adapters))
	for _, a := range r.adapters {
		metas = append(metas, fromPortMeta(a.Meta()))
	}
	return metas
}
