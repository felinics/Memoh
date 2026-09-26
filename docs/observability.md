# Observability

Logs answer what happened. Traces answer where the time went and which step
failed — the question that matters most here, because one agent turn strings
together a model call, tool executions inside a workspace container, database
work, and compaction, and "the reply was slow" does not say which of them was.

Log records are covered by [logging.md](logging.md). This file covers tracing
and the metrics exported with it: what is instrumented, how to turn it on, and
what it costs when it is off.

## Off by default, and off means off

With no collector configured, the process installs **no tracer provider and
no meter provider**. It does not build an exporter pointed at a default address, so nothing retries a
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
| `OTEL_EXPORTER_OTLP_TRACES_INSECURE`, `OTEL_EXPORTER_OTLP_INSECURE` | Whether to skip TLS for a scheme-less endpoint |
| `OTEL_EXPORTER_OTLP_HEADERS` | `key=value,key=value` sent with every export |
| `OTEL_TRACES_SAMPLER_ARG` | Sample ratio |
| `OTEL_SERVICE_NAME` | Overrides the reported `service.name` |
| `OTEL_SDK_DISABLED=true` | Disables export whatever else is set |
| `OTEL_METRICS_EXPORTER=none` | Disables metric export; traces stay on |
| `OTEL_METRIC_EXPORT_INTERVAL` | Milliseconds between metric exports (SDK default 60000) |

`OTEL_TRACES_SAMPLER` is deliberately **not** read beyond that ratio: claiming
to support sampler names that are not implemented would be worse than not
reading the variable at all.

Give the endpoint a scheme where you can. `http://collector:4317` and
`https://collector:4317` each say what to do on their own; `collector:4317`
does not, and then TLS is decided by the insecure setting, which defaults to
off. Pointing a scheme-less endpoint at a plaintext collector therefore ends
in a TLS handshake error that names neither the setting nor the endpoint.

Sampling is `ParentBased`, so `sample_ratio` governs traces this process
starts. A request that arrives already sampled stays sampled — deciding again
at each hop would cut traces in half at process boundaries, which is worse
than either keeping or dropping them whole.

## What produces spans

| Where | What | How |
| --- | --- | --- |
| HTTP server | One server span per request, named after the matched route (`GET /bots/:id`). WebSocket upgrades get a `ws.handshake` span from the handler instead | `telemetry.EchoServer`, on both HTTP shells |
| gRPC, internal RPC | Client and server spans, channel process ↔ server process | `otelgrpc` stats handlers in `internal/rpc` |
| gRPC, workspace bridge | Client spans, host → workspace container | `otelgrpc` client handler in `internal/workspace/bridge` |
| gRPC, remote runtime | Client spans, server → runtime over the WebSocket tunnel | `otelgrpc` client handler in `internal/userruntime` |
| PostgreSQL | One span per query, on all three pools | `telemetry.PgxTracer` |
| Channel inbound | One span per queued message, linked to the request that enqueued it | `Manager.runInboundTask` |
| Agent turn | One span per turn, with its outcome | `startTurnSpan`, on both entry points |
| Model call | One span per provider call | `providerCallObserver`, in `internal/agent/runtime/native` |
| Tool call | One span per tool execution | `wrapToolTracing`, in `assembleTools` |

The liveness probe is the one request that gets no span at all. It runs every
few seconds forever and says the same thing every time, so tracing it fills a
backend with probes. `/ping` is not in that category: it reports the server's
capabilities and the desktop app calls it to decide whether a server is
usable.

A turn therefore reads as a tree:

```
agent.turn                       outcome=completed
├ agent.model.stream             call_index=0, first_part_ms=…
├ agent.tool ask_user
│ ├ bridgepb.ContainerService/ListDir
│ └ postgresql.query
└ agent.model.stream             call_index=1, first_part_ms=…
```

### Where a turn's trace begins

