package gateway

import (
	"context"
	"errors"
	"testing"
)

type fakeLifecycleStore struct {
	resolveFunc func(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, error)
	upsertFunc  func(ctx context.Context, botID string, channelType ChannelType, req UpsertConfigRequest) (ChannelConfig, error)
	statusFunc  func(ctx context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error)
	deleteFunc  func(ctx context.Context, botID string, channelType ChannelType) error
}

func (f *fakeLifecycleStore) ResolveEffectiveConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, error) {
	if f.resolveFunc == nil {
		return ChannelConfig{}, ErrChannelConfigNotFound
	}
	return f.resolveFunc(ctx, botID, channelType)
}

func (f *fakeLifecycleStore) UpsertConfig(ctx context.Context, botID string, channelType ChannelType, req UpsertConfigRequest) (ChannelConfig, error) {
	if f.upsertFunc == nil {
		return ChannelConfig{}, nil
	}
	return f.upsertFunc(ctx, botID, channelType, req)
}

func (f *fakeLifecycleStore) UpdateConfigDisabled(ctx context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error) {
	if f.statusFunc == nil {
		return ChannelConfig{}, ErrChannelConfigNotFound
	}
	return f.statusFunc(ctx, botID, channelType, disabled)
}

func (f *fakeLifecycleStore) DeleteConfig(ctx context.Context, botID string, channelType ChannelType) error {
	if f.deleteFunc == nil {
		return nil
	}
	return f.deleteFunc(ctx, botID, channelType)
}

type fakeConnectionController struct {
	ensureFunc func(ctx context.Context, cfg ChannelConfig) error
	removeFunc func(ctx context.Context, botID string, channelType ChannelType)
}

func (f *fakeConnectionController) EnsureConnection(ctx context.Context, cfg ChannelConfig) error {
	if f.ensureFunc == nil {
		return nil
	}
	return f.ensureFunc(ctx, cfg)
}

func (f *fakeConnectionController) RemoveConnection(ctx context.Context, botID string, channelType ChannelType) {
	if f.removeFunc == nil {
		return
	}
	f.removeFunc(ctx, botID, channelType)
}

