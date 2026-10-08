package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/rpc"
)

const boundaryTestSecret = "boundary-test-secret"

// boundaryTestHandler registers routes that end the way a handler can end:
// returning an error, panicking, or writing a server status itself.
type boundaryTestHandler struct {
	err error
}

func (h boundaryTestHandler) Register(e *echo.Echo) {
	e.GET("/probe", func(echo.Context) error { return h.err })
	e.HEAD("/probe", func(echo.Context) error { return h.err })
	e.GET("/panic", func(echo.Context) error { panic("synthetic panic value") })
	e.GET("/written", func(c echo.Context) error { return c.NoContent(http.StatusInternalServerError) })
	e.POST("/bind", func(c echo.Context) error {
		var body struct {
			Count  int `json:"count"`
			Config struct {
				Retries int `json:"retries"`
			} `json:"config"`
		}
		return c.Bind(&body)
	})
	e.POST("/channels/:platform/webhook/:id", func(c echo.Context) error {
		_, err := io.ReadAll(c.Request().Body)
		return err
	})
}

type boundaryResult struct {
	rec     *httptest.ResponseRecorder
	records []map[string]any
	spans   []sdktrace.ReadOnlySpan
}

// request is the one result record of the request.
func (r boundaryResult) request(t *testing.T) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, record := range r.records {
		if record["msg"] == "request" {
			found = append(found, record)
		}
	}
	if len(found) != 1 {
		t.Fatalf("request records = %d, want 1: %v", len(found), r.records)
	}
	return found[0]
}

