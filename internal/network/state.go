package network

// OverlayState is the state of a bot's network overlay as reported by the
// status API. The web UI keys its labels on these values.
type OverlayState string

const (
	// OverlayStateReady means the client is logged in, has an address and
	// reports no connectivity-impacting health warnings.
	OverlayStateReady OverlayState = "ready"
	// OverlayStateStarting means the client is up but has not reached a
	// steady state yet.
	OverlayStateStarting OverlayState = "starting"
	// OverlayStateNeedsLogin means the client waits for an interactive login
	// or for an administrator to approve the node.
	OverlayStateNeedsLogin OverlayState = "needs_login"
	// OverlayStateDegraded means the client is connected but reports a
	// health problem that affects connectivity.
	OverlayStateDegraded OverlayState = "degraded"
	// OverlayStateStopped means the sidecar exists but is not running.
	OverlayStateStopped OverlayState = "stopped"
	// OverlayStateMissing means no sidecar has been created yet.
	OverlayStateMissing OverlayState = "missing"
	// OverlayStateUnsupported means the workspace runtime cannot host the
	// provider's sidecar.
	OverlayStateUnsupported OverlayState = "unsupported"
	// OverlayStateDisabled means bot networking is turned off.
	OverlayStateDisabled OverlayState = "disabled"
	// OverlayStateWorkspaceMissing means the bot workspace has not been
	// created yet.
	OverlayStateWorkspaceMissing OverlayState = "workspace_missing"
	// OverlayStateNeedsConfig means the provider configuration is incomplete.
	OverlayStateNeedsConfig OverlayState = "needs_config"
	// OverlayStateUnauthorized means the provider rejected the credentials.
	OverlayStateUnauthorized OverlayState = "unauthorized"
	// OverlayStateUnknown means the state could not be determined.
	OverlayStateUnknown OverlayState = "unknown"
)
