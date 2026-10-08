package apperror

// Public is the transport-neutral, user-safe representation of an application
// error. HTTP and streaming transports may wrap it in different envelopes, but
// must not derive their own code, detail, or metadata.
type Public struct {
	Code      Code              `json:"code"`
	Args      map[string]string `json:"args"`
	Detail    string            `json:"detail"`
	RequestID string            `json:"request_id,omitempty"`
}

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
	Fault     string            `json:"fault" validate:"required"`
	RequestID string            `json:"request_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
}

func PublicFrom(err error, requestID string) (Public, bool) {
	code := CodeOf(err)
	definition, ok := Lookup(code)
	if !ok {
		return Public{}, false
	}
	return Public{
		Code:      code,
		Args:      sanitizeArgs(code, ArgsOf(err)),
		Detail:    definition.Detail,
		RequestID: requestID,
	}, true
}

func ProblemFrom(err error, requestID string) (Problem, bool) {
	public, ok := PublicFrom(err, requestID)
	if !ok {
		return Problem{}, false
	}
	definition, _ := Lookup(public.Code)
	return Problem{
		Type:      TypeURI(public.Code),
		Status:    definition.HTTPStatus,
		Detail:    public.Detail,
		Code:      string(public.Code),
		Args:      public.Args,
		RequestID: public.RequestID,
	}, true
}
