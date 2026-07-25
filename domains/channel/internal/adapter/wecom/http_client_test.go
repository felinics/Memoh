package wecom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadFile_ParsesFilename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''hello%20wecom.txt")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := NewHTTPClient(HTTPClientOptions{})
	file, err := client.DownloadFile(context.Background(), srv.URL+"/download.bin")
	if err != nil {
		t.Fatalf("DownloadFile error = %v", err)
	}
	if file.FileName != "hello wecom.txt" {
		t.Fatalf("unexpected filename: got=%q", file.FileName)
	}
	if string(file.Data) != "ok" {
		t.Fatalf("unexpected payload: got=%q", string(file.Data))
	}
}

func TestExtractFilename_FallbackPath(t *testing.T) {
	got := extractFilename("", "https://example.com/files/a.png")
	if got != "a.png" {
		t.Fatalf("unexpected filename fallback: got=%q", got)
	}
}
