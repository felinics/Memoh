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
  published with and only grows. It is sorted by code, and so are the keys
  under `errors` in the locale files; the guard test checks both, so codes
  added on parallel branches do not touch the same lines. A published status is changed only when no
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

Every entry point answers with the public error `errs.Answer(ctx, err)`
chooses, where `ctx` is the unit's own context. It applies these rules in
order:

1. The caller has ended and the chain holds a cancellation: `canceled`, fault
   `canceled`.
2. The outermost apperror on the chain whose code is in the catalog, with the
   fault its attribution gives. It is skipped when it came from a remote that
   refused a request this process sent and was not marked `errs.Forwarded`:
   that refusal says nothing about this process's caller.
3. The generic code for the fault: `http.bad_request` for a `client` fault,
   `internal` for a `server` or `dependency` fault.

A native gRPC status takes part in the attribution but is never the answer.
The transport renders the answer in its own envelope:

| Entry point | Rendered by |
| --- | --- |
| HTTP request | `internal/server/error_handler.go`, as described below |
| SSE or WebSocket event after the stream is open | The handler that sends the event, from `server.NewStreamError`: code, args, catalog detail and fault, no error text. It returns the error it rendered, which the request's result record attributes. |
| IM channel reply | `channel.ErrorEvent` or `channel.ReplyText`, which look up the code's copy. A flow's own failure copy replaces the generic codes, and a canceled caller gets no reply. |
| Cross-process RPC | `rpc.AnswerStatus`, a gRPC status whose `google.rpc.ErrorInfo` reason is the code and whose metadata holds the args and the server's `fault`; a canceled caller gets `Canceled` |

Errors produced by the transport itself (an unknown route, a method that is
not allowed, a body over the limit, an unparsable WebSocket frame) are
translated by that transport.

