package chatgptplan

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// UpstreamError retains diagnostic metadata without putting upstream messages,
// response bodies, or credentials in Error() or public responses.
type UpstreamError struct {
	Status    int    `json:"-"`
	Code      string `json:"-"`
	Param     string `json:"-"`
	RequestID string `json:"-"`
	Shape     string `json:"-"`
	kind      error
}

func (e *UpstreamError) Error() string { return e.kind.Error() }
func (e *UpstreamError) Unwrap() error { return e.kind }
func (e *UpstreamError) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("status", e.Status), slog.String("code", e.Code),
		slog.String("param", e.Param), slog.String("upstream_request_id", e.RequestID),
		slog.String("body_shape", e.Shape),
	)
}

type wireError struct {
	Code  string `json:"code"`
	Param string `json:"param"`
}

func readUpstreamError(resp *http.Response) *UpstreamError {
	err := &UpstreamError{Status: resp.StatusCode, RequestID: resp.Header.Get("X-Request-Id"), Shape: "unknown"}
	var body struct {
		Error  json.RawMessage `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) == nil {
		var failure wireError
		switch {
		case len(body.Error) > 0 && body.Error[0] == '{' && json.Unmarshal(body.Error, &failure) == nil:
			err.Code, err.Param, err.Shape = failure.Code, failure.Param, "error_object"
		case json.Unmarshal(body.Error, &err.Code) == nil:
			err.Shape = "oauth_error"
		case len(body.Detail) > 0:
			err.Shape = "detail"
		}
	}
	err.kind = responseError(err.Code, statusError(err.Status))
	return err
}
