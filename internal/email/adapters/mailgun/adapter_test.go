package mailgun

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseWebhookFormRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	const boundary = "mailgun-test-boundary"
	body := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"payload\"\r\n\r\n" +
		strings.Repeat("x", 256) + "\r\n" +
		"--" + boundary + "--\r\n"
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/mailgun/webhook",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

	err := parseWebhookForm(req, 128)
	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) {
		t.Fatalf("parseWebhookForm() error = %v, want *http.MaxBytesError", err)
	}
}
