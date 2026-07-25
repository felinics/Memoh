// Package assembly owns Server-side construction of public Agent services.
//
// cmd/** and HTTP handlers must import public domains/agent packages (or this
// assembly package) and must not reach into domains/agent/internal. Channel
// split composition must likewise avoid Agent private packages; Agent turn
// execution crosses the process boundary only through internal/rpc/channel/turn.
package assembly
