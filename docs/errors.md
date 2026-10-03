# Errors

How an error travels from the code that produces it to a client and to the
log, and the rules a change to error handling has to satisfy.
`internal/apperror`, `internal/errs` and `internal/errlog` implement the
mechanism. For the shape of log records in general, see
[logging.md](logging.md).

## Three kinds of error

| Kind | Where it lives | What callers do with it |
| --- | --- | --- |
| Public error | `*apperror.Error`, with a code from the catalog | Clients branch on the code and show its copy. |
| Package error | A sentinel or error type exported by the package that owns the condition | Callers in the process compare it with `errors.Is` or `errors.As`. |
| Diagnostic wrapping | `errs.Wrap`, `errs.New`, `errs.WrapDependency` | Nobody branches on it. It records where the error was produced, who is at fault and structured attributes for the result record. |

A package error is declared with `errors.New` and carries no HTTP status,
copy key or user-facing sentence. Callers never read error text to decide
what to do. When a caller has to retry or classify on something the provider
knows (an upstream status code, a rate limit, an authentication failure), the
providing package exports an error type or a predicate for it.

`errs.Wrap(err, "load bot", slog.String("bot_id", id))` keeps `err` in the
chain, captures the stack at the first wrap and attaches the attributes.
`errs.WrapDependency` marks the failure as a dependency's rather than this
process's. The message is a short constant describing the step, not a
sentence with values in it.

## The catalog

