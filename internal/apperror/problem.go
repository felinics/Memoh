package apperror

// Public is the transport-neutral, user-safe projection of an error. HTTP and
// streaming transports wrap it in different envelopes but must not derive
// their own code, detail or metadata.
type Public struct {
	Kind      string            `json:"kind"`
	Code      Code              `json:"code,omitempty"`
	Args      map[string]string `json:"args,omitempty"`
	Detail    string            `json:"detail"`
	Upstream  *Upstream         `json:"upstream,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// Upstream quotes a third party's error verbatim.
//
// It exists because for provider calls this service is a proxy, and the only
// party who can act on "you exceeded your current quota" is the user who owns
// that provider account. Replacing that sentence with our own generic text
// would be the contract destroying the one useful thing in the response.
//
// It is a separate member rather than part of Detail because it is categorically
// different: Detail is ours, closed, and localized into the user's language,
// while Message is foreign, open-ended and arrives in whatever language and
// wording the provider chose. Merging them would mean a client could no longer
// tell which half it is allowed to translate. Clients should present Message as
// a quotation, attributed to Provider.
//
// Message must already have had credentials scrubbed from it by the layer that
// owns them; apperror cannot know what is secret.
type Upstream struct {
	Provider string `json:"provider,omitempty"`
	Message  string `json:"message"`
}

// Problem is the RFC 9457 body served with application/problem+json. Kind,
// Code, Args and Errors are extension members; Type is about:blank whenever the
// error carries no catalog code, which RFC 9457 explicitly allows.
//
// Kind is present even though Type and Status already imply it. Most errors in
// this project carry no catalog code, so Kind is the only stable identity a
// client can localize them by, and reading one field beats parsing a URI or
// maintaining a status-to-message table on every client.
type Problem struct {
	Type      string            `json:"type" validate:"required"`
	Title     string            `json:"title" validate:"required"`
	Status    int               `json:"status" validate:"required"`
	Detail    string            `json:"detail" validate:"required"`
	Kind      string            `json:"kind" validate:"required"`
	Code      string            `json:"code,omitempty"`
	Args      map[string]string `json:"args,omitempty"`
	Errors    []FieldError      `json:"errors,omitempty"`
	Upstream  *Upstream         `json:"upstream,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// PublicOf projects any error, including one produced outside the contract.
// It never fails: an unrecognized error renders as its Kind.
func PublicOf(err error, requestID string) Public {
	kind := KindOf(err)
	code := CodeOf(err)

	detail := kind.Detail()
	if definition, ok := catalog[code]; ok {
		detail = definition.Detail
	} else {
		code = ""
	}

	return Public{
		Kind:      kind.String(),
		Code:      code,
		Args:      sanitizeArgs(code, ArgsOf(err)),
		Detail:    detail,
		Upstream:  UpstreamOf(err),
		RequestID: requestID,
	}
}

// ProblemOf renders any error as an RFC 9457 problem. This is what the global
// HTTP error handler uses, so that an endpoint cannot fall through to an
// unstructured body by forgetting to register anything.
func ProblemOf(err error, requestID string) Problem {
	kind := KindOf(err)
	public := PublicOf(err, requestID)

	status := kind.HTTPStatus()
	title := kind.Title()
	// A catalog code overrides the HTTP status because some codes deliberately
	// answer a narrower status than their Kind implies. Kind still drives the
	// gRPC and JSON-RPC renderings.
	if definition, ok := catalog[public.Code]; ok {
		status = definition.HTTPStatus
		title = KindFromHTTPStatus(status).Title()
	}

	return Problem{
		Type:      TypeURI(public.Code),
		Title:     title,
		Status:    status,
		Detail:    public.Detail,
		Kind:      public.Kind,
		Code:      string(public.Code),
		Args:      public.Args,
		Errors:    FieldsOf(err),
		Upstream:  public.Upstream,
		RequestID: public.RequestID,
	}
}

// PublicFrom reports whether err belongs to the contract and projects it.
// Streaming adapters that still keep a legacy branch use the boolean; new code
// should call PublicOf.
func PublicFrom(err error, requestID string) (Public, bool) {
	if _, ok := As(err); !ok {
		return Public{}, false
	}
	return PublicOf(err, requestID), true
}

// ProblemFrom is the boolean-returning counterpart of ProblemOf, kept for
// call sites that still distinguish contract errors from legacy ones.
func ProblemFrom(err error, requestID string) (Problem, bool) {
	if _, ok := As(err); !ok {
		return Problem{}, false
	}
	return ProblemOf(err, requestID), true
}