func (r boundaryResult) problem(t *testing.T) Problem {
	t.Helper()
	if got := r.rec.Header().Get(echo.HeaderContentType); got != "application/problem+json" {
		t.Fatalf("content-type = %q, body %s", got, r.rec.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(r.rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v: %s", err, r.rec.Body.String())
	}
	return problem
}

func serveBoundary(t *testing.T, handlerErr error, req *http.Request) boundaryResult {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	token, _, err := auth.GenerateToken("boundary-user", boundaryTestSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)

	var logs bytes.Buffer
	srv := NewServer(logger.New(&logs, "debug", "json"), ":0", boundaryTestSecret, boundaryTestHandler{err: handlerErr})
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return boundaryResult{rec: rec, records: records, spans: recorder.Ended()}
}

// rpcRefusal is what an RPC client returns when the server refused the call
// with a client-fault catalog code: the restored apperror under the remote
// marker, not forwarded.
func rpcRefusal(t *testing.T) error {
	t.Helper()
	st, err := status.New(grpccodes.PermissionDenied, "agent not enabled").WithDetails(&errdetails.ErrorInfo{
		Reason:   string(apperror.CodeACPAgentNotEnabled),
		Metadata: map[string]string{rpc.MetadataFault: string(apperror.FaultClient)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rpc.Decode(st.Err(), nil, nil)
}

func TestBoundaryAnswersEveryErrorWithAProblem(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		method    string
		path      string
		body      string
		status    int
		code      apperror.Code
		fault     apperror.Fault
		level     string
		errorText string
	}{
		{name: "unknown route", path: "/nowhere", status: http.StatusNotFound, code: apperror.CodeHTTPNotFound, fault: apperror.FaultClient, level: "INFO"},
		{name: "method not allowed", method: http.MethodDelete, path: "/probe", status: http.StatusMethodNotAllowed, code: apperror.CodeHTTPMethodNotAllowed, fault: apperror.FaultClient, level: "INFO"},
		{name: "body too large", method: http.MethodPost, path: "/channels/line/webhook/cfg-1", body: strings.Repeat("x", 2<<20), status: http.StatusRequestEntityTooLarge, code: apperror.CodeHTTPPayloadTooLarge, fault: apperror.FaultClient, level: "INFO"},
		{name: "http error message stays in the record", err: echo.NewHTTPError(http.StatusBadRequest, "synthetic handler message"), path: "/probe", status: http.StatusBadRequest, code: apperror.CodeHTTPBadRequest, fault: apperror.FaultClient, level: "INFO", errorText: "synthetic handler message"},
		{name: "client status without a framework code", err: echo.NewHTTPError(http.StatusUnprocessableEntity), path: "/probe", status: http.StatusBadRequest, code: apperror.CodeHTTPBadRequest, fault: apperror.FaultClient, level: "INFO"},
		{name: "server http error", err: echo.NewHTTPError(http.StatusInternalServerError, "synthetic query failed"), path: "/probe", status: http.StatusInternalServerError, code: apperror.CodeInternal, fault: apperror.FaultServer, level: "ERROR", errorText: "synthetic query failed"},
		{name: "untranslated error", err: errors.New("dial tcp 10.0.0.9:5432: synthetic refusal"), path: "/probe", status: http.StatusInternalServerError, code: apperror.CodeInternal, fault: apperror.FaultServer, level: "ERROR", errorText: "synthetic refusal"},
		{name: "dependency error", err: errs.WrapDependency(errors.New("synthetic upstream reset"), "call upstream"), path: "/probe", status: http.StatusInternalServerError, code: apperror.CodeInternal, fault: apperror.FaultDependency, level: "ERROR", errorText: "synthetic upstream reset"},
		{name: "public error", err: apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("synthetic socket refused"), nil), path: "/probe", status: http.StatusServiceUnavailable, code: apperror.CodeWorkspaceUnreachable, fault: apperror.FaultServer, level: "ERROR", errorText: "synthetic socket refused"},
		{name: "provider error", err: apperror.Wrap(apperror.CodeAgentProviderAuthFailed, errors.New("synthetic api error 401"), nil), path: "/probe", status: http.StatusBadGateway, code: apperror.CodeAgentProviderAuthFailed, fault: apperror.FaultDependency, level: "ERROR", errorText: "synthetic api error 401"},
		{name: "external agent error", err: apperror.Wrap(apperror.CodeACPAgentNotEnabled, errors.New("synthetic agent codex disabled"), nil), path: "/probe", status: http.StatusForbidden, code: apperror.CodeACPAgentNotEnabled, fault: apperror.FaultClient, level: "INFO", errorText: "synthetic agent codex disabled"},
		{name: "native client status", err: status.Error(grpccodes.InvalidArgument, "synthetic payload rejected"), path: "/probe", status: http.StatusBadRequest, code: apperror.CodeHTTPBadRequest, fault: apperror.FaultClient, level: "INFO", errorText: "synthetic payload rejected"},
		{name: "remote refusal of this process", err: errs.Wrap(rpcRefusal(t), "call runtime"), path: "/probe", status: http.StatusInternalServerError, code: apperror.CodeInternal, fault: apperror.FaultServer, level: "ERROR"},
		{name: "forwarded remote refusal", err: rpc.Forward(rpcRefusal(t)), path: "/probe", status: http.StatusForbidden, code: apperror.CodeACPAgentNotEnabled, fault: apperror.FaultClient, level: "INFO"},
		{name: "code outside the catalog", err: apperror.Wrap("synthetic.unregistered", errors.New("synthetic cause"), nil), path: "/probe", status: http.StatusInternalServerError, code: apperror.CodeInternal, fault: apperror.FaultServer, level: "ERROR", errorText: "synthetic cause"},
		{name: "panic", path: "/panic", status: http.StatusInternalServerError, code: apperror.CodeInternal, fault: apperror.FaultServer, level: "ERROR", errorText: "panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			res := serveBoundary(t, tc.err, httptest.NewRequest(method, tc.path, body))

			if res.rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", res.rec.Code, tc.status, res.rec.Body.String())
			}
			problem := res.problem(t)
			if problem.Code != string(tc.code) || problem.Status != tc.status || problem.Type != apperror.TypeURI(tc.code) {
				t.Fatalf("problem = %+v, want code %s status %d", problem, tc.code, tc.status)
			}
			if problem.Fault != tc.fault {
				t.Errorf("problem fault = %q, want %q", problem.Fault, tc.fault)
			}
			if problem.RequestID == "" || problem.RequestID != res.rec.Header().Get(echo.HeaderXRequestID) {
				t.Errorf("problem request_id = %q, header %q", problem.RequestID, res.rec.Header().Get(echo.HeaderXRequestID))
			}
			if tc.errorText != "" && strings.Contains(res.rec.Body.String(), tc.errorText) {
				t.Errorf("response exposed the error text: %s", res.rec.Body.String())
			}

			record := res.request(t)
			if record["level"] != tc.level || record["fault"] != string(tc.fault) {
				t.Errorf("record level = %v fault = %v, want %s %s", record["level"], record["fault"], tc.level, tc.fault)
			}
			if got, _ := record["status"].(float64); int(got) != tc.status {
				t.Errorf("record status = %v, want %d", record["status"], tc.status)
			}
			if text, _ := record["error"].(string); !strings.Contains(text, tc.errorText) {
				t.Errorf("record error = %q, want it to contain %q", text, tc.errorText)
			}
		})
	}
}

func TestBoundaryAnswersACanceledRequestAsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := serveBoundary(t, context.Canceled, httptest.NewRequest(http.MethodGet, "/probe", nil).WithContext(ctx))

	if res.rec.Code != 499 {
		t.Fatalf("status = %d, want 499", res.rec.Code)
	}
	problem := res.problem(t)
	if problem.Code != string(apperror.CodeCanceled) || problem.Fault != apperror.FaultCanceled {
		t.Fatalf("problem = %+v", problem)
	}
	record := res.request(t)
	if record["level"] != "INFO" || record["fault"] != string(apperror.FaultCanceled) || record["reason"] != "canceled" {
		t.Fatalf("record = %v", record)
	}
}

