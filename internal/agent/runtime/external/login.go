package external

import (
	"context"
	"strings"
)

// DeviceLoginStart is a device-code sign-in the runtime has begun: the user
// opens the URL and confirms the code there.
type DeviceLoginStart struct {
	LoginID         string
	UserCode        string
	VerificationURL string
}

// DeviceLoginStatus is the poll answer for a pending device-code sign-in.
type DeviceLoginStatus struct {
	// Status is pending, success, error, or unknown (the login is not
	// tracked, e.g. the agent process restarted).
	Status string
	Error  string
}

// DeviceLoginProvider is implemented by direct runtimes whose account sign-in
// runs inside the Agent's own process: the agent obtains the device code,
// completes the sign-in, and writes its own login file. Memoh only copies the
// result into the Agent credential store, so the file is always in the shape
// the agent itself produces.
type DeviceLoginProvider interface {
	StartDeviceLogin(ctx context.Context, botID, botAgentID string) (DeviceLoginStart, error)
	// PollDeviceLogin reports the sign-in's state. On success it has already
	// stored the credential for the Agent, owned by ownerUserID.
	PollDeviceLogin(ctx context.Context, ownerUserID, botID, botAgentID, loginID string) (DeviceLoginStatus, error)
	CancelDeviceLogin(ctx context.Context, botID, botAgentID, loginID string) error
}

// DeviceLogin returns the runtime's sign-in flow, or false when the runtime
// has none.
func (drivers Drivers) DeviceLogin(runtimeType string) (DeviceLoginProvider, bool) {
	runtimeType = strings.TrimSpace(runtimeType)
	for _, driver := range drivers {
		if driver == nil || driver.RuntimeType() != runtimeType {
			continue
		}
		provider, ok := driver.(DeviceLoginProvider)
		return provider, ok
	}
	return nil, false
}
