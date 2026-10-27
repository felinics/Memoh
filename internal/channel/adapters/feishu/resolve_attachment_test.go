package feishu

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"

	"github.com/felinics/memoh/internal/channel"
)

func TestResolveAttachmentReadsFileBody(t *testing.T) {
	// Do not run in parallel: intercept the SDK's default HTTP transport.
	const messageID, fileKey = "om_1", "file_1"
	resourcePath := fmt.Sprintf("/open-apis/im/v1/messages/%s/resources/%s", messageID, fileKey)

	for _, tc := range []struct {
		name         string
		contentType  string
		body         string
		wantRequests int
	}{
		{"json array", "application/json; charset=utf-8", `[1,2,3]`, 1},
		{"json object", "application/json; charset=utf-8", `{"a":1}`, 1},
		{"json object with non-integer code", "application/json; charset=utf-8", `{"code":"abc"}`, 1},
		{"json number", "application/json; charset=utf-8", `42`, 1},
		{"json object with token-expired code", "application/json; charset=utf-8", `{"code":99991663,"msg":"x"}`, 1},
		{"binary", "application/octet-stream", "\x00\x01\xff\xfe binary", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resourceRequests := 0
			installFeishuTransport(t, func(r *http.Request) (int, string, string) {
				switch r.URL.Path {
				case larkcore.TenantAccessTokenInternalUrlPath:
					return http.StatusOK, "application/json", `{"code":0,"tenant_access_token":"t","expire":7200}`
				case resourcePath:
					resourceRequests++
					return http.StatusOK, tc.contentType, tc.body
				}
				return http.StatusNotFound, "text/plain", "unexpected " + r.URL.Path
			})
			payload, err := (&FeishuAdapter{}).ResolveAttachment(context.Background(), testResolveConfig(), channel.Attachment{
				Type:        channel.AttachmentFile,
				PlatformKey: fileKey,
				Metadata:    map[string]any{"message_id": messageID},
			})
			if err != nil {
				t.Fatalf("ResolveAttachment: %v", err)
			}
			defer func() { _ = payload.Reader.Close() }()
			got, err := io.ReadAll(payload.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(tc.body)) {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
			if resourceRequests != tc.wantRequests {
				t.Fatalf("resource requests = %d, want %d", resourceRequests, tc.wantRequests)
			}
		})
	}
}

func TestResolveAttachmentReturnsFeishuError(t *testing.T) {
	installFeishuTransport(t, func(r *http.Request) (int, string, string) {
		if r.URL.Path == larkcore.TenantAccessTokenInternalUrlPath {
			return http.StatusOK, "application/json", `{"code":0,"tenant_access_token":"t","expire":7200}`
		}
		return http.StatusBadRequest, "application/json; charset=utf-8", `{"code":234003,"msg":"file not found"}`
	})
	_, err := (&FeishuAdapter{}).ResolveAttachment(context.Background(), testResolveConfig(), channel.Attachment{
		Type:        channel.AttachmentFile,
		PlatformKey: "file_1",
		Metadata:    map[string]any{"message_id": "om_1"},
	})
	if err == nil {
		t.Fatal("expected error for Feishu error response")
	}
	if !strings.Contains(err.Error(), "234003") {
		t.Fatalf("error = %v, want code 234003", err)
	}
}

func testResolveConfig() channel.ChannelConfig {
	// A fresh App ID per call keeps the SDK token cache from crossing cases.
	return channel.ChannelConfig{Credentials: map[string]any{"appId": uuid.NewString(), "appSecret": "secret"}}
}

func installFeishuTransport(t *testing.T, handle func(*http.Request) (int, string, string)) {
	t.Helper()
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = discoveryTransport(func(r *http.Request) (*http.Response, error) {
		status, contentType, body := handle(r)
		header := make(http.Header)
		header.Set("Content-Type", contentType)
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
}
