package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
)

func TestPrepareImageForCreateIfNotPresentSkipsExistingImage(t *testing.T) {
	svc := &legacyRouteTestService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		ImagePullPolicy: config.ImagePullPolicyIfNotPresent,
	})

	result, err := m.PrepareImageForCreate(context.Background(), "debian:bookworm-slim", nil)
	if err != nil {
		t.Fatalf("PrepareImageForCreate returned error: %v", err)
	}
	if result.Mode != ImagePrepareSkipped {
		t.Fatalf("expected skipped, got %s", result.Mode)
	}
	if svc.getImageCalls != 1 || svc.pullCalls != 0 {
		t.Fatalf("unexpected calls: get=%d pull=%d", svc.getImageCalls, svc.pullCalls)
	}
}

func TestPrepareImageForCreateIfNotPresentPullsMissingImage(t *testing.T) {
	svc := &legacyRouteTestService{getImageErr: ctr.ErrNotFound}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		ImagePullPolicy: config.ImagePullPolicyIfNotPresent,
	})

	result, err := m.PrepareImageForCreate(context.Background(), "debian:bookworm-slim", nil)
	if err != nil {
		t.Fatalf("PrepareImageForCreate returned error: %v", err)
	}
	if result.Mode != ImagePreparePulled {
		t.Fatalf("expected pulled, got %s", result.Mode)
	}
	if svc.getImageCalls != 1 || svc.pullCalls != 1 {
		t.Fatalf("unexpected calls: get=%d pull=%d", svc.getImageCalls, svc.pullCalls)
	}
}

func TestPrepareImageForCreateAlwaysPulls(t *testing.T) {
	svc := &legacyRouteTestService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		ImagePullPolicy: config.ImagePullPolicyAlways,
	})

	result, err := m.PrepareImageForCreate(context.Background(), "debian:bookworm-slim", nil)
	if err != nil {
		t.Fatalf("PrepareImageForCreate returned error: %v", err)
	}
	if result.Mode != ImagePreparePulled {
		t.Fatalf("expected pulled, got %s", result.Mode)
	}
	if svc.getImageCalls != 0 || svc.pullCalls != 1 {
		t.Fatalf("unexpected calls: get=%d pull=%d", svc.getImageCalls, svc.pullCalls)
	}
}

func TestPrepareImageForCreateNeverSkips(t *testing.T) {
	svc := &legacyRouteTestService{}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		ImagePullPolicy: config.ImagePullPolicyNever,
	})

	result, err := m.PrepareImageForCreate(context.Background(), "debian:bookworm-slim", nil)
	if err != nil {
		t.Fatalf("PrepareImageForCreate returned error: %v", err)
	}
	if result.Mode != ImagePrepareSkipped {
		t.Fatalf("expected skipped, got %s", result.Mode)
	}
	if svc.getImageCalls != 0 || svc.pullCalls != 0 {
		t.Fatalf("unexpected calls: get=%d pull=%d", svc.getImageCalls, svc.pullCalls)
	}
}

func TestPrepareImageForCreateDelegatesWhenImageServiceUnsupported(t *testing.T) {
	svc := &legacyRouteTestService{getImageErr: ctr.ErrNotSupported}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{})

	result, err := m.PrepareImageForCreate(context.Background(), "debian:bookworm-slim", nil)
	if err != nil {
		t.Fatalf("PrepareImageForCreate returned error: %v", err)
	}
	if result.Mode != ImagePrepareDelegated {
		t.Fatalf("expected delegated, got %s", result.Mode)
	}
}

func TestPrepareImageForCreateFallsBackToWorkspaceMirror(t *testing.T) {
	primary := "docker.io/memohai/workspace:debian"
	fallback := "memoh.cn/memohai/workspace:debian"
	svc := &legacyRouteTestService{
		getImageErr: ctr.ErrNotFound,
		pullErrs: map[string]error{
			primary: ctr.ErrRuntime,
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		ImagePullPolicy: config.ImagePullPolicyIfNotPresent,
	})

	result, err := m.PrepareImageForCreate(context.Background(), "memohai/workspace:debian", nil)
	if err != nil {
		t.Fatalf("PrepareImageForCreate returned error: %v", err)
	}
	if result.Mode != ImagePreparePulled {
		t.Fatalf("expected pulled, got %s", result.Mode)
	}
	if result.ImageRef != fallback {
		t.Fatalf("expected fallback image %q, got %q", fallback, result.ImageRef)
	}
	if got := strings.Join(svc.pullRefs, ","); got != primary+","+fallback {
		t.Fatalf("pull refs = %q, want %q", got, primary+","+fallback)
	}
}

func TestPrepareImageForCreateSkipsExistingWorkspaceMirror(t *testing.T) {
	primary := "docker.io/memohai/workspace:debian"
	fallback := "memoh.cn/memohai/workspace:debian"
	svc := &legacyRouteTestService{
		getImageErrs: map[string]error{
			primary: ctr.ErrNotFound,
		},
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		ImagePullPolicy: config.ImagePullPolicyIfNotPresent,
	})

	result, err := m.PrepareImageForCreate(context.Background(), "memohai/workspace:debian", nil)
	if err != nil {
		t.Fatalf("PrepareImageForCreate returned error: %v", err)
	}
	if result.Mode != ImagePrepareSkipped {
		t.Fatalf("expected skipped, got %s", result.Mode)
	}
	if result.ImageRef != fallback {
		t.Fatalf("expected fallback image %q, got %q", fallback, result.ImageRef)
	}
	if svc.pullCalls != 0 {
		t.Fatalf("expected no pull calls, got %d", svc.pullCalls)
	}
	if got := strings.Join(svc.getImageRefs, ","); got != primary+","+fallback {
		t.Fatalf("get refs = %q, want %q", got, primary+","+fallback)
	}
}

func TestPrepareImageForCreateDoesNotFallbackForCustomImages(t *testing.T) {
	svc := &legacyRouteTestService{
		getImageErr: ctr.ErrNotFound,
		pullErr:     ctr.ErrRuntime,
	}
	m := newLegacyRouteTestManager(t, svc, config.WorkspaceConfig{
		ImagePullPolicy: config.ImagePullPolicyIfNotPresent,
	})

	_, err := m.PrepareImageForCreate(context.Background(), "debian:bookworm-slim", nil)
	if !errors.Is(err, ctr.ErrRuntime) {
		t.Fatalf("expected runtime error, got %v", err)
	}
	if svc.pullCalls != 1 {
		t.Fatalf("expected one pull call, got %d", svc.pullCalls)
	}
}
