package email

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

const (
	serviceProviderID = "a6692f30-51c2-4f6a-acb8-948f86349a24"
	serviceUserID     = "e388086c-085d-43dc-8d1e-c32bc4002795"
)

type providerStoreFake struct {
	emailport.ProviderStore
	record         emailport.ProviderRecord
	createInput    emailport.CreateProviderInput
	createCalls    int
	findByNameErr  error
	findByNameUser string
	findByNameName string
}

func (f *providerStoreFake) CreateProvider(_ context.Context, input emailport.CreateProviderInput) (emailport.ProviderRecord, error) {
	f.createCalls++
	f.createInput = input
	record := f.record
	record.UserID = input.UserID
	record.Name = input.Name
	record.Provider = input.Provider
	record.Config = append([]byte(nil), input.Config...)
	return record, nil
}

func (f *providerStoreFake) FindProviderByName(_ context.Context, userID, name string) (emailport.ProviderRecord, error) {
	f.findByNameUser = userID
	f.findByNameName = name
	return f.record, f.findByNameErr
}

type normalizingAdapter struct{}

func (normalizingAdapter) Type() emailport.ProviderName { return "gmail" }

func (normalizingAdapter) Meta() emailport.ProviderMeta {
	return emailport.ProviderMeta{Provider: "gmail", DisplayName: "Gmail"}
}

func (normalizingAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	normalized := make(map[string]any, len(raw)+1)
	for key, value := range raw {
		normalized[key] = value
	}
	normalized["normalized"] = true
	return normalized, nil
}

func TestServiceUsesProviderPortAndPreservesRegistryBehavior(t *testing.T) {
	t.Parallel()
	store := &providerStoreFake{record: emailport.ProviderRecord{ID: serviceProviderID}}
	registry := NewRegistry()
	registry.Register(normalizingAdapter{})
	service := NewService(slog.New(slog.DiscardHandler), store, nil, registry)

	response, err := service.CreateProvider(t.Context(), serviceUserID, CreateProviderRequest{
		Name: " Primary ", Provider: "gmail",
		Config: map[string]any{"client_id": "id", "client_secret": "secret"},
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if store.createInput.Name != "Primary" || store.createInput.Provider != "gmail" {
		t.Fatalf("CreateProvider input = %+v", store.createInput)
	}
	var persisted map[string]any
	if err := json.Unmarshal(store.createInput.Config, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["normalized"] != true {
		t.Fatalf("persisted config = %#v, want normalized marker", persisted)
	}
	if _, ok := response.Config["client_id"]; ok {
		t.Fatalf("response config leaked client_id: %#v", response.Config)
	}
	if _, ok := response.Config["client_secret"]; ok {
		t.Fatalf("response config leaked client_secret: %#v", response.Config)
	}
}

func TestEnsureDefaultGmailProviderCreatesOnlyWhenMissing(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.Register(normalizingAdapter{})
	store := &providerStoreFake{
		record:        emailport.ProviderRecord{ID: serviceProviderID},
		findByNameErr: emailport.ErrNotFound,
	}
	service := NewService(slog.New(slog.DiscardHandler), store, nil, registry)

	if err := service.EnsureDefaultGmailProvider(t.Context(), serviceUserID); err != nil {
		t.Fatalf("EnsureDefaultGmailProvider() error = %v", err)
	}
	if store.findByNameUser != serviceUserID || store.findByNameName != DefaultGmailProviderName {
		t.Fatalf("FindProviderByName args = %q, %q", store.findByNameUser, store.findByNameName)
	}
	if store.createCalls != 1 || store.createInput.Name != DefaultGmailProviderName {
		t.Fatalf("CreateProvider calls = %d, input = %+v", store.createCalls, store.createInput)
	}

	store.findByNameErr = nil
	if err := service.EnsureDefaultGmailProvider(t.Context(), serviceUserID); err != nil {
		t.Fatalf("EnsureDefaultGmailProvider(existing) error = %v", err)
	}
	if store.createCalls != 1 {
		t.Fatalf("CreateProvider calls = %d, want 1", store.createCalls)
	}
}

func TestEnsureDefaultGmailProviderPreservesInfrastructureError(t *testing.T) {
	t.Parallel()
	errUnavailable := errors.New("database unavailable")
	store := &providerStoreFake{findByNameErr: errUnavailable}
	service := NewService(slog.New(slog.DiscardHandler), store, nil, NewRegistry())

	err := service.EnsureDefaultGmailProvider(t.Context(), serviceUserID)
	if !errors.Is(err, errUnavailable) {
		t.Fatalf("EnsureDefaultGmailProvider() error = %v, want %v", err, errUnavailable)
	}
}