Every public code is registered in `internal/apperror/error.go`. An entry
declares the HTTP status, a fixed English detail and the argument names that
may be sent to a client (`AllowedArgs`). Arguments with any other name are
dropped when the error is constructed. An entry may also declare its `Fault`
when the status would attribute the code wrongly (see
[Attribution of a code](#attribution-of-a-code)).

- A code stands for one cause: two conditions get the same code only if the
  client does the same thing about them. The same cause reached from several
  entry points reuses its code.
- The code is the copy key. The Web app looks up `errors.<code>` in
  `apps/web/src/i18n/locales`, and IM channels look it up in
  `internal/i18n/locales`. Both must have copy in every locale for every code;
  the guard tests fail otherwise.
- A published code is never renamed. A code already written to persisted data
  (`session_runs.error_code`, the `error_code` of history metadata) is
  registered under the value that was written. New codes are lowercase and
  dotted.
- The status tells the client what to do about its own request. 401 is only
  for a missing or invalid Memoh session, because the Web app signs out on a
  401. A code for a dependency's failure answers 5xx, or 429 when the client
  should back off.
- `internal/apperror/testdata/codes.golden` records the status each code was
  published with and only grows. A published status is changed only when no
  client depends on it, and the change is listed in `restatedStatuses` in the
  guard test.
- `internal` (500) is the answer when no public error applies, `canceled`
  (499) when the caller canceled the request, and `http.*` when the transport
  itself refused the request.

`*apperror.Error` does not implement `Unwrap`, so `errors.Is` and `errors.As`
stop at a public error and code above it cannot branch on its cause. It
implements `Cause() error` for diagnostics only: `errs` walks through it to
find the stack, the attribution and the attributes of the cause.

## Translation and rendering

A package error becomes a public error in a translation function written for
one use case and shared by every entry point of that use case. The function
maps its input to a public error, keeps the input as the cause, and does
nothing else: it does not log and does not read the transport. Application
services and domain packages return package errors and do not construct
`apperror` values. The same package error may translate to different codes in
different use cases when the client's action differs.

The packages that return package errors are listed in the `depguard` rule
`package-errors` in `.golangci.yml`, which fails lint when one of them imports
`internal/apperror`. A package is added to the rule when its errors move to
package errors.

The transport renders a public error in its own envelope:

| Entry point | Rendered by |
| --- | --- |
| HTTP request | `internal/server/error_handler.go`, as described below |
| SSE or WebSocket event after the stream is open | The handler that sends the event: code, args and fault, no error text |
| IM channel reply | The renderer in `internal/channel/inbound`, which looks up the code's copy |
| Cross-process RPC | The RPC server packages, as a gRPC status whose `google.rpc.ErrorInfo` reason is the code and whose metadata holds the args and the server's `fault` |

Errors produced by the transport itself (an unknown route, a method that is
not allowed, a body over the limit, an unparsable WebSocket frame) are
translated by that transport.

The text of an internal error is never sent to a client, in a response body,
an event, a frame or a reply. It goes to the result record. Nor is it stored:
an agent run records its failure in `session_runs.error_code` and in the
`error_code` of history metadata, and the live run view carries the code
alone. An error without a public error is answered with the generic code for
its fault: `internal` on the WebSocket and in IM, or `http.bad_request` in IM
for a client fault. A stream error without a code is `runtime_run_failed`,
the code its run records.

A warning attached to a result, such as an item a backup import skipped, says
only what was skipped. Its cause is recorded as an event.

## HTTP responses

Every error answered by an HTTP shell is an RFC 9457 Problem with the content
type `application/problem+json`:

```json
{
  "type": "urn:memoh:error:workspace.unreachable",
  "code": "workspace.unreachable",
  "status": 503,
  "detail": "The workspace is not reachable.",
  "args": {},
  "fault": "server",
  "request_id": "aeOSIBuu…",
  "trace_id": "4bf92f35…"
}
```

`status` and `detail` come from the catalog entry. `trace_id` is present when
the request is traced. A `HEAD` request gets the status and headers without a
body.

The code is chosen from the chain of the returned error:

| The chain holds | Code |
| --- | --- |
| A cancellation by the caller | `canceled`, 499 |
| An `*echo.HTTPError` | The `http.*` code for its status. A client status without one is `http.bad_request`; a server status without one is `internal`. |
| A public error from a remote that refused this process's request | `internal`, 500 |
| Any other public error | The outermost one |
| No public error | `internal`, 500 |

The message of an `*echo.HTTPError` is not sent. A handler that has a cause
for one attaches it with `WithInternal(err)`, so the result record carries it.

`fault` is who the process attributes the failure to, not something derived
from the status:

| Fault | Meaning |
| --- | --- |
| `client` | The request was refused: invalid, unauthorized, conflicting or unknown. |
| `server` | This process failed. |
| `dependency` | A service this process called failed. |
| `canceled` | The caller canceled the request. |

A client that has no copy for a code uses the fault: the copy for the status
for `client`, a retry prompt for `server` and `dependency`, and nothing for
`canceled`.

## Attribution of a code

A public error with a catalog code is attributed by its entry. When the entry
declares a `Fault`, that is the fault. Otherwise a 4xx status is a `client`
fault, and a 5xx status is a `server` fault unless the cause in the chain is
marked with `errs.WrapDependency`. The HTTP Problem, the result record of
every boundary and the RPC envelope all take the fault from this one rule.

A model provider is outside Memoh, whoever holds the credential. Every code
that reports a provider's answer declares `dependency`, including a rejected
key, an exhausted quota and a rate limit (429); a content
moderation refusal would be the one `client` code. The codes an external
agent runtime reports about its own failure declare `dependency` for the same
reason. A code that some producers raise for this process's own failures,
such as `external_runtime.unavailable`, declares nothing and is attributed by
its chain. `workspace.unreachable` is one: the workspace bridge client marks
an unreachable workspace runtime with `errs.WrapDependency`, and a failure to
look up the target in this process stays `server`. A guard test in
`internal/apperror` lists every declaration and fails when a code under
`agent.provider_` or `agent.response_` declares none.

| Code | Fault |
| --- | --- |
| `agent.provider_auth_failed`, `agent.provider_permission_denied`, `agent.provider_quota_exhausted`, `agent.provider_rate_limited`, `agent.provider_overloaded`, `agent.provider_request_rejected`, `agent.provider_unreachable` | `dependency` |
| `agent.response_interrupted`, `agent.response_timeout` | `dependency` |
| `runtime_prompt_failed`, `external_runtime.session_resume_failed`, `external_runtime.usage_limited`, `acp.config_update_failed` | `dependency` |

## Attribution across an RPC

An RPC server writes the fault it attributes the error to under the `fault`
key of the `ErrorInfo` metadata. The client records the error as `remote`
with that value as `remote_fault`, and attributes it on its side:

| `remote_fault` | Fault in the client |
| --- | --- |
| `client` | `server`: this process sent a request the server refused. A caller that deliberately forwarded end-user input marks the error with `errs.Forwarded`, and the fault stays `client`. |
| `server` or `dependency` | `dependency` |
| Absent, from a server that predates the key | `dependency` |

A client that predates the key ignores it: the key is not a catalog argument
and is not restored as one.

## Result records

A unit of work ends with exactly one result record. For an HTTP request that
is the access record, `msg` = `request`, written by `server.AccessLog` in both
HTTP shells. For a failed request it also carries the error fields of the
error the response was rendered from. A panic in a handler is recovered into
an error with the stack of the panic, answered as `internal` and recorded the
same way.

The boundary logs; the code below it returns. A handler or helper that logs
an error and then returns it produces a second record of the same failure and
must not do so. An error that is handled and does not end the unit (a
fallback, a swallowed failure, one attempt of a retry loop) is recorded as an
event with `errlog.Event` and a context, and the unit's result record is
written as usual.

```go
result := errlog.Finish(ctx, operation, err, errlog.Options{})
logger.LogAttrs(ctx, result.Level, "request", attrs...)
```

A WebSocket message that fails before a run takes it over ends with a
`ws request` record, chosen and leveled by the same rule as an HTTP request. A
message that starts a run is recorded by the run's `agent run` record, which
the session runtime's terminal observer writes once per run. It carries the
cause the run's owner held, or the session runtime's own cause when the
runtime ended the run itself, such as an admission it could not complete or a
shutdown.

`errlog.Finish` also sets the span status to error for a `server` or
`dependency` fault and sets `error.type` on the span to the reason. The error
text is not recorded on the span.

## Levels

The level comes from the attribution, not from the status:

| Outcome | Level |
| --- | --- |
| Success | `INFO` |
| `client` or `canceled` fault | `INFO` |
| `server` fault, or a panic | `ERROR` |
| `dependency` fault | `ERROR`, or `WARN` when the remote reported its own failure and logged it there |
| A background unit that will be retried after a dependency failure | `WARN` |
| An event | `WARN` |

A background unit has no caller to blame, so a client fault there counts as
this process's fault.

## Error fields

| Key | Content |
| --- | --- |
| `fault` | The attribution above. |
| `reason` | The code of the public error in the chain, or `internal` or `canceled`. |
| `error` | The redacted text of the whole chain. |
| `error_source` | The frame where the error was produced, when a stack was captured. |
| `error_stack` | The captured stack. |
| `error_attrs` | The attributes attached by `errs.Wrap` and its relatives. |
| `remote`, `remote_fault` | Present when the error came from another service, with that service's own attribution. |
| `panic` | Present when the error is a recovered panic. |
| `will_retry` | Present on the result of a background unit. |

A server or dependency failure without a captured stack is counted in the
`errors.unlocated_total` metric by operation. A rising count means an error
reached a boundary without being wrapped where it was produced.

## Redaction

The `error` field renders the chain with every URL stripped of its userinfo,
query and fragment, since database DSNs and signed URLs carry credentials.
Attributes are logged as given, so an attribute must never hold a secret,
a token, a request body or a model conversation. The list of what must never
be logged is in [logging.md](logging.md#what-must-never-be-logged).
