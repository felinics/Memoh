package models

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
)

type trajectoryHTTPTransport struct {
	base http.RoundTripper
}

func clientWithTrajectory(client *http.Client) *http.Client {
	if _, ok := client.Transport.(*trajectoryHTTPTransport); ok {
		return client
	}
	copied := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copied.Transport = &trajectoryHTTPTransport{base: base}
	return &copied
}

func (t *trajectoryHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder := trajectory.FromContext(req.Context())
	if recorder == nil || req.Method != http.MethodPost || req.Body == nil || !generationRequestPath(req.URL.Path) {
		return t.base.RoundTrip(req)
	}
	body := req.Body
	if req.GetBody != nil {
		copyBody, err := req.GetBody()
		if err != nil {
			recorder.Record(req.Context(), "capture_error", nil, trajectory.Block{Kind: "capture_error", Content: "request_body_unavailable"})
			return t.base.RoundTrip(req)
		}
		body = copyBody
		defer func() { _ = body.Close() }()
	}
	data, err := io.ReadAll(body)
	if err != nil {
		recorder.Record(req.Context(), "capture_error", nil, trajectory.Block{Kind: "capture_error", Content: "request_body_read_failed"})
		if req.GetBody != nil {
			return t.base.RoundTrip(req)
		}
		_ = body.Close()
		return nil, err
	}
	if req.GetBody == nil {
		copied := req.Clone(req.Context())
		copied.Body = struct {
			io.Reader
			io.Closer
		}{bytes.NewReader(data), body}
		req = copied
	}
	sequence := recorder.Record(req.Context(), "wire_request", nil,
		trajectory.JSONBlock("endpoint", "endpoint", map[string]string{
			"method": req.Method, "url": req.URL.Scheme + "://" + req.URL.Host + req.URL.EscapedPath(),
		}),
		trajectory.Block{Kind: "request_body", Label: "request", Format: "json", Content: string(data)},
	)
	started := time.Now()
	response, err := t.base.RoundTrip(req)
	outcome := map[string]any{"wire_request": sequence, "elapsed_ms": time.Since(started).Milliseconds()}
	if response != nil {
		outcome["status"] = response.StatusCode
	}
	if err != nil {
		outcome["outcome"] = "transport_error"
	}
	recorder.Record(req.Context(), "wire_result", nil, trajectory.JSONBlock("transport", "response_headers_received", outcome))
	return response, err
}

func generationRequestPath(path string) bool {
	return strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/responses") ||
		strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, ":generateContent") ||
		strings.HasSuffix(path, ":streamGenerateContent")
}
