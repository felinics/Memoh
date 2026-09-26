package channel

import (
	"context"
	"crypto/md5" //nolint:gosec // compatibility digest required by the Weixin upload protocol
	"encoding/hex"
	"io"
	"strings"

	"go.uber.org/fx"

	domainchannel "github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
)

type localAttachmentStoreParams struct {
	fx.In

	Local *media.Service
}

type remoteAttachmentStoreParams struct {
	fx.In

	Local  *media.Service
	Remote *runtimeRpc.Client
}

func provideLocalChannelAttachmentStore(params localAttachmentStoreParams) *outboundAttachmentStore {
	return &outboundAttachmentStore{local: params.Local}
}

func provideRemoteChannelAttachmentStore(params remoteAttachmentStoreParams) *outboundAttachmentStore {
	return &outboundAttachmentStore{local: params.Local, remote: params.Remote}
}

type outboundAttachmentStore struct {
	local  *media.Service
	remote *runtimeRpc.Client
}

func (s *outboundAttachmentStore) Stat(ctx context.Context, botID, contentHash string) (media.Asset, error) {
	asset, err := s.local.Stat(ctx, botID, contentHash)
	if err == nil {
		return s.ensureRawMD5(ctx, asset)
	}
	if s.remote == nil {
		return media.Asset{}, err
	}
	return s.remote.ResolveAttachment(ctx, botID, contentHash, "")
}

func (s *outboundAttachmentStore) Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error) {
	asset, err := s.Stat(ctx, botID, contentHash)
	if err != nil {
		return nil, media.Asset{}, err
	}
	reader, err := s.OpenPrepared(ctx, botID, contentHash)
	if err != nil {
		return nil, media.Asset{}, err
	}
	return reader, asset, nil
}

func (s *outboundAttachmentStore) OpenPrepared(ctx context.Context, botID, contentHash string) (io.ReadCloser, error) {
	reader, _, localErr := s.local.Open(ctx, botID, contentHash)
	if localErr == nil {
		return reader, nil
	}
	if s.remote == nil {
		return nil, localErr
	}
	return s.remote.OpenAttachment(ctx, botID, contentHash)
}

func (s *outboundAttachmentStore) Ingest(ctx context.Context, input media.IngestInput) (media.Asset, error) {
	return s.local.Ingest(ctx, input)
}

func (s *outboundAttachmentStore) GetByStorageKey(ctx context.Context, botID, storageKey string) (media.Asset, error) {
	asset, err := s.local.GetByStorageKey(ctx, botID, storageKey)
	if err != nil {
		return media.Asset{}, err
	}
	return s.ensureRawMD5(ctx, asset)
}

func (s *outboundAttachmentStore) AccessPath(ctx context.Context, asset media.Asset) string {
	return s.local.AccessPath(ctx, asset)
}

func (s *outboundAttachmentStore) IngestContainerFile(ctx context.Context, botID, containerPath string) (media.Asset, error) {
	if s.remote != nil {
		return s.remote.ResolveAttachment(ctx, botID, "", containerPath)
	}
	asset, err := s.local.IngestContainerFile(ctx, botID, containerPath)
	if err != nil {
		return media.Asset{}, err
	}
	return s.ensureRawMD5(ctx, asset)
}

func (s *outboundAttachmentStore) ensureRawMD5(ctx context.Context, asset media.Asset) (media.Asset, error) {
	if strings.TrimSpace(asset.RawMD5) != "" && asset.SizeBytes > 0 {
		return asset, nil
	}
	reader, _, err := s.local.Open(ctx, asset.BotID, asset.ContentHash)
	if err != nil {
		return media.Asset{}, err
	}
	defer func() { _ = reader.Close() }()
	hasher := md5.New() //nolint:gosec // compatibility digest required by the Weixin upload protocol
	limited := &io.LimitedReader{R: reader, N: media.MaxAssetBytes + 1}
	size, err := io.Copy(hasher, limited)
	if err != nil {
		return media.Asset{}, err
	}
	if size > media.MaxAssetBytes {
		return media.Asset{}, media.ErrAssetTooLarge
	}
	asset.SizeBytes = size
	asset.RawMD5 = hex.EncodeToString(hasher.Sum(nil))
	return asset, nil
}

var (
	_ domainchannel.OutboundAttachmentStore     = (*outboundAttachmentStore)(nil)
	_ domainchannel.ContainerAttachmentIngester = (*outboundAttachmentStore)(nil)
)
