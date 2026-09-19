# Logging

How this codebase produces log records, and the rules a change to logging has
to satisfy. `internal/logger` implements the mechanism; this file holds the
decisions a linter cannot make.

## The shape of a record

```json
{"time":"2026-09-18T18:54:53.830Z","level":"INFO","msg":"request","method":"GET","uri":"/api/bots","status":200,"latency":"12ms","request_id":"aeOSIBuu…","trace_id":"4bf92f35…"}
```

`time`, `level`, `msg` and `source` belong to slog and must never be used as
attribute keys. Everything else is an attribute.

`msg` is the event. It is a constant string chosen by the call site, and it is
what a reader groups and filters by, so it must describe what happened rather
than restate a value: `"workspace snapshot failed"`, not `"failed"` and not
`fmt.Sprintf("snapshot %s failed", id)`. Values go in attributes. The linter
enforces the constant part (`sloglint.static-msg`); the rest is judgement.

## Getting a logger

A logger is constructed once and passed to whatever logs, the same as any
other dependency. `logger.New` builds it; components take a `*slog.Logger` as
a constructor argument and usually keep it in a field, often derived with
`With` to attach a component name:

```go
logger: log.With(slog.String("component", "runtime_hub")),
```

There is no package-level logger to reach for, and a context does not carry
one. `slog.SetDefault` is called once during startup so that a dependency
calling `log.Printf` lands in the same stream; that is its only purpose.

`With` is for values fixed when the component is built. Per-request values do
not go there — see below.

## Context variants, and when they are not required

`logger.Info` logs with `context.Background()` internally. The correlation
handler reads the context it is given, so a record logged without one carries
no request or trace identity. On a request path, that is the difference
between an identifier a user quotes finding the work and finding nothing.

**Use the `Context` variants wherever a context is in scope.** In an
`echo.Context` handler that is `c.Request().Context()`.

**The target is not "no plain `Info` calls anywhere."** Some code has no
request to belong to, and threading a context into it to satisfy a rule makes
the code worse and the record no truer:

| Situation | What to do |
| --- | --- |
| A request, job or turn is being served | `InfoContext(ctx, …)`. Thread a context through the callers if one is missing. |
| Process startup, migrations, registry sync | Plain `Info`. There is no request; a record with no correlation is accurate. |
| Pure transforms and formatters | Plain `Info`, or move the log to the caller that has the context. |
| A goroutine outliving the request that started it | `context.WithoutCancel(ctx)`. It keeps the correlation values and drops a cancellation that would otherwise kill background work when the request returns. |

If you are adding a context parameter only so that a log line can take it, and
the function does no I/O and serves no request, that is the case for leaving
it alone.

`cmd/bridge` is the clearest example. It runs inside the per-bot workspace
container, the host reaches it over a Unix socket, and nothing collects that
container's stdout. Correlation fields there would be written into a stream
no one reads. Leave its logging as it is.

## Correlation fields

Added by the handler, never by the call site:

| Key | Source |
| --- | --- |
| `request_id` | The id echo's `RequestID` middleware assigns, put into the context by `httpx.RequestIDContext`. The same id the client receives, in the response header and in `apperror.Problem`. |
| `trace_id`, `span_id` | The span context, when tracing is configured — see [observability.md](observability.md). |

Absent identity means absent keys rather than empty ones: an empty `trace_id`
would match a query for records that have no trace at all.

Do not call `WithGroup` on a logger that should report correlation. Grouping
nests everything the record carries, these fields included, which moves them
to `group.request_id` and breaks queries written against the top level.

## Levels

| Level | Meaning |
| --- | --- |
| `ERROR` | This process failed at something it was asked to do. |
| `WARN` | Something is wrong but the operation continued, or a caller was refused. |
| `INFO` | A thing happened that an operator would want in the record. |
| `DEBUG` | Detail useful while working on this code. |

A rejected request is not this process failing. Authentication and validation
refusals are `WARN` at most, so that a filter on `ERROR` shows faults rather
than ordinary traffic.

## Naming

Keys are `snake_case`, enforced by `sloglint.key-naming-case`. This includes
the correlation fields: a log record is not an OpenTelemetry attribute set,
and log pipelines commonly flatten dots to underscores on ingest, so a dotted
spelling would be written one way and queried another.

Entity identifiers are `<entity>_id` (`bot_id`, `config_id`, `workspace_id`).
Durations use `slog.Duration`. Errors use `slog.Any("error", err)`.

Attributes are `slog.Attr` values, not loose key-value pairs
(`sloglint.attr-only`): an odd number of arguments silently renders as
`!BADKEY` rather than failing.

## What must never be logged

Request and response bodies, authorization headers, API keys, tokens, signed
URLs, model conversations, and tool arguments.

URLs need care because some carry authorisation in the query string.
`httpx.SafeRequestLogURI` returns only the path for those; both HTTP shells
use it, and a new one must too.

## The access log

One record per request, emitted by the shell, with `msg` = `request`:

`method`, `uri` (through `SafeRequestLogURI`), `status`, `latency`,
`remote_ip`. `request_id` and any trace identity come from the handler, so the
access log does not name them itself — one source for those fields, on every
record rather than on this line alone.

Both HTTP shells — the main server and the webhook tunnel listener — emit it.
A public entrance without one leaves a delivery that a third party reports as
failed with nothing to look up.
