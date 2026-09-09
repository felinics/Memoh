// Package protocol implements the wire contract Memoh speaks with the codex
// app-server: JSON-RPC 2.0 semantics over newline-delimited JSON on stdio,
// with the `jsonrpc` version field omitted, as the app-server does.
//
// The typed protocol surface (*.gen.go) is generated from the vendored JSON
// Schema snapshot by cmd/gen-codex-protocol; this file holds the
// version-independent core: envelopes, request ids, inbound classification,
// and the union runtime helpers the generated code relies on.
//
// Decoding is deliberately tolerant: unknown methods, notification kinds, and
// union variants never fail — they surface with their raw bytes retained — so
// a pinned CLI upgrade can only add information, not break the stream.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

// RequestID mirrors the app-server RequestId, which is a string or a number.
// The original JSON representation is preserved so ids echo back
// byte-for-byte: 42 must not become "42".
type RequestID struct {
	raw json.RawMessage
}

// NewRequestID returns a numeric request id.
func NewRequestID(n uint64) RequestID {
	return RequestID{raw: json.RawMessage(strconv.FormatUint(n, 10))}
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	if len(id.raw) == 0 {
		return nil, errors.New("codex protocol: marshaling zero RequestID")
	}
	return id.raw, nil
}

func (id *RequestID) UnmarshalJSON(data []byte) error {
	// Fresh allocation: value copies of a RequestID must not be rewritten by
	// a later decode into the original variable.
	id.raw = append(json.RawMessage(nil), data...)
	return nil
}

// IsZero reports whether the id is unset.
func (id RequestID) IsZero() bool { return len(id.raw) == 0 }

// Key returns the exact wire representation, usable as a correlation map key.
func (id RequestID) Key() string { return string(id.raw) }

func (id RequestID) String() string { return string(id.raw) }

// Request is an outbound Memoh → app-server request.
type Request struct {
	ID     RequestID `json:"id"`
	Method string    `json:"method"`
	Params any       `json:"params,omitempty"`
}

func (r Request) MarshalJSON() ([]byte, error) {
	type plain Request
	p := plain(r)
	p.Params = normalizeParams(p.Params)
	return json.Marshal(p)
}

// Notification is an outbound Memoh → app-server notification.
type Notification struct {
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

func (n Notification) MarshalJSON() ([]byte, error) {
	type plain Notification
	p := plain(n)
	p.Params = normalizeParams(p.Params)
	return json.Marshal(p)
}

// normalizeParams unwraps a typed nil (a nil pointer/map/slice inside a
// non-nil interface) to a plain nil so `params,omitempty` actually omits it
// instead of sending `"params": null`, which serde rejects for required
// params.
func normalizeParams(params any) any {
	if params == nil {
		return nil
	}
	v := reflect.ValueOf(params)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		if v.IsNil() {
			return nil
		}
	}
	return params
}

// Response answers an app-server → Memoh request.
type Response struct {
	ID     RequestID `json:"id"`
	Result any       `json:"result"`
}

// ErrorResponse rejects an app-server → Memoh request.
type ErrorResponse struct {
	ID    RequestID `json:"id"`
	Error *RPCError `json:"error"`
}

// RPCError is the JSON-RPC error object. Codes are not enumerated by the
// schema; treat them as opaque and rely on CodexErrorInfo where present.
type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

// InboundKind classifies a line received from the app-server.
type InboundKind int

const (
	// InboundResponse answers one of our requests.
	InboundResponse InboundKind = iota + 1
	// InboundRequest is a server → client request that expects a Response.
	InboundRequest
	// InboundNotification is fire-and-forget.
	InboundNotification
)

// Inbound is one decoded line from the app-server stream.
type Inbound struct {
	Kind   InboundKind
	ID     RequestID       // requests and responses
	Method string          // requests and notifications
	Params json.RawMessage // requests and notifications
	Result json.RawMessage // successful responses
	Err    *RPCError       // failed responses
	Raw    json.RawMessage // the full original line
}

// DecodeInbound classifies one NDJSON line from the app-server.
func DecodeInbound(line []byte) (*Inbound, error) {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return nil, fmt.Errorf("codex protocol: decoding inbound line: %w", err)
	}
	in := &Inbound{
		Method: probe.Method,
		Params: probe.Params,
		Result: probe.Result,
		Err:    probe.Error,
		Raw:    append(json.RawMessage(nil), line...),
	}
	// A literal `id: null` (JSON-RPC's marker for "request id unknowable",
	// e.g. a parse-error response) is no id: it can never correlate with a
	// pending request, and answering a null-id request would be wrong.
	if trimmed := bytes.TrimSpace(probe.ID); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		in.ID = RequestID{raw: probe.ID}
	}
	switch {
	case probe.Method != "" && !in.ID.IsZero():
		in.Kind = InboundRequest
	case probe.Method != "":
		in.Kind = InboundNotification
	case !in.ID.IsZero():
		in.Kind = InboundResponse
	case probe.Error != nil || len(probe.Result) > 0:
		// A null-id response (e.g. a parse-error report): a real response that
		// can never correlate. Surface it with a zero ID so the caller can log
		// it as an orphan instead of dropping the error on the floor.
		in.Kind = InboundResponse
	default:
		return nil, errors.New("codex protocol: inbound line is neither request, response, nor notification")
	}
	return in, nil
}

// MethodInitialized is the client notification completing the handshake.
const MethodInitialized = "initialized"

// InitializeResponse is the app-server's answer to `initialize`. It belongs to
// the version-independent bootstrap layer, which is why it is not part of the
// generated v2 surface.
type InitializeResponse struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

// Union runtime helpers referenced by generated code. Generated files use
// these instead of importing packages so they stay import-free.

type rawMessage = json.RawMessage

var (
	jsonUnmarshal = json.Unmarshal
	jsonMarshal   = json.Marshal
)

func isJSONString(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '"'
}

// marshalTagged marshals payload and splices the union tag into the object.
func marshalTagged(tagKey, tagValue string, payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &obj); err != nil {
		return nil, fmt.Errorf("codex protocol: tagged union payload is not an object: %w", err)
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	tag, err := json.Marshal(tagValue)
	if err != nil {
		return nil, err
	}
	obj[tagKey] = tag
	return json.Marshal(obj)
}

// marshalKeyed wraps payload as the single-key object form of a mixed union.
func marshalKeyed(key string, payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]json.RawMessage{key: encoded})
}

func errNoVariant(name string) error {
	return fmt.Errorf("codex protocol: %s: no variant set", name)
}
