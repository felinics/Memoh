package errs

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/felinics/memoh/internal/apperror"
)

func TestText(t *testing.T) {
	u, _ := url.Parse("https://user:secret@example.test/path?token=abc")
	err := Wrap(causedError{code: codeServer, cause: stderrors.New("see https://x.test/a?sig=secret")}, "request "+u.String())
	text := Text(err)
	if strings.Contains(text, "secret") || strings.Contains(text, "user:") || !strings.Contains(text, "workspace.unreachable: see https://x.test/a") {
		t.Fatalf("text=%q", text)
	}
}

func TestRedactURLs(t *testing.T) {
	got := RedactURLs(`upstream said "https://u:p@blob.test/o?sig=abc#f", retry`)
	if want := `upstream said "https://blob.test/o", retry`; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRedactURLsAnyScheme(t *testing.T) {
	for in, want := range map[string]string{ //nolint:gosec // fake credentials the redaction must remove
		`parse database url: postgres://app:hunter2@db:5432/memoh?sslmode=disable`: `parse database url: postgres://db:5432/memoh`,
		`dial postgres:///memoh?host=/tmp&password=hunter2`:                        `dial postgres:///memoh`,
		`bad dsn postgres://app:hun%zzter2@db/memoh`:                               `bad dsn postgres://[redacted]`,
		`redis://:hunter2@cache:6379/0 refused`:                                    `redis://cache:6379/0 refused`,
	} {
		if got := RedactURLs(in); got != want || strings.Contains(got, "hunter2") {
			t.Errorf("RedactURLs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogAttrsOmission(t *testing.T) {
	r := Analyze(context.Background(), New("bad"))
	for _, a := range r.LogAttrs() {
		if a.Key == "remote" {
			t.Fatal("remote must be omitted")
		}
	}
}

func TestTextRendering(t *testing.T) {
	signedURL := "https://user:secret@files.example.test/download?token=abc" //nolint:gosec // fake credentials the redaction must remove
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"url error", &url.Error{Op: "GET", URL: signedURL, Err: stderrors.New("failed")}, `GET "https://files.example.test/download": failed`},
		{"plain text", New("download " + signedURL), "download https://files.example.test/download"},
		{"fmt public", fmt.Errorf("request: %w", apperror.New(codeClient, map[string]string{"field": "not-for-log"})), "request: bot.name_taken"},
		{"public error", apperror.New(codeServer, nil), "workspace.unreachable"},
		{"cause only", causedError{code: codeServer, cause: New("source")}, "workspace.unreachable: source"},
		{"joined branches", stderrors.Join(New("first"), New("second")), "first; second"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.err); got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
		})
	}
	wrapped := Wrap(&url.Error{Op: "GET", URL: signedURL, Err: stderrors.New("failed")}, "fetch")
	if got := wrapped.Error(); strings.Contains(got, "secret") || strings.Contains(got, "token") {
		t.Fatalf("Error() leaked URL: %q", got)
	}
}

func jsonLogAttrs(t *testing.T, r Report) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.LogAttrs(context.Background(), slog.LevelError, "failure", r.LogAttrs()...)
	var data map[string]any
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLogAttrsJSON(t *testing.T) {
	wrapped := WrapDependency(New("root", slog.String("id", "inner"), slog.Int("count", 3)), "outer", slog.String("id", "outer"))
	remote := Remote(stderrors.Join(remoteStatus(t, codes.Unavailable, "unavailable", "down", "server"), wrapped))
	r := Analyze(context.Background(), remote)
	r.Panic = true
	data := jsonLogAttrs(t, r)
	for key, want := range map[string]any{"fault": "dependency", "reason": "down", "remote": true, "remote_fault": "server", "panic": true} {
		if data[key] != want {
			t.Errorf("%s = %v, want %v", key, data[key], want)
		}
	}
	if text, _ := data["error"].(string); !strings.HasSuffix(text, "; outer: root") {
		t.Errorf("error = %q", text)
	}
	attrs, ok := data["error_attrs"].(map[string]any)
	if !ok || attrs["id"] != "outer" || attrs["count"] != float64(3) {
		t.Fatalf("attrs = %#v", data["error_attrs"])
	}
	source, ok := data["error_source"].(map[string]any)
	if !ok || source["file"] == "" || source["function"] == "" || source["line"] == nil {
		t.Fatalf("source = %#v", data["error_source"])
	}
	stack, ok := data["error_stack"].([]any)
	if !ok || len(stack) < 1 || len(stack) > 16 || !reflect.DeepEqual(source, stack[0]) {
		t.Fatalf("stack = %#v, source = %#v", data["error_stack"], source)
	}

	bare := jsonLogAttrs(t, Analyze(context.Background(), stderrors.New("bare")))
	if bare["fault"] != "server" || bare["reason"] != "internal" || bare["error"] != "bare" {
		t.Fatalf("bare = %#v", bare)
	}
	for _, key := range []string{"error_source", "error_stack", "error_attrs", "remote", "remote_fault", "panic"} {
		if _, exists := bare[key]; exists {
			t.Errorf("bare log must omit %q", key)
		}
	}
	if !Analyze(context.Background(), stderrors.New("bare")).Unlocated {
		t.Fatal("bare error must be unlocated")
	}
}