A turn is usually its own trace, with a link to whatever caused it rather
than a parent. The cause is a WebSocket that stays open for hours, or an HTTP
request that was answered before the turn started, and making either the
parent produces a trace that is wrong in a specific way: a parent that ends
before its children, or one that never ends, with every turn of the
conversation sharing its trace id. Asking how long an answer took then
returns the length of the session.

A turn that arrives over the internal RPC is the exception, and keeps the
ordinary parent-child edge: the calling process is waiting on it, so that one
trace really does span both.

The ingress decides which, by recording a `telemetry.Trigger` in the context
it hands on. `startTurnSpan` links when it finds one and nests when it does
not.

### What the span names promise

One model span per call to the provider, not one per turn. The SDK runs the
whole loop — model, tools, model again — inside a single call, so a span
around that call covers every round at once and can only be ended on one of
them; `call_index` is what tells the rounds apart.

`first_part_ms` is how long the provider took to say anything, which is not
the duration of the call: an answer that streams tool arguments for twenty
seconds spends almost none of it waiting. It is an attribute rather than a
span of its own, because both numbers describe the same call and nesting them
would double every model row in a waterfall.

The model spans are leaves beside the tool spans, not their parents. The SDK
keeps the context it is handed for the whole stream, so passing it a span's
context would file every tool call under a model span.

The `agent.*` names are **not a stable interface yet**. The agent application
package is still being restructured, and these names are placed at its
current seams. Write a dashboard against them if it helps; expect to revisit
it. The framework-level names above (HTTP routes, gRPC methods,
`postgresql.query`) do not carry that caveat — they follow the shape of the
protocol, not of our code.

Turn orchestration is instrumented at its boundaries, not throughout.
Assembly, memory retrieval and compaction have no spans of their own yet;
they show up as time inside `agent.turn` that no child accounts for.

`cmd/bridge` is not instrumented either, and that one is permanent for the
same reason its logs are not collected: it runs inside the per-bot workspace
container, which has no collector to export to. The host side of every bridge
call is traced as a client span, so the call is visible; what happens inside
the container is not.

## Metrics

Metrics go to the same collector as traces, over the same protocol, with the
same headers and the same resource. Setting `OTEL_METRICS_EXPORTER=none`
keeps traces and drops metrics, for a collector that accepts only one of them.

`telemetry.EchoServer` records `http.server.request.duration`, in seconds, for
the same requests it traces: WebSocket upgrades and probes are left out. The
attributes are `http.request.method` (unknown methods become `_OTHER`),
`url.scheme`, `http.route`, `http.response.status_code` and
`network.protocol.version`. The status is the one the client received, so a
handler that returns an unmapped error counts as a 500. A response with
`Content-Type: text/event-stream` also carries `http.response.streaming=true`;
its duration is how long the page stayed open, so latency panels should
exclude it.

The path, the host, the port and the request id are not attributes. Each
distinct value would be a separate series for the life of the process.

The buckets run from 5 ms to 300 s. The first fourteen are the ones the HTTP
semantic conventions recommend and stop at 10 s; the rest are added so that a
slow chat request still has a quantile.

Installing a meter provider also turns on the RPC metrics that `otelgrpc`
records on every gRPC connection in the table above, and the HTTP client
metrics of libraries instrumented with OpenTelemetry, the Docker client among
them.

## What must never be on a span

The same rule as log records, and for the same reason — a span attribute is no
safer a place for a credential than a log line. Request and response bodies,
authorization headers, API keys, tokens, signed URLs, model conversations,
tool arguments.

Specifically:

- HTTP spans record the path through `httpx.SafeRequestLogURI`, never the
  query string, which on some routes carries an authorising token.
- Database spans record the SQL text but never the arguments. The text is
  code; the arguments are the contents of the rows being read and written.
- Tool spans record the tool's name, never its input. A tool call carries
  file contents, shell commands and whatever the user asked for.
- Model spans record the model and provider, never the prompt, the reply, or
  the tool definitions sent with the request.

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
