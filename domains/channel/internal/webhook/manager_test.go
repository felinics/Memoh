package webhook

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/memohai/memoh/internal/config"
)

const testDefaultListenAddr = "127.0.0.1:18734"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type processKillerFunc func() error

func (f processKillerFunc) Kill() error {
	return f()
}

func TestNormalizePublicBase(t *testing.T) {
	t.Parallel()

	got, err := normalizePublicBase("abc.trycloudflare.com")
	if err != nil {
		t.Fatalf("normalizePublicBase returned error: %v", err)
	}
	if got != "https://abc.trycloudflare.com" {
		t.Fatalf("normalizePublicBase = %q", got)
	}

	got, err = normalizePublicBase("https://abc.trycloudflare.com/")
	if err != nil {
		t.Fatalf("normalizePublicBase returned error: %v", err)
	}
	if got != "https://abc.trycloudflare.com" {
		t.Fatalf("normalizePublicBase trims slash = %q", got)
	}
}

func TestNormalizePublicBaseRejectsNonHTTPS(t *testing.T) {
	t.Parallel()

	if _, err := normalizePublicBase("http://abc.trycloudflare.com"); err == nil {
		t.Fatal("expected non-HTTPS URL to fail")
	}
}

func TestNormalizePublicBaseRejectsUnsafeHosts(t *testing.T) {
	t.Parallel()

	tests := []string{
		"https://127.0.0.1",
		"https://[::1]",
		"https://user:pass@abc.trycloudflare.com",
		"https://abc.trycloudflare.com/path",
		"https://abc.trycloudflare.com?x=1",
		"https://abc.trycloudflare.com#frag",
		"https://abc.trycloudflare.com:8443",
		"https://trycloudflare.com",
		"https://abc.example.com",
	}
	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizePublicBase(raw); err == nil {
				t.Fatalf("expected %q to fail", raw)
			}
		})
	}
}

func TestNormalizeConfiguredPublicBase(t *testing.T) {
	t.Parallel()

	got, err := normalizeConfiguredPublicBase("https://Memoh.EXAMPLE.org/")
	if err != nil {
		t.Fatalf("normalizeConfiguredPublicBase returned error: %v", err)
	}
	if got != "https://memoh.example.org" {
		t.Fatalf("normalizeConfiguredPublicBase = %q", got)
	}
}

func TestNormalizeConfiguredPublicBaseRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"http://memoh.example.org",
		"https://localhost",
		"https://127.0.0.1",
		"https://[2001:4860:4860::8888]",
		"https://192.168.1.10",
		"https://100.64.0.1",
		"https://192.0.2.1",
		"https://198.51.100.1",
		"https://203.0.113.1",
		"https://memoh.local",
		"https://memoh.internal",
		"https://user:pass@memoh.example.org",
		"https://memoh.example.org/app",
		"https://memoh.example.org:443",
		"https://memoh.example.org:8443",
		"https://memoh.example.org?x=1",
		"https://memoh.example.org#frag",
	}
	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizeConfiguredPublicBase(raw); err == nil {
				t.Fatalf("expected %q to fail", raw)
			}
		})
	}
}

func TestErrorAndStoppedStatusClearPublicBaseURL(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeExternal},
	}, testDefaultListenAddr)
	m.setReady("https://abc.trycloudflare.com")
	m.setError(assertErr("boom"))
	if got := m.Snapshot(); got.PublicBaseURL != "" || got.Status != statusError {
		t.Fatalf("error status = %+v, want no public base", got)
	}
	m.setReady("https://abc.trycloudflare.com")
	m.markStopped()
	if got := m.Snapshot(); got.PublicBaseURL != "" || got.Status != statusStopped {
		t.Fatalf("stopped status = %+v, want no public base", got)
	}
}

func TestErrorStatusDoesNotExposeInternalError(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeExternal},
	}, testDefaultListenAddr)
	m.setError(assertErr("dial tcp 10.0.0.5:18735: connect: connection refused"))
	got := m.Snapshot()
	if got.Error != "webhook tunnel unavailable" {
		t.Fatalf("error = %q, want sanitized message", got.Error)
	}
	if strings.Contains(got.Error, "10.0.0.5") || strings.Contains(got.Error, "18735") {
		t.Fatalf("error leaked internal detail: %q", got.Error)
	}
}

func TestPollErrorPreservesReadyPublicBaseURL(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeExternal},
	}, testDefaultListenAddr)
	m.setReady("https://abc.trycloudflare.com")
	m.setPollError(assertErr("temporary metrics failure"))
	got := m.Snapshot()
	if got.Status != statusReady || got.PublicBaseURL != "https://abc.trycloudflare.com" {
		t.Fatalf("status after poll error = %+v, want ready with existing public base", got)
	}
}

