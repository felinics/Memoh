# Observability

Logs answer what happened. Traces answer where the time went and which step
failed — the question that matters most here, because one agent turn strings
together a model call, tool executions inside a workspace container, database
work, and compaction, and "the reply was slow" does not say which of them was.

Log records are covered by [logging.md](logging.md). This file covers tracing:
what is instrumented, how to turn it on, and what it costs when it is off.

## Off by default, and off means off

With no collector configured, the process installs **no tracer provider**. It
does not build an exporter pointed at a default address, so nothing retries a
connection that cannot succeed, and nothing is batched or dropped in the
background.

What is installed unconditionally is **context propagation**: reading and
writing the W3C `traceparent` header. That is a map lookup on a header that is
usually absent, and it is not optional for a reason — see
[Why propagation is always on](#why-propagation-is-always-on).

## Turning it on

Point it at any collector that speaks OTLP — Grafana Alloy or Tempo, Jaeger,
SigNoz, Uptrace, a vendor endpoint:

```toml
[telemetry]
endpoint = "127.0.0.1:4317"
insecure = true
```

The standard OpenTelemetry environment variables work too and **take
precedence over the config file**, because they are what a container platform
injects and what an operator running other instrumented services already
knows:

| Variable | Effect |
| --- | --- |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector address; setting either enables export |
| `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`, `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` or `http/protobuf` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `key=value,key=value` sent with every export |
| `OTEL_TRACES_SAMPLER_ARG` | Sample ratio |
| `OTEL_SERVICE_NAME` | Overrides the reported `service.name` |
| `OTEL_SDK_DISABLED=true` | Disables export whatever else is set |

`OTEL_TRACES_SAMPLER` is deliberately **not** read beyond that ratio: claiming
to support sampler names that are not implemented would be worse than not
reading the variable at all.

Sampling is `ParentBased`, so `sample_ratio` governs traces this process
starts. A request that arrives already sampled stays sampled — deciding again
at each hop would cut traces in half at process boundaries, which is worse
than either keeping or dropping them whole.

## What produces spans

| Where | What | How |
| --- | --- | --- |
| HTTP server | One server span per request, named after the matched route (`GET /bots/:id`) | `telemetry.EchoServer`, on both HTTP shells |
| gRPC, internal RPC | Client and server spans, channel process ↔ server process | `otelgrpc` stats handlers in `internal/rpc` |
| gRPC, workspace bridge | Client spans, host → workspace container | `otelgrpc` client handler in `internal/workspace/bridge` |
| gRPC, remote runtime | Client spans, server → runtime over the WebSocket tunnel | `otelgrpc` client handler in `internal/userruntime` |
| PostgreSQL | One span per query, on all three pools | `telemetry.PgxTracer` |

Turn orchestration inside `internal/agent/application` is **not** instrumented
yet. That is the layer where a trace is most useful, and it is also the layer
still being restructured; span names and attributes are an interface that
dashboards and alerts are written against, so fixing them before the code
underneath settles would mean breaking them later. It is the next piece of
work, not an oversight.

`cmd/bridge` is not instrumented either, and that one is permanent for the
same reason its logs are not collected: it runs inside the per-bot workspace
container, which has no collector to export to. The host side of every bridge
call is traced as a client span, so the call is visible; what happens inside
the container is not.

## What must never be on a span

The same rule as log records, and for the same reason — a span attribute is no
safer a place for a credential than a log line. Request and response bodies,
authorization headers, API keys, tokens, signed URLs, model conversations,
tool arguments.

Two specific consequences:

- HTTP spans record the path through `httpx.SafeRequestLogURI`, never the
  query string, which on some routes carries an authorising token.
- Database spans record the SQL text but never the arguments. The text is
  code; the arguments are the contents of the rows being read and written.

## Correlation with logs

Every log record emitted with a context during a traced request carries
`trace_id` and `span_id` — that is the correlation handler in
`internal/logger`, described in [logging.md](logging.md). Server spans also
carry a `request_id` attribute holding the identifier the client received, so
a user quoting that identifier can be looked up in either system.

## Why propagation is always on

A propagator with no provider behind it has nothing to write, so leaving it
installed costs a header lookup. Making it conditional would cost something
much larger and much harder to diagnose: a deployment that enables tracing on
one service and not the others gets a separate disconnected trace per hop.
Every service looks correct in isolation, each produces spans, and the only
symptom is that the halves of one request never join up — with nothing logging
an error to say why.

`internal/rpc/trace_test.go` asserts the continuity end to end over a real
gRPC connection, because that failure has no other symptom.

## Cost when disabled

The OpenTelemetry API is already linked into these binaries regardless of this
feature — several dependencies, the Docker client among them, are
instrumented with it. What this adds on top is the SDK and the OTLP exporter:
around 1.9 MiB, on a binary of roughly 148 MiB.

At runtime, with no provider installed, each instrumented call resolves to a
no-op tracer that returns a shared non-recording span. The middleware and
tracers still run; they just have nothing to record to.
