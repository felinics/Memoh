package channel_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

const dirTestChannelType = channel.ChannelType("dir-test")

// dirMockAdapter implements Adapter and ChannelDirectoryAdapter for registry DirectoryAdapter tests.
type dirMockAdapter struct{}

func (*dirMockAdapter) Type() channel.ChannelType { return dirTestChannelType }

func (*dirMockAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{Type: dirTestChannelType, DisplayName: "DirTest"}
}

func (*dirMockAdapter) ListPeers(_ context.Context, _ channel.ChannelConfig, _ channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	return nil, nil
}

func (*dirMockAdapter) ListGroups(_ context.Context, _ channel.ChannelConfig, _ channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	return nil, nil
}

func (*dirMockAdapter) ListGroupMembers(_ context.Context, _ channel.ChannelConfig, _ string, _ channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	return nil, nil
}

func (*dirMockAdapter) ResolveEntry(_ context.Context, _ channel.ChannelConfig, _ string, _ channel.DirectoryEntryKind) (channel.DirectoryEntry, error) {
	return channel.DirectoryEntry{}, nil
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
	reg := channel.NewRegistry()
	reg.MustRegister(&dirMockAdapter{})
	dir, ok := reg.DirectoryAdapter(dirTestChannelType)
	if !ok || dir == nil {
		t.Fatalf("DirectoryAdapter(dir-test) = (%v, %v), want (non-nil, true)", dir, ok)
	}
}

func TestDirectoryAdapter_UnknownType(t *testing.T) {
	t.Parallel()
	reg := channel.NewRegistry()
	dir, ok := reg.DirectoryAdapter(channel.ChannelType("unknown"))
	if ok || dir != nil {
		t.Fatalf("DirectoryAdapter(unknown) = (%v, %v), want (nil, false)", dir, ok)
	}
}

type attachmentResolverMockAdapter struct{}

func (*attachmentResolverMockAdapter) Type() channel.ChannelType {
	return channel.ChannelType("attachment-test")
}

func (*attachmentResolverMockAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{Type: channel.ChannelType("attachment-test"), DisplayName: "AttachmentTest"}
}

func (*attachmentResolverMockAdapter) ResolveAttachment(_ context.Context, _ channel.ChannelConfig, _ channel.Attachment) (channel.AttachmentPayload, error) {
	return channel.AttachmentPayload{
		Reader: io.NopCloser(strings.NewReader("payload")),
		Mime:   "text/plain",
		Name:   "payload.txt",
		Size:   7,
	}, nil
}

func TestGetAttachmentResolver_Supported(t *testing.T) {
	t.Parallel()
	reg := channel.NewRegistry()
	reg.MustRegister(&attachmentResolverMockAdapter{})
	resolver, ok := reg.GetAttachmentResolver(channel.ChannelType("attachment-test"))
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
