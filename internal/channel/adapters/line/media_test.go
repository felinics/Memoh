package line

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
)

func TestResolveAttachmentKeepsBlobContextAliveUntilReaderClose(t *testing.T) {
	t.Parallel()

	factory := &testBlobClientFactory{}
	adapter := NewAdapter(nil)
	adapter.client = factory

	payload, err := adapter.ResolveAttachment(context.Background(), testLineConfig(), channel.Attachment{
		Type:           channel.AttachmentImage,
		PlatformKey:    "line-message-id",
		SourcePlatform: Type.String(),
	})
	if err != nil {
		t.Fatalf("ResolveAttachment returned error: %v", err)
	}
	defer func() { _ = payload.Reader.Close() }()

	body, err := io.ReadAll(payload.Reader)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(body) != "image-bytes" {
		t.Fatalf("body = %q, want image-bytes", string(body))
	}
}

func TestResolveAttachmentRejectsOversizedContentLength(t *testing.T) {
	t.Parallel()

	factory := &testBlobClientFactory{
		response: &http.Response{
			Header:        http.Header{"Content-Type": []string{"image/png"}},
			Body:          io.NopCloser(strings.NewReader("x")),
			ContentLength: lineBlobMaxBytes + 1,
		},
	}
	adapter := NewAdapter(nil)
	adapter.client = factory

	payload, err := adapter.ResolveAttachment(context.Background(), testLineConfig(), channel.Attachment{
		Type:           channel.AttachmentImage,
		PlatformKey:    "line-message-id",
		SourcePlatform: Type.String(),
	})
	if err == nil {
		_ = payload.Reader.Close()
		t.Fatal("expected oversized content length error")
	}
	if !errors.Is(err, media.ErrAssetTooLarge) {
		t.Fatalf("expected ErrAssetTooLarge, got %v", err)
	}
}

func TestResolveAttachmentRejectsEmptyBlobBody(t *testing.T) {
	t.Parallel()

	factory := &testBlobClientFactory{response: &http.Response{ContentLength: 0}}
	adapter := NewAdapter(nil)
	adapter.client = factory

	payload, err := adapter.ResolveAttachment(context.Background(), testLineConfig(), channel.Attachment{
		Type:           channel.AttachmentImage,
		PlatformKey:    "line-message-id",
		SourcePlatform: Type.String(),
	})
	if err == nil {
		_ = payload.Reader.Close()
		t.Fatal("expected empty response error")
	}
	if !strings.Contains(err.Error(), "empty_response") {
		t.Fatalf("expected empty_response error, got %v", err)
	}
}

func TestLimitedReadCloserReturnsTooLargeWhenBodyExceedsLimit(t *testing.T) {
	t.Parallel()

	reader := &limitedReadCloser{
		body: io.NopCloser(strings.NewReader("abcdef")),
		max:  5,
	}
	defer func() { _ = reader.Close() }()

	body, err := io.ReadAll(reader)
	if !errors.Is(err, media.ErrAssetTooLarge) {
		t.Fatalf("expected ErrAssetTooLarge, got body=%q err=%v", string(body), err)
	}
	if string(body) != "abcde" {
		t.Fatalf("body = %q, want abcde", string(body))
	}
}

type testBlobClientFactory struct {
	blob     *testBlobClient
	response *http.Response
}

func (*testBlobClientFactory) NewMessagingClient(context.Context, string) (messagingClient, error) {
	return nil, nil
}

func (f *testBlobClientFactory) NewBlobClient(ctx context.Context, _ string) (blobClient, error) {
	f.blob = &testBlobClient{ctx: ctx, response: f.response}
	return f.blob, nil
}

type testBlobClient struct {
	ctx      context.Context
	response *http.Response
}

func (c *testBlobClient) GetMessageContent(string) (*http.Response, error) {
	if c.response != nil {
		return c.response, nil
	}
	const body = "image-bytes"
	return &http.Response{
		Header:        http.Header{"Content-Type": []string{"image/png"}},
		Body:          &contextAwareReadCloser{ctx: c.ctx, reader: strings.NewReader(body)},
		ContentLength: int64(len(body)),
	}, nil
}

type contextAwareReadCloser struct {
	ctx    context.Context
	reader *strings.Reader
}

func (r *contextAwareReadCloser) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (*contextAwareReadCloser) Close() error {
	return nil
}