func TestBoundaryWritesNoBodyForHead(t *testing.T) {
	res := serveBoundary(t, errors.New("synthetic failure"), httptest.NewRequest(http.MethodHead, "/probe", nil))

	if res.rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", res.rec.Code)
	}
	if res.rec.Body.Len() != 0 {
		t.Fatalf("HEAD response has a body: %q", res.rec.Body.String())
	}
	if got := res.rec.Header().Get(echo.HeaderContentType); got != "application/problem+json" {
		t.Fatalf("content-type = %q", got)
	}
	if record := res.request(t); record["level"] != "ERROR" {
		t.Fatalf("record = %v", record)
	}
}

func TestBoundaryRecordsThePanicWithItsStack(t *testing.T) {
	res := serveBoundary(t, nil, httptest.NewRequest(http.MethodGet, "/panic", nil))

	record := res.request(t)
	if record["panic"] != true {
		t.Fatalf("record does not mark the panic: %v", record)
	}
	stack, _ := record["error_stack"].([]any)
	if len(stack) == 0 {
		t.Fatalf("record has no stack: %v", record)
	}
	if strings.Contains(res.rec.Body.String(), "synthetic panic value") || strings.Contains(record["error"].(string), "synthetic panic value") {
		t.Fatal("the panic value left the process")
	}
}

func TestBoundaryRecordsAServerStatusWrittenWithoutAnError(t *testing.T) {
	res := serveBoundary(t, nil, httptest.NewRequest(http.MethodGet, "/written", nil))

	record := res.request(t)
	if record["level"] != "ERROR" || record["fault"] != string(apperror.FaultServer) {
		t.Fatalf("record = %v", record)
	}
}

func TestBoundaryPutsTheTraceOnTheProblemAndTheSpan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		spanError bool
	}{
		{"client", echo.NewHTTPError(http.StatusForbidden), false},
		{"server", errors.New("synthetic failure"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := serveBoundary(t, tc.err, httptest.NewRequest(http.MethodGet, "/probe", nil))

			if len(res.spans) != 1 {
				t.Fatalf("spans = %d, want 1", len(res.spans))
			}
			span := res.spans[0]
			problem := res.problem(t)
			if problem.TraceID == "" || problem.TraceID != span.SpanContext().TraceID().String() {
				t.Errorf("problem trace_id = %q, span %s", problem.TraceID, span.SpanContext().TraceID())
			}
			if got := res.request(t)["trace_id"]; got != problem.TraceID {
				t.Errorf("record trace_id = %v, problem %q", got, problem.TraceID)
			}
			if got := span.Status().Code == codes.Error; got != tc.spanError {
				t.Errorf("span error = %v, want %v", got, tc.spanError)
			}
			if len(span.Events()) != 0 {
				t.Errorf("span records the error as an event: %v", span.Events())
			}
		})
	}
}

func TestBoundaryWritesNoErrorFieldsForASuccess(t *testing.T) {
	res := serveBoundary(t, nil, httptest.NewRequest(http.MethodGet, "/probe", nil))

	record := res.request(t)
	if record["level"] != "INFO" {
		t.Fatalf("record = %v", record)
	}
	for _, key := range []string{"fault", "reason", "error"} {
		if _, found := record[key]; found {
			t.Errorf("successful request record has %s: %v", key, record)
		}
	}
}

// A JSON value of the wrong type is answered with the field it was found in,
// named by its key; malformed JSON has no field and stays a bad request.
func TestBoundaryAnswersABindingTypeErrorWithItsField(t *testing.T) {
	for name, tc := range map[string]struct {
		body  string
		code  apperror.Code
		field string
	}{
		"top-level key":  {`{"count":"three"}`, apperror.CodeRequestFieldInvalid, "count"},
		"nested key":     {`{"config":{"retries":"many"}}`, apperror.CodeRequestFieldInvalid, "config.retries"},
		"malformed JSON": {`{"count":`, apperror.CodeHTTPBadRequest, ""},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			res := serveBoundary(t, nil, req)
			if res.rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s", res.rec.Code, res.rec.Body.String())
			}
			problem := res.problem(t)
			if problem.Code != string(tc.code) || problem.Args["field"] != tc.field {
				t.Fatalf("problem = %+v, want code %s field %q", problem, tc.code, tc.field)
			}
			if problem.Fault != apperror.FaultClient {
				t.Fatalf("fault = %q, want client", problem.Fault)
			}
		})
	}
}