func TestConfiguredPublicBaseURLTakesPrecedence(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:          config.WebhookTunnelModeDisabled,
			PublicBaseURL: "https://memoh.example.org/",
		},
	}, testDefaultListenAddr)
	if got := m.PublicBaseURL(); got != "https://memoh.example.org" {
		t.Fatalf("PublicBaseURL = %q", got)
	}
	status := m.Snapshot()
	if !status.Enabled || status.Status != statusReady || status.PublicBaseURL != "https://memoh.example.org" {
		t.Fatalf("Status = %+v, want configured public base ready", status)
	}

	m.setReady("https://abc.trycloudflare.com")
	if got := m.PublicBaseURL(); got != "https://memoh.example.org" {
		t.Fatalf("PublicBaseURL with tunnel ready = %q, want configured base", got)
	}
}

func TestConfiguredPublicBaseURLStatusOverridesTunnelError(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:          config.WebhookTunnelModeExternal,
			PublicBaseURL: "https://memoh.example.org",
		},
	}, testDefaultListenAddr)
	m.setError(assertErr("metrics unavailable"))
	status := m.Snapshot()
	if status.Status != statusReady || status.Error != "" || status.PublicBaseURL != "https://memoh.example.org" {
		t.Fatalf("Status = %+v, want configured public base ready", status)
	}
}

func TestTargetURLDefaultsToWebhookTunnelListenAddr(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:       config.WebhookTunnelModeManaged,
			ListenAddr: ":18734",
		},
	}, testDefaultListenAddr)
	got, err := m.targetURL()
	if err != nil {
		t.Fatalf("targetURL returned error: %v", err)
	}
	if got != "http://127.0.0.1:18734" {
		t.Fatalf("targetURL = %q", got)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestTargetURLHonorsExplicitTarget(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:      config.WebhookTunnelModeManaged,
			TargetURL: "http://127.0.0.1:9999",
		},
	}, testDefaultListenAddr)
	got, err := m.targetURL()
	if err != nil {
		t.Fatalf("targetURL returned error: %v", err)
	}
	if got != "http://127.0.0.1:9999" {
		t.Fatalf("targetURL = %q", got)
	}
}

func TestStopWaitsForExternalPollLoop(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequest) }) }
	t.Cleanup(release)
	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeExternal},
	}, testDefaultListenAddr)
	m.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		close(requestCanceled)
		<-releaseRequest
		return nil, req.Context().Err()
	})}

	startupCtx, cancelStartup := context.WithCancel(t.Context())
	if err := m.Start(startupCtx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := m.Start(startupCtx); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	select {
	case <-requestStarted:
	case <-t.Context().Done():
		t.Fatal("poll request did not start")
	}
	cancelStartup()
	select {
	case <-requestCanceled:
		t.Fatal("startup context cancellation stopped the application-owned poll loop")
	default:
	}

	stopCtx, cancelStop := context.WithCancel(t.Context())
	stopResult := make(chan error, 1)
	go func() {
		stopResult <- m.Stop(stopCtx)
	}()
	select {
	case <-requestCanceled:
	case <-t.Context().Done():
		t.Fatal("Stop() did not cancel the poll request")
	}
	cancelStop()
	if err := <-stopResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context.Canceled while poll is still running", err)
	}
	if got := m.Snapshot().Status; got == statusStopped {
		t.Fatalf("status = %q before poll loop exits, want non-stopped", got)
	}

	release()
	if err := m.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() after poll exit error = %v", err)
	}
	if got := m.Snapshot().Status; got != statusStopped {
		t.Fatalf("status = %q after poll loop exits, want %q", got, statusStopped)
	}
	if err := m.Start(t.Context()); err == nil {
		t.Fatal("Start() after Stop() succeeded")
	}
}

func TestStopAggregatesManagedProcessAndTaskErrors(t *testing.T) {
	t.Parallel()

	killErr := assertErr("kill failed")
	killCalled := make(chan struct{})
	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeManaged},
	}, testDefaultListenAddr)
	m.managedProcess = processKillerFunc(func() error {
		close(killCalled)
		return killErr
	})
	m.cmdDone = make(chan struct{})
	m.pollDone = make(chan struct{})
	stopCtx, cancelStop := context.WithCancel(t.Context())
	cancelStop()

	err := m.Stop(stopCtx)
	if !errors.Is(err, killErr) {
		t.Fatalf("Stop() error = %v, want kill error", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context cancellation", err)
	}
	select {
	case <-killCalled:
	default:
		t.Fatal("Stop() did not attempt to stop the managed process")
	}
	for _, message := range []string{
		"stop managed webhook tunnel",
		"wait for managed webhook tunnel",
		"wait for webhook tunnel polling",
	} {
		if !strings.Contains(err.Error(), message) {
			t.Fatalf("Stop() error = %q, want %q", err, message)
		}
	}
}

func TestStopIgnoresAlreadyFinishedManagedProcess(t *testing.T) {
	t.Parallel()

	cmdDone := make(chan struct{})
	pollDone := make(chan struct{})
	close(cmdDone)
	close(pollDone)
	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeManaged},
	}, testDefaultListenAddr)
	m.managedProcess = processKillerFunc(func() error { return os.ErrProcessDone })
	m.cmdDone = cmdDone
	m.pollDone = pollDone

	if err := m.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v, want nil", err)
	}
	if got := m.Snapshot().Status; got != statusStopped {
		t.Fatalf("status = %q, want %q", got, statusStopped)
	}
}
