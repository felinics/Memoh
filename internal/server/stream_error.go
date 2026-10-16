package server

import (
	"context"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

// StreamError is the error event of a stream that is already open: an SSE
// error frame or a WebSocket error frame. It carries the public error the
// HTTP error handler would answer with, and never the error's own text.
type StreamError struct {
	Type   string            `json:"type" enums:"error"`
	Code   string            `json:"code"`
	Args   map[string]string `json:"args"`
	Detail string            `json:"detail"`
	// Message repeats Detail. A Web client recognizes an error event by it.
	Message   string         `json:"message"`
	Fault     apperror.Fault `json:"fault"`
	RequestID string         `json:"request_id,omitempty"`
}

// NewStreamError is the error event for err, chosen by errs.Answer, and the
// error the event was rendered from. A handler that sends the event returns
// that error, so that the request's result record attributes it; it does not
// log it itself.
func NewStreamError(ctx context.Context, err error, requestID string) (StreamError, error) {
	err = transportError(err)
	public, fault := errs.Answer(ctx, err)
	code := apperror.CodeOf(public)
	definition, _ := apperror.Lookup(code)
	return StreamError{
		Type:      "error",
		Code:      string(code),
		Args:      apperror.ArgsOf(public),
		Detail:    definition.Detail,
		Message:   definition.Detail,
		Fault:     fault,
		RequestID: requestID,
	}, err
}