func TestLifecycleUpsertDisabledRemovesConnection(t *testing.T) {
	t.Parallel()

	removeCalled := false
	store := &fakeLifecycleStore{
		upsertFunc: func(_ context.Context, botID string, channelType ChannelType, _ UpsertConfigRequest) (ChannelConfig, error) {
			return ChannelConfig{ID: "cfg-1", BotID: botID, ChannelType: channelType, Disabled: true}, nil
		},
	}
	controller := &fakeConnectionController{
		removeFunc: func(_ context.Context, _ string, _ ChannelType) {
			removeCalled = true
		},
	}
	service := NewLifecycle(store, controller)
	disabled := true

	cfg, err := service.UpsertBotChannelConfig(context.Background(), "bot-1", ChannelType("telegram"), UpsertConfigRequest{
		Credentials: map[string]any{"botToken": "x"},
		Disabled:    &disabled,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !cfg.Disabled {
		t.Fatalf("expected disabled config")
	}
	if !removeCalled {
		t.Fatalf("expected remove connection to be called")
	}
}

func TestLifecycleUpsertEnableFailureRollsBackToPrevious(t *testing.T) {
	t.Parallel()

	previous := ChannelConfig{
		ID:          "cfg-prev",
		BotID:       "bot-1",
		ChannelType: ChannelType("telegram"),
		Credentials: map[string]any{"botToken": "old"},
		Disabled:    false,
	}
	newConfig := ChannelConfig{
		ID:          "cfg-new",
		BotID:       "bot-1",
		ChannelType: ChannelType("telegram"),
		Credentials: map[string]any{"botToken": "new"},
		Disabled:    false,
	}
	upsertCalls := 0
	ensureCalls := 0
	store := &fakeLifecycleStore{
		resolveFunc: func(_ context.Context, _ string, _ ChannelType) (ChannelConfig, error) {
			return previous, nil
		},
		upsertFunc: func(_ context.Context, _ string, _ ChannelType, req UpsertConfigRequest) (ChannelConfig, error) {
			upsertCalls++
			if upsertCalls == 1 {
				return newConfig, nil
			}
			if token := ReadString(req.Credentials, "botToken"); token != "old" {
				t.Fatalf("expected rollback credentials old, got %s", token)
			}
			return previous, nil
		},
	}
	controller := &fakeConnectionController{
		ensureFunc: func(_ context.Context, _ ChannelConfig) error {
			ensureCalls++
			if ensureCalls == 1 {
				return errors.New("dial failed")
			}
			return nil
		},
	}
	service := NewLifecycle(store, controller)
	enabled := false

	_, err := service.UpsertBotChannelConfig(context.Background(), "bot-1", ChannelType("telegram"), UpsertConfigRequest{
		Credentials: map[string]any{"botToken": "new"},
		Disabled:    &enabled,
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if upsertCalls != 2 {
		t.Fatalf("expected 2 upsert calls (write + rollback), got %d", upsertCalls)
	}
	if ensureCalls != 2 {
		t.Fatalf("expected 2 ensure calls (new + restore), got %d", ensureCalls)
	}
}

func TestLifecycleUpsertEnableFailureWithoutPreviousDeletesNewConfig(t *testing.T) {
	t.Parallel()

	deleteCalls := 0
	store := &fakeLifecycleStore{
		resolveFunc: func(_ context.Context, _ string, _ ChannelType) (ChannelConfig, error) {
			return ChannelConfig{}, ErrChannelConfigNotFound
		},
		upsertFunc: func(_ context.Context, botID string, channelType ChannelType, _ UpsertConfigRequest) (ChannelConfig, error) {
			return ChannelConfig{
				ID:          "cfg-new",
				BotID:       botID,
				ChannelType: channelType,
				Credentials: map[string]any{"botToken": "new"},
			}, nil
		},
		deleteFunc: func(_ context.Context, _ string, _ ChannelType) error {
			deleteCalls++
			return nil
		},
	}
	controller := &fakeConnectionController{
		ensureFunc: func(_ context.Context, _ ChannelConfig) error {
			return errors.New("start failed")
		},
	}
	service := NewLifecycle(store, controller)
	enabled := false

	_, err := service.UpsertBotChannelConfig(context.Background(), "bot-1", ChannelType("telegram"), UpsertConfigRequest{
		Credentials: map[string]any{"botToken": "new"},
		Disabled:    &enabled,
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if deleteCalls != 1 {
		t.Fatalf("expected 1 delete call for rollback, got %d", deleteCalls)
	}
}

func TestLifecycleDeleteStopsConnection(t *testing.T) {
	t.Parallel()

	removeCalled := false
	store := &fakeLifecycleStore{
		deleteFunc: func(_ context.Context, _ string, _ ChannelType) error {
			return nil
		},
	}
	controller := &fakeConnectionController{
		removeFunc: func(_ context.Context, _ string, _ ChannelType) {
			removeCalled = true
		},
	}
	service := NewLifecycle(store, controller)

	if err := service.DeleteBotChannelConfig(context.Background(), "bot-1", ChannelType("telegram")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !removeCalled {
		t.Fatalf("expected remove connection to be called")
	}
}

func TestLifecycleSetBotChannelStatusDisable(t *testing.T) {
	t.Parallel()

	removeCalled := false
	store := &fakeLifecycleStore{
		statusFunc: func(_ context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error) {
			if !disabled {
				t.Fatalf("expected disabled=true update")
			}
			return ChannelConfig{ID: "cfg-1", BotID: botID, ChannelType: channelType, Disabled: true}, nil
		},
	}
	controller := &fakeConnectionController{
		removeFunc: func(_ context.Context, _ string, _ ChannelType) {
			removeCalled = true
		},
	}
	service := NewLifecycle(store, controller)

	cfg, err := service.SetBotChannelStatus(context.Background(), "bot-1", ChannelType("telegram"), true)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !cfg.Disabled {
		t.Fatalf("expected disabled config")
	}
	if !removeCalled {
		t.Fatalf("expected remove connection to be called")
	}
}

func TestLifecycleSetBotChannelStatusEnableFailureRollsBack(t *testing.T) {
	t.Parallel()

	statusCalls := 0
	removeCalled := false
	store := &fakeLifecycleStore{
		statusFunc: func(_ context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error) {
			statusCalls++
			if statusCalls == 1 && disabled {
				t.Fatalf("first status update should enable config")
			}
			if statusCalls == 2 && !disabled {
				t.Fatalf("second status update should rollback to disabled=true")
			}
			return ChannelConfig{
				ID:          "cfg-1",
				BotID:       botID,
				ChannelType: channelType,
				Disabled:    disabled,
			}, nil
		},
	}
	controller := &fakeConnectionController{
		ensureFunc: func(_ context.Context, _ ChannelConfig) error {
			return errors.New("start failed")
		},
		removeFunc: func(_ context.Context, _ string, _ ChannelType) {
			removeCalled = true
		},
	}
	service := NewLifecycle(store, controller)

	_, err := service.SetBotChannelStatus(context.Background(), "bot-1", ChannelType("telegram"), false)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if statusCalls != 2 {
		t.Fatalf("expected 2 status updates, got %d", statusCalls)
	}
	if !removeCalled {
		t.Fatalf("expected remove connection to be called on failed enable")
	}
}
