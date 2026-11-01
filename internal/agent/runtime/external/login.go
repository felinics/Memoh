package external

import (
	"context"
)

// DeviceLoginStart is a device-code sign-in the runtime has begun: the user
// opens the URL and confirms the code there.
type DeviceLoginStart struct {
	LoginID         string
	UserCode        string
	VerificationURL string
}

// Device-code sign-in states. Unknown: the login is not tracked, e.g. the
// agent process restarted.
const (
	DeviceLoginPending = "pending"
	DeviceLoginSuccess = "success"
	DeviceLoginError   = "error"
	DeviceLoginUnknown = "unknown"
)

// DeviceLoginStatus is the poll answer for a pending device-code sign-in.
type DeviceLoginStatus struct {
	// Status is one of the DeviceLogin states.
	Status string
	Error  string
}

// DeviceLoginProvider is implemented by direct runtimes whose account sign-in
// runs inside the Agent's own process: the agent obtains the device code,
// completes the sign-in, and writes its own login file.
type DeviceLoginProvider interface {
	StartDeviceLogin(ctx context.Context, botID, botAgentID string) (DeviceLoginStart, error)
	// PollDeviceLogin reports the sign-in's state. On success the login is in
	// place for the Agent; ownerUserID owns it where the runtime stores one.
	PollDeviceLogin(ctx context.Context, ownerUserID, botID, botAgentID, loginID string) (DeviceLoginStatus, error)
	CancelDeviceLogin(ctx context.Context, botID, botAgentID, loginID string) error
}

// Account is the account an Agent's runtime is signed in to.
type Account struct {
	SignedIn bool
	// Email is empty when the runtime does not report one.
	Email string
}

// AccountReader is implemented by runtimes that keep their own sign-in in the
// Agent's workspace, so only the runtime can say whether it is signed in.
type AccountReader interface {
	Account(ctx context.Context, botID, botAgentID string) (Account, error)
}
