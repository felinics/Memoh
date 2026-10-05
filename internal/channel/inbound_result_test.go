package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/logger"
)

func decodeLogRecords(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

// A queued message gets exactly one result line, whatever its outcome, and
// the handler does not log the error it returns.
func TestInboundTaskWritesOneResultLine(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		err       error
		wantLevel string
		wantFault string
		wantError string
	}{
		{name: "success", ctx: context.Background(), wantLevel: "INFO"},
		{name: "failure", ctx: context.Background(), err: errors.New("resolve route: connection reset"), wantLevel: "ERROR", wantFault: "server", wantError: "resolve route: connection reset"},
		{name: "canceled", ctx: canceled, err: context.Canceled, wantLevel: "INFO", wantFault: "canceled", wantError: "context canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			m := NewManager(logger.New(&logs, "debug", "json"), NewRegistry(), &fakeConfigStore{}, &fakeInboundProcessor{err: tt.err})
			m.runInboundTask(tt.ctx, inboundTask{
				cfg: ChannelConfig{ID: "cfg-1", BotID: "bot-1"},
				msg: InboundMessage{Channel: ChannelType("test")},
			})

			records := decodeLogRecords(t, &logs)
			if len(records) != 1 || records[0]["msg"] != "inbound message" {
				t.Fatalf("records = %v, want one inbound message line", records)
			}
			record := records[0]
			if record["level"] != tt.wantLevel {
				t.Fatalf("level = %v, want %s: %v", record["level"], tt.wantLevel, record)
			}
			if record["channel"] != "test" || record["bot_id"] != "bot-1" {
				t.Fatalf("channel/bot_id = %v/%v", record["channel"], record["bot_id"])
			}
			if tt.wantFault == "" {
				if _, ok := record["error"]; ok {
					t.Fatalf("success record carries an error: %v", record)
				}
				return
			}
			if record["fault"] != tt.wantFault || record["error"] != tt.wantError {
				t.Fatalf("fault/error = %v/%v, want %s/%s", record["fault"], record["error"], tt.wantFault, tt.wantError)
			}
		})
	}
}

// A message an adapter connection hands to its handler is an inbound unit
// too: one result line and its own channel.inbound span, with middlewares
// still outside the unit.
func TestConnectionInboundWritesOneResultLine(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	var logs bytes.Buffer
	adapter := &fakeAdapter{channelType: ChannelType("test")}
	m := NewManager(logger.New(&logs, "debug", "json"), NewRegistry(), &fakeConfigStore{},
		&fakeInboundProcessor{err: errors.New("resolve route: connection reset")})
	m.RegisterAdapter(adapter)
	var middlewareSawSpan bool
	m.Use(func(next InboundHandler) InboundHandler {
		return func(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error {
			middlewareSawSpan = trace.SpanFromContext(ctx).SpanContext().IsValid()
			return next(ctx, cfg, msg)
		}
	})
	cfg := ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: ChannelType("test"), UpdatedAt: time.Now()}
	if err := m.EnsureConnection(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	logs.Reset()

	adapter.mu.Lock()
	handler := adapter.handlers[0]
	adapter.mu.Unlock()
	err := handler(context.Background(), cfg, InboundMessage{Channel: ChannelType("test"), BotID: "bot-1"})
	if err == nil || err.Error() != "resolve route: connection reset" {
		t.Fatalf("handler error = %v", err)
	}
	if middlewareSawSpan {
		t.Fatal("middleware ran inside the inbound span")
	}

	records := decodeLogRecords(t, &logs)
	if len(records) != 1 || records[0]["msg"] != "inbound message" {
		t.Fatalf("records = %v, want one inbound message line", records)
	}
	if records[0]["level"] != "ERROR" || records[0]["fault"] != "server" {
		t.Fatalf("level/fault = %v/%v", records[0]["level"], records[0]["fault"])
	}
	var spans []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "channel.inbound" {
			spans = append(spans, span)
		}
	}
	if len(spans) != 1 || spans[0].Parent().IsValid() || spans[0].Status().Code != codes.Error {
		t.Fatalf("channel.inbound spans = %d, want one root span with error status", len(spans))
	}
}

// Each inbound message reports a request id of its own, on its result line
// and on what the middlewares log for it, rather than the id of the request
// that enqueued it or that started the connection it arrived on.
func TestInboundUnitHasItsOwnRequestID(t *testing.T) {
	var logs bytes.Buffer
	adapter := &fakeAdapter{channelType: ChannelType("test")}
	m := NewManager(logger.New(&logs, "debug", "json"), NewRegistry(), &fakeConfigStore{}, &fakeInboundProcessor{})
	m.RegisterAdapter(adapter)
	var middlewareIDs []string
	m.Use(func(next InboundHandler) InboundHandler {
		return func(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error {
			middlewareIDs = append(middlewareIDs, logger.RequestIDFromContext(ctx))
			return next(ctx, cfg, msg)
		}
	})
	configRequest := logger.ContextWithRequestID(context.Background(), "config-put")
	cfg := ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: ChannelType("test"), UpdatedAt: time.Now()}
	if err := m.EnsureConnection(configRequest, cfg); err != nil {
		t.Fatal(err)
	}
	logs.Reset()

	adapter.mu.Lock()
	handler := adapter.handlers[0]
	connectCtx := adapter.connectCtxs[0]
	adapter.mu.Unlock()
	if id := logger.RequestIDFromContext(connectCtx); id != "" {
		t.Fatalf("connection context request_id = %q, want none", id)
	}
	for range 2 {
		if err := handler(connectCtx, cfg, InboundMessage{Channel: ChannelType("test"), BotID: "bot-1"}); err != nil {
			t.Fatal(err)
		}
	}
	m.runInboundTask(logger.ContextWithRequestID(context.Background(), "webhook-delivery"), inboundTask{
		cfg: cfg,
		msg: InboundMessage{Channel: ChannelType("test")},
	})

	records := decodeLogRecords(t, &logs)
	if len(records) != 3 {
		t.Fatalf("records = %v, want three inbound message lines", records)
	}
	seen := map[string]bool{}
	for i, record := range records {
		id, _ := record["request_id"].(string)
		if id == "" || id == "config-put" || id == "webhook-delivery" || seen[id] {
			t.Fatalf("record %d request_id = %q, want a fresh id: %v", i, id, records)
		}
		seen[id] = true
		if i < len(middlewareIDs) && middlewareIDs[i] != id {
			t.Fatalf("middleware saw request_id %q, result line %q", middlewareIDs[i], id)
		}
	}
}
