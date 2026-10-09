package mcp

import "errors"

var (
	// ErrNameRequired reports a connection request without a name.
	ErrNameRequired = errors.New("name is required")
	// ErrNameTaken reports that the bot already has a connection with the name.
	ErrNameTaken = errors.New("mcp connection name is already taken")
	// ErrEndpointInvalid reports a connection that gives neither a command nor
	// a URL, or both.
	ErrEndpointInvalid = errors.New("mcp endpoint is invalid")
	// ErrOAuthNotDiscovered reports that OAuth discovery has not been saved for
	// the connection, or saved without an authorization endpoint.
	ErrOAuthNotDiscovered = errors.New("oauth not discovered for this connection")
	// ErrClientIDRequired reports that no client_id is known and the
	// authorization server cannot register one.
	ErrClientIDRequired = errors.New("client_id is required")
	// ErrOAuthStateInvalid reports an authorization callback whose state is
	// unknown, expired or incomplete.
	ErrOAuthStateInvalid = errors.New("oauth state is invalid or expired")
	// ErrTokenExchange reports that the authorization server did not accept the
	// code exchange.
	ErrTokenExchange = errors.New("token exchange failed")
)

// ServerError ties an import failure to the server entry that caused it.
type ServerError struct {
	Name string
	Err  error
}

func (e *ServerError) Error() string { return "server " + e.Name + ": " + e.Err.Error() }

func (e *ServerError) Unwrap() error { return e.Err }
