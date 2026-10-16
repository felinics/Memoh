package dingtalk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

// attrValue returns the value of key among the error's attributes.
func attrValue(t *testing.T, err error, key string) (any, bool) {
	t.Helper()
	for _, attr := range errs.Analyze(context.Background(), err).Attrs {
		if attr.Key == key {
			return attr.Value.Any(), true
		}
	}
	return nil, false
}

// uploadStubClient points a client at a stub that answers the token endpoint
// and then the media upload with uploadBody.
func uploadStubClient(t *testing.T, uploadBody string) *apiClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			_, _ = w.Write([]byte(`{"accessToken":"tok","expireIn":7200}`))
			return
		}
		_, _ = w.Write([]byte(uploadBody))
	}))
	t.Cleanup(server.Close)
	client := newAPIClient("key", "secret")
	client.base = server.URL
	return client
}

func TestUploadMediaRejectionKeepsUpstreamTextOutOfTheError(t *testing.T) {
	t.Parallel()

	client := uploadStubClient(t, `{"errcode":40035,"errmsg":"dingtalk secret rejection"}`)
	_, err := client.uploadMedia(context.Background(), "image", "a.png", strings.NewReader("bytes"))
	if err == nil {
		t.Fatal("uploadMedia returned nil, want the upstream rejection")
	}
	if strings.Contains(err.Error(), "dingtalk secret rejection") {
		t.Fatalf("error carries the upstream text: %v", err)
	}
	if got, ok := attrValue(t, err, "errcode"); !ok || got != int64(40035) {
		t.Fatalf("errcode attr = %v (present %t), want 40035", got, ok)
	}
	if fault := errs.FaultOf(err); fault != apperror.FaultDependency {
		t.Fatalf("fault = %q, want dependency", fault)
	}
	if !strings.Contains(err.Error(), "dingtalk upload rejected") {
		t.Fatalf("error = %v, want the constant upload failure", err)
	}
}

func TestDownloadMessageFileNonOKStatusIsDependency(t *testing.T) {
	t.Parallel()

	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(downloadServer.Close)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			_, _ = w.Write([]byte(`{"accessToken":"tok","expireIn":7200}`))
			return
		}
		_, _ = w.Write([]byte(`{"downloadUrl":"` + downloadServer.URL + `/file"}`))
	}))
	t.Cleanup(apiServer.Close)

	client := newAPIClient("key", "secret")
	client.base = apiServer.URL

	body, _, err := client.downloadMessageFile(context.Background(), "robot-1", "code-1")
	if body != nil {
		_ = body.Close()
	}
	if err == nil {
		t.Fatal("downloadMessageFile returned nil, want the refusal")
	}
	if !strings.Contains(err.Error(), "dingtalk download file request failed") {
		t.Fatalf("error = %v, want the constant download failure", err)
	}
	if strings.Contains(err.Error(), "403") {
		t.Fatalf("error carries the status in text: %v", err)
	}
	if got, ok := attrValue(t, err, "status"); !ok || got != int64(http.StatusForbidden) {
		t.Fatalf("status attr = %v (present %t), want 403", got, ok)
	}
	if fault := errs.FaultOf(err); fault != apperror.FaultDependency {
		t.Fatalf("fault = %q, want dependency", fault)
	}
}
