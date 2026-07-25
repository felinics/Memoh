package gateway_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
)

const dirTestChannelType = gateway.ChannelType("dir-test")

// dirMockAdapter implements Adapter and ChannelDirectoryAdapter for registry DirectoryAdapter tests.
type dirMockAdapter struct{}

func (*dirMockAdapter) Type() gateway.ChannelType { return dirTestChannelType }

func (*dirMockAdapter) Descriptor() gateway.Descriptor {
	return gateway.Descriptor{Type: dirTestChannelType, DisplayName: "DirTest"}
}

func (*dirMockAdapter) ListPeers(_ context.Context, _ gateway.ChannelConfig, _ gateway.DirectoryQuery) ([]gateway.DirectoryEntry, error) {
	return nil, nil
}

func (*dirMockAdapter) ListGroups(_ context.Context, _ gateway.ChannelConfig, _ gateway.DirectoryQuery) ([]gateway.DirectoryEntry, error) {
	return nil, nil
}

func (*dirMockAdapter) ListGroupMembers(_ context.Context, _ gateway.ChannelConfig, _ string, _ gateway.DirectoryQuery) ([]gateway.DirectoryEntry, error) {
	return nil, nil
}

func (*dirMockAdapter) ResolveEntry(_ context.Context, _ gateway.ChannelConfig, _ string, _ gateway.DirectoryEntryKind) (gateway.DirectoryEntry, error) {
	return gateway.DirectoryEntry{}, nil
}

func TestDirectoryAdapter_Unsupported(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()
	dir, ok := reg.DirectoryAdapter(testChannelType)
	if ok || dir != nil {
		t.Fatalf("DirectoryAdapter(test) = (%v, %v), want (nil, false)", dir, ok)
	}
}

func TestDirectoryAdapter_Supported(t *testing.T) {
	t.Parallel()
	reg := gateway.NewRegistry()
	reg.MustRegister(&dirMockAdapter{})
	dir, ok := reg.DirectoryAdapter(dirTestChannelType)
	if !ok || dir == nil {
		t.Fatalf("DirectoryAdapter(dir-test) = (%v, %v), want (non-nil, true)", dir, ok)
	}
}

func TestDirectoryAdapter_UnknownType(t *testing.T) {
	t.Parallel()
	reg := gateway.NewRegistry()
	dir, ok := reg.DirectoryAdapter(gateway.ChannelType("unknown"))
	if ok || dir != nil {
		t.Fatalf("DirectoryAdapter(unknown) = (%v, %v), want (nil, false)", dir, ok)
	}
}

type attachmentResolverMockAdapter struct{}

func (*attachmentResolverMockAdapter) Type() gateway.ChannelType {
	return gateway.ChannelType("attachment-test")
}

func (*attachmentResolverMockAdapter) Descriptor() gateway.Descriptor {
	return gateway.Descriptor{Type: gateway.ChannelType("attachment-test"), DisplayName: "AttachmentTest"}
}

func (*attachmentResolverMockAdapter) ResolveAttachment(_ context.Context, _ gateway.ChannelConfig, _ gateway.Attachment) (gateway.AttachmentPayload, error) {
	return gateway.AttachmentPayload{
		Reader: io.NopCloser(strings.NewReader("payload")),
		Mime:   "text/plain",
		Name:   "payload.txt",
		Size:   7,
	}, nil
}

func TestGetAttachmentResolver_Supported(t *testing.T) {
	t.Parallel()
	reg := gateway.NewRegistry()
	reg.MustRegister(&attachmentResolverMockAdapter{})
	resolver, ok := reg.GetAttachmentResolver(gateway.ChannelType("attachment-test"))
	if !ok || resolver == nil {
		t.Fatalf("GetAttachmentResolver should return resolver for supported adapter")
	}
}

func TestGetAttachmentResolver_Unsupported(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()
	resolver, ok := reg.GetAttachmentResolver(testChannelType)
	if ok || resolver != nil {
		t.Fatalf("GetAttachmentResolver(test) = (%v, %v), want (nil, false)", resolver, ok)
	}
}
