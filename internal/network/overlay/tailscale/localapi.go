package tailscale

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
)

// localAPIHost is the Host header tailscaled requires on LocalAPI requests.
const localAPIHost = "local-tailscaled.sock"

// IPN bus watch options, from tailscale.com/ipn NotifyWatchOpt.
const (
	notifyInitialState       = 1 << 1
	notifyInitialHealthState = 1 << 7
)

type localStatus struct {
	BackendState string   `json:"BackendState"`
	AuthURL      string   `json:"AuthURL"`
	Health       []string `json:"Health"`
	Self         *struct {
		ID           string   `json:"ID"`
		HostName     string   `json:"HostName"`
		DNSName      string   `json:"DNSName"`
		Online       bool     `json:"Online"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
	Peer map[string]*peerStatus `json:"Peer"`
}

type peerStatus struct {
	ID             string   `json:"ID"`
	HostName       string   `json:"HostName"`
	DNSName        string   `json:"DNSName"`
	Online         bool     `json:"Online"`
	TailscaleIPs   []string `json:"TailscaleIPs"`
	OS             string   `json:"OS"`
	ExitNodeOption bool     `json:"ExitNodeOption"`
	Active         bool     `json:"Active"`
}

// healthWarning is the subset of tailscale.com/health.UnhealthyState that
// Memoh reads.
type healthWarning struct {
	Severity            string `json:"Severity"`
	Title               string `json:"Title"`
	Text                string `json:"Text"`
	ImpactsConnectivity bool   `json:"ImpactsConnectivity"`
}

type ipnNotify struct {
	Health *struct {
		Warnings map[string]healthWarning `json:"Warnings"`
	} `json:"Health"`
}

// localClient talks to tailscaled's LocalAPI over its unix socket.
type localClient struct {
	socketPath string
}

func (c localClient) httpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return sidecar.DialUnix(ctx, c.socketPath)
			},
		},
	}
}

func (c localClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+localAPIHost+"/localapi/v0/"+path, reader)
	if err != nil {
		return err
	}
	client := c.httpClient(10 * time.Second)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req) //nolint:gosec // Requests go to tailscaled's LocalAPI over a unix socket.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("tailscale localapi %s %s: status %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c localClient) status(ctx context.Context, includePeers bool) (localStatus, error) {
	var status localStatus
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("status?peers=%t", includePeers), nil, &status)
	return status, err
}

// prefs returns the full preferences as a JSON object so that fields Memoh
// does not manage survive a round trip through start.
func (c localClient) prefs(ctx context.Context) (map[string]any, error) {
	var prefs map[string]any
	err := c.do(ctx, http.MethodGet, "prefs", nil, &prefs)
	return prefs, err
}

// editPrefs applies a tailscale.com/ipn.MaskedPrefs: the preference fields
// plus a "<Field>Set": true marker for each field to change.
func (c localClient) editPrefs(ctx context.Context, masked map[string]any) error {
	return c.do(ctx, http.MethodPatch, "prefs", masked, nil)
}

// start replaces the preferences and, with an auth key, logs in
// non-interactively once login is requested.
func (c localClient) start(ctx context.Context, authKey string, prefs map[string]any) error {
	opts := map[string]any{"UpdatePrefs": prefs}
	if authKey != "" {
		opts["AuthKey"] = authKey
	}
	return c.do(ctx, http.MethodPost, "start", opts, nil)
}

func (c localClient) loginInteractive(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "login-interactive", nil, nil)
}

func (c localClient) logout(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "logout", nil, nil)
}

// healthWarnings reads the current health warnings from the first message of
// an IPN bus subscription.
func (c localClient) healthWarnings(ctx context.Context) (map[string]healthWarning, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	q := url.Values{"mask": {strconv.Itoa(notifyInitialState | notifyInitialHealthState)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+localAPIHost+"/localapi/v0/watch-ipn-bus?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	client := c.httpClient(0)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req) //nolint:gosec // Requests go to tailscaled's LocalAPI over a unix socket.
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscale localapi watch-ipn-bus: status %d", resp.StatusCode)
	}
	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var n ipnNotify
		if err := dec.Decode(&n); err != nil {
			return nil, err
		}
		if n.Health != nil {
			return n.Health.Warnings, nil
		}
	}
}

// waitReady polls until tailscaled answers on its socket.
func (c localClient) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := c.status(ctx, false)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tailscaled did not answer within %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
