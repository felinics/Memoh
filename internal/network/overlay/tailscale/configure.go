package tailscale

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/configutil"
)

// defaultControlURL is what tailscaled uses when ControlURL is empty.
const defaultControlURL = "https://controlplane.tailscale.com"

// desiredPrefs is the part of tailscaled's preferences that the bot network
// config determines. Memoh owns these fields and resets drift on every
// attach; every other preference keeps the value tailscaled has.
type desiredPrefs struct {
	ControlURL      string
	Hostname        string
	RouteAll        bool
	AdvertiseRoutes []string
	ExitNode        string
}

func (d *nativeDriver) desired(req netctl.AttachmentRequest) (desiredPrefs, error) {
	cfg := d.config.Config
	routes, err := parseRoutes(configutil.String(cfg, "advertise_routes"))
	if err != nil {
		return desiredPrefs{}, err
	}
	return desiredPrefs{
		ControlURL:      normalizeControlURL(configutil.String(cfg, "control_url")),
		Hostname:        configutil.FirstNonEmpty(configutil.String(cfg, "hostname"), req.BotID),
		RouteAll:        configutil.Bool(cfg, "accept_routes"),
		AdvertiseRoutes: routes,
		ExitNode:        configutil.String(cfg, "exit_node"),
	}, nil
}

// configure brings tailscaled in line with the bot network config: it logs
// in when the node has no identity and otherwise patches only the
// preferences that differ.
func (d *nativeDriver) configure(ctx context.Context, req netctl.AttachmentRequest) error {
	want, err := d.desired(req)
	if err != nil {
		return err
	}
	c := d.client(req)
	if err := c.waitReady(ctx, localAPIWait); err != nil {
		return err
	}
	st, err := c.status(ctx, true)
	if err != nil {
		return err
	}
	prefs, err := c.prefs(ctx)
	if err != nil {
		return err
	}
	identity := hasIdentity(prefs)
	if identity && normalizeControlURL(stringField(prefs, "ControlURL")) != want.ControlURL {
		if err := c.logout(ctx); err != nil {
			return err
		}
		identity = false
		if prefs, err = c.prefs(ctx); err != nil {
			return err
		}
	}
	// A node with an identity keeps it while the control server is
	// unreachable, so only a missing identity or an expired key needs a login.
	if !identity || st.BackendState == "NeedsLogin" {
		if st.AuthURL != "" {
			// An interactive login is already waiting for the user; starting
			// another one would invalidate the URL they may have open.
			return nil
		}
		applyLoginPrefs(prefs, want, st)
		if err := c.start(ctx, configutil.String(d.config.Config, "auth_key"), prefs); err != nil {
			return err
		}
		return c.loginInteractive(ctx)
	}
	if patch := prefsPatch(prefs, want, st); len(patch) > 0 {
		return c.editPrefs(ctx, patch)
	}
	return nil
}

// hasIdentity reports whether tailscaled holds a node identity, read from
// the persisted login ("Config" in the prefs JSON). BackendState cannot tell:
// a logged-in node reports NoState until the control server answers.
func hasIdentity(prefs map[string]any) bool {
	if boolField(prefs, "LoggedOut") {
		return false
	}
	persist, _ := prefs["Config"].(map[string]any)
	return stringField(persist, "NodeID") != ""
}

// fixedPrefs are set for every bot regardless of its config. CorpDNS lets
// 100.100.100.100 follow the tailnet's DNS settings, including split DNS.
// ExitNodeAllowLANAccess keeps the workspace's own subnet out of the exit
// node tunnel, which would otherwise cut the server off from the workspace.
var fixedPrefs = map[string]bool{
	"CorpDNS":                true,
	"ExitNodeAllowLANAccess": true,
	"WantRunning":            true,
}

func applyLoginPrefs(prefs map[string]any, want desiredPrefs, st localStatus) {
	// The LocalAPI returns the persisted login with its keys zeroed; sending
	// it back would overwrite the real keys.
	delete(prefs, "Config")
	for key, value := range fixedPrefs {
		prefs[key] = value
	}
	prefs["ControlURL"] = want.ControlURL
	prefs["Hostname"] = want.Hostname
	prefs["RouteAll"] = want.RouteAll
	prefs["AdvertiseRoutes"] = want.AdvertiseRoutes
	id, ip := resolveExitNode(want.ExitNode, st)
	prefs["ExitNodeID"] = id
	prefs["ExitNodeIP"] = ip
}

// prefsPatch returns a MaskedPrefs body with the fields that differ from
// the desired values, or nil when nothing needs to change.
func prefsPatch(prefs map[string]any, want desiredPrefs, st localStatus) map[string]any {
	patch := map[string]any{}
	set := func(key string, value any) {
		patch[key] = value
		patch[key+"Set"] = true
	}
	for key, value := range fixedPrefs {
		if boolField(prefs, key) != value {
			set(key, value)
		}
	}
	if stringField(prefs, "Hostname") != want.Hostname {
		set("Hostname", want.Hostname)
	}
	if boolField(prefs, "RouteAll") != want.RouteAll {
		set("RouteAll", want.RouteAll)
	}
	if !slices.Equal(stringsField(prefs, "AdvertiseRoutes"), want.AdvertiseRoutes) {
		set("AdvertiseRoutes", want.AdvertiseRoutes)
	}
	id, ip := resolveExitNode(want.ExitNode, st)
	currentID, currentIP := stringField(prefs, "ExitNodeID"), stringField(prefs, "ExitNodeIP")
	switch {
	case want.ExitNode == "":
		if currentID != "" || currentIP != "" {
			set("ExitNodeID", "")
			set("ExitNodeIP", "")
		}
	case id != "":
		if currentID != id {
			set("ExitNodeID", id)
		}
	case currentIP != ip:
		set("ExitNodeIP", ip)
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}

// resolveExitNode maps the configured exit node (an address, MagicDNS name
// or hostname) to a peer's stable ID. When no peer matches, an address is
// returned for tailscaled to resolve once the peer appears.
func resolveExitNode(value string, st localStatus) (id, ip string) {
	if value == "" {
		return "", ""
	}
	for _, peer := range st.Peer {
		if peer == nil {
			continue
		}
		if slices.Contains(peer.TailscaleIPs, value) ||
			strings.TrimSuffix(peer.DNSName, ".") == strings.TrimSuffix(value, ".") ||
			strings.EqualFold(peer.HostName, value) {
			return peer.ID, ""
		}
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		return "", addr.String()
	}
	return "", ""
}

// parseRoutes turns a comma-separated list of CIDRs into the canonical,
// sorted form tailscaled reports.
func parseRoutes(raw string) ([]string, error) {
	var routes []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid advertise_routes entry %q: %w", part, err)
		}
		routes = append(routes, prefix.Masked().String())
	}
	slices.Sort(routes)
	return slices.Compact(routes), nil
}

func normalizeControlURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return defaultControlURL
	}
	return raw
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func boolField(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

func stringsField(m map[string]any, key string) []string {
	items, _ := m[key].([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}
