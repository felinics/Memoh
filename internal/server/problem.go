package server

import "github.com/felinics/memoh/internal/apperror"

// Problem is the RFC 9457 body of an HTTP error response. Fault and TraceID
// are filled by the HTTP boundary: fault is its attribution of the error
// (client, server, dependency or canceled), which a client uses to choose
// generic copy for a code it does not know.
type Problem struct {
	Type      string            `json:"type" validate:"required"`
	Status    int               `json:"status" validate:"required"`
	Detail    string            `json:"detail" validate:"required"`
	Code      string            `json:"code" validate:"required"`
	Args      map[string]string `json:"args" validate:"required"`
	Fault     apperror.Fault    `json:"fault" validate:"required"`
	RequestID string            `json:"request_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
}

// ProblemFrom is the Problem for the outermost apperror on err, with the
// status and detail of its catalog entry. It reports false when err holds no
// apperror whose code is in the catalog.
func ProblemFrom(err error, requestID string) (Problem, bool) {
	code := apperror.CodeOf(err)
	definition, ok := apperror.Lookup(code)
	if !ok {
		return Problem{}, false
	}
	return Problem{
		Type:      apperror.TypeURI(code),
		Status:    definition.HTTPStatus,
		Detail:    definition.Detail,
		Code:      string(code),
		Args:      apperror.ArgsOf(err),
		RequestID: requestID,
	}, true
}