The text of an internal error is never sent to a client, in a response body,
an event, a frame or a reply. It goes to the result record. Nor is it stored:
an agent run records its failure in `session_runs.error_code` and in the
`error_code` of history metadata, and the live run view carries the code
alone. An error without a public error is answered with the generic code for
its fault on every entry point. A stream error without a code is
`runtime_run_failed`, the code its run records.

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
  "fault": "dependency",
  "request_id": "3f2b9c1e-…",
  "trace_id": "4bf92f35…"
}
```

`status` and `detail` come from the catalog entry. `trace_id` is present when
the request is traced. A `HEAD` request gets the status and headers without a
body.

The code is the one `errs.Answer` chooses. An `*echo.HTTPError` is the
transport's own error and becomes the `http.*` code for its status, with the
returned error as its cause, before the choice: a client status without its
own code is `http.bad_request` and a server status without one is
`internal`. The rules then give:

| The chain holds | Code |
| --- | --- |
| A cancellation by the caller | `canceled`, 499 |
| An `*echo.HTTPError` | The `http.*` code for its status |
| A public error from a remote that refused this process's request | `internal`, 500 |
| Any other public error | The outermost one |
| No public error, `client` fault | `http.bad_request`, 400 |
| No public error | `internal`, 500 |

The message of an `*echo.HTTPError` is not sent. A handler that has a cause
for one attaches it with `WithInternal(err)`, so the result record carries it.

### Request fields

A request that lacks a field, or holds a value this process cannot accept in
one, is answered with the field it names:

| Code | Built with | Args |
| --- | --- | --- |
| `request.field_required` | `apperror.FieldRequired(field)`, or `httpx.RequiredParam` / `httpx.RequiredQuery`, which read and trim the parameter | `field` |
| `request.field_invalid` | `apperror.FieldInvalid(field, cause)`; the cause stays private | `field` |

`field` is the name the request uses: the JSON key, query parameter or path
parameter as written there, without changing its case, with dots for a nested
key. It is written as a string literal where the field is read; a guard test
in `internal/apperror` fails on any other argument. A response names one
field: a handler returns at the first problem.

Both HTTP servers bind with `httpx.Binder`, so a JSON value of the wrong type
is answered as `request.field_invalid` for its key. Malformed JSON has no
field and stays `http.bad_request`.

A field problem whose fix needs more than the field's name, such as a rule
between two fields or an action the user has to take first, has a code of its
own in the domain that checks it.

An `*echo.HTTPError` with a 400 or 422 status carries no message: nothing a
handler writes there reaches the user. Text kept for the access record goes in
`WithInternal`. A guard test in `internal/apperror` fails on a 400 or 422
`echo.NewHTTPError` with a message argument.

`fault` is who the process attributes the failure to, not something derived
from the status:

| Fault | Meaning |
| --- | --- |
| `client` | The request was refused: invalid, unauthorized, conflicting or unknown. |
| `server` | This process failed. |
| `dependency` | A service this process called failed. |
| `canceled` | The caller canceled the request. |

A client that has no copy for a code chooses by the status: nothing for 499,
a retry prompt for 429 and for a 5xx status, and the copy for the status, or
`http.bad_request`, for any other 4xx status. The error event of an SSE or
WebSocket stream has no status. One whose code the client has no copy for
shows the generic failure copy `errors.internal`, and nothing when its fault
is `canceled`. `fault` is only used for attribution and log levels.

## Attribution of a code

A public error with a catalog code is attributed by its entry. When the entry
declares a `Fault`, that is the fault. Otherwise a 4xx status is a `client`
fault, and a 5xx status is a `server` fault unless its cause is marked with
`errs.WrapDependency` or was received from another service. A received cause
is a `dependency` fault, except when that service reported a `client` fault:
then this process sent a request it refused, and the fault is `server`. An
error with no public error is attributed by its cause the same way. The HTTP Problem, the result record of
every boundary and the RPC envelope all take the fault from this one rule.

A model provider is outside Memoh, whoever holds the credential. Every code
that reports a provider's answer declares `dependency`, including a rejected
key, an exhausted quota and a rate limit (429); a content
moderation refusal would be the one `client` code. The codes an external
agent runtime reports about its own failure declare `dependency` for the same
reason. The ones where nothing failed and the user has to change something
answer 4xx and stay `client`: an account that is not signed in
(`external_runtime.auth_required`), a conversation that no longer fits the
context window (`external_runtime.context_window_exceeded`) and a request the
model service's policy refused (`external_runtime.request_blocked`). A code
that some producers raise for this process's own failures, such as
`external_runtime.unavailable`, declares nothing and is attributed by its
chain. `workspace.unreachable` is one: the workspace bridge client decodes an
unreachable workspace runtime with `rpc.Decode`, so its cause is received and
the fault is `dependency`; a failure to look up the target in this process
stays `server`. A guard test in
`internal/apperror` lists every declaration and fails when a code under
`agent.provider_` or `agent.response_` declares none.

| Code | Fault |
| --- | --- |
| `agent.provider_auth_failed`, `agent.provider_permission_denied`, `agent.provider_quota_exhausted`, `agent.provider_rate_limited`, `agent.provider_overloaded`, `agent.provider_request_rejected`, `agent.provider_unreachable` | `dependency` |
| `agent.response_interrupted`, `agent.response_timeout` | `dependency` |
| `runtime_prompt_failed`, `external_runtime.session_resume_failed`, `external_runtime.usage_limited`, `external_runtime.rate_limited`, `external_runtime.overloaded`, `external_runtime.upstream_unreachable`, `acp.config_update_failed` | `dependency` |
| `connector.oauth_client_not_configured` | `dependency` |

## Attribution across an RPC

An internal RPC server writes the fault it attributes the error to under the
`fault` key of the `ErrorInfo` metadata of every status it returns. A catalog
envelope carries the fault of its error, a reason an RPC package registers
the fault its status code gives, and any other status the fault the server's
result record attributes. The client records the error as `remote` with that
value as `remote_fault`, and attributes it on its side:

| `remote_fault` | Fault in the client |
| --- | --- |
| `client` | `server`: this process sent a request the server refused. A caller that deliberately forwarded end-user input marks the error with `errs.Forwarded`, and the fault stays `client`. |
| `server` or `dependency` | `dependency` |
| Absent, from a server that predates the key | `dependency` |

A client that predates the key ignores it: the key is not a catalog argument
and is not restored as one.

Every internal RPC client decodes what it receives with `rpc.Decode`. The
result is marked remote and keeps the received status on its chain. A status
is restored by its `ErrorInfo` reason (a sentinel the RPC package registers,
or a catalog code) and then by its status code alone; the status message is
never read. `Canceled` and `DeadlineExceeded` are not restored as
`context.Canceled` or `context.DeadlineExceeded`: a server, a closing
connection and the caller all end a call with them, and only the caller's
own context tells them apart. A caller that needs to know whether it ended a
call reads its context. The clients that relay an end user's request, the
turn client and the server runtime client of the channel process, mark a
restored catalog error with `errs.Forwarded`, so the user gets the same
answer in split and all-in-one deployments.

## Result records

A unit of work ends with exactly one result record. For an HTTP request that
is the access record, `msg` = `request`, written by `server.AccessLog` in both
HTTP shells. For a failed request it also carries the error fields of the
error the response was rendered from. A handler that wrote its own response
body, such as an SSE error frame, returns the error the body was rendered
from; the access record attributes it by the same rule without answering it
again. A write that failed because the client went away is not a failure and
is not returned. A panic in a handler is recovered into an error with the
stack of the panic, answered as `internal` and recorded the same way.

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
| A `server` or `dependency` failure marked `errs.Recorded` | `WARN` |
| An event | `WARN` |

A background unit has no caller to blame, so a client fault there counts as
this process's fault. An agent run is a background unit when its mode has no
user in the loop: schedule, discuss and subagent. An IM message is not: it has
a sender, so a client fault stays the sender's.

`errs.Recorded` marks a failure that a nested unit in this process has already
written its own result record for. A run with a durable record writes its
`agent run` record, and the unit that consumed the run (the inbound message,
the RPC, the schedule firing, the discuss turn) gets the failure marked. That
unit still records it, at no more than `WARN`, so one failure has one `ERROR`.

## Background units

`internal/job` is the boundary of a background unit: work that has an object
(a session, a workspace, a schedule firing, a task) and an end, but no caller
waiting for its outcome. `job.Go` runs the unit on a new goroutine and
`job.Run` on the caller's. The unit's context is detached from the
cancellation of whatever started it, and it runs under its own root span
linked to the span that started it. The unit recovers its own panic and writes
exactly one result record, `msg` = `job`, on success as well as on failure,
attributed as a background unit. Work no request started, such as a timer or a
cron firing, sets `OwnRequestID` and gets a new request id and no link.

The work inside the unit returns its failure. `job.WillRetry` marks a failure
the owner retries automatically: the record carries `will_retry` and a
`dependency` failure is `WARN`. Only an attempt inside the retry budget is
marked; the attempt that exhausts it is the outcome. `job.Annotate` adds facts
the unit learns while it runs, such as a skip reason or an exit code, to the
record. Periodic passes and ticks, reconnect loops and single attempts inside
a unit are not units, and their failures are events.

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
