package channeltest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/media"
)

type storedAsset struct {
	asset media.Asset
	data  []byte
}

type storedContainerFile struct {
	botID string
	path  string
	data  []byte
	mime  string
	name  string
}

// MemoryAttachmentStore is a lightweight in-memory store for attachment preparation tests.
type MemoryAttachmentStore struct {
	mu             sync.Mutex
	assets         map[string]storedAsset
	storageIndex   map[string]string
	containerFiles map[string]storedContainerFile
}

func NewMemoryAttachmentStore() *MemoryAttachmentStore {
	return &MemoryAttachmentStore{
		assets:         make(map[string]storedAsset),
		storageIndex:   make(map[string]string),
		containerFiles: make(map[string]storedContainerFile),
	}
}

func (s *MemoryAttachmentStore) Stat(_ context.Context, botID, contentHash string) (media.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.assets[s.assetKey(botID, contentHash)]
	if !ok {
		return media.Asset{}, media.ErrAssetNotFound
	}
	return stored.asset, nil
}

func (s *MemoryAttachmentStore) Open(_ context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.assets[s.assetKey(botID, contentHash)]
	if !ok {
		return nil, media.Asset{}, media.ErrAssetNotFound
	}
	return io.NopCloser(bytes.NewReader(stored.data)), stored.asset, nil
}

func (s *MemoryAttachmentStore) Ingest(_ context.Context, input media.IngestInput) (media.Asset, error) {
	if strings.TrimSpace(input.BotID) == "" {
		return media.Asset{}, errors.New("bot id is required")
	}
	if input.Reader == nil {
		return media.Asset{}, errors.New("reader is required")
	}
	maxBytes := input.MaxBytes
	if maxBytes <= 0 {
		maxBytes = media.MaxAssetBytes
	}
	data, err := media.ReadAllWithLimit(input.Reader, maxBytes)
	if err != nil {
		return media.Asset{}, err
	}
	return s.ingestBytes(input.BotID, data, input.Mime, input.OriginalExt)
}

func (s *MemoryAttachmentStore) GetByStorageKey(_ context.Context, botID, storageKey string) (media.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	contentHash, ok := s.storageIndex[s.storageKey(botID, storageKey)]
	if !ok {
		return media.Asset{}, media.ErrAssetNotFound
	}
	stored, ok := s.assets[s.assetKey(botID, contentHash)]
	if !ok {
		return media.Asset{}, media.ErrAssetNotFound
	}
	return stored.asset, nil
}

func (*MemoryAttachmentStore) AccessPath(_ context.Context, asset media.Asset) string {
	return "/data/.memoh/media/" + strings.TrimSpace(asset.StorageKey)
}

func (s *MemoryAttachmentStore) IngestContainerFile(_ context.Context, botID, containerPath string) (media.Asset, error) {
	s.mu.Lock()
	containerFile, ok := s.containerFiles[s.containerKey(botID, containerPath)]
	s.mu.Unlock()
	if !ok {
		return media.Asset{}, media.ErrAssetNotFound
	}
	return s.ingestBytes(containerFile.botID, containerFile.data, containerFile.mime, filepath.Ext(containerFile.name))
}

func (s *MemoryAttachmentStore) SeedAsset(botID string, data []byte, mimeType, originalExt string) (media.Asset, error) {
	return s.ingestBytes(botID, data, mimeType, originalExt)
}

func (s *MemoryAttachmentStore) SeedContainerFile(botID, containerPath string, data []byte, mimeType, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containerFiles[s.containerKey(botID, containerPath)] = storedContainerFile{
		botID: botID,
		path:  containerPath,
		data:  append([]byte(nil), data...),
		mime:  strings.TrimSpace(mimeType),
		name:  strings.TrimSpace(name),
	}
}

func (s *MemoryAttachmentStore) ingestBytes(botID string, data []byte, mimeType, originalExt string) (media.Asset, error) {
	sum := sha256.Sum256(data)
	contentHash := hex.EncodeToString(sum[:])
	ext := strings.TrimSpace(originalExt)
	if ext == "" {
		ext = firstExtension(strings.TrimSpace(mimeType))
	}
	if ext == "" {
		ext = ".bin"
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	asset := media.Asset{
		ContentHash: contentHash,
		BotID:       botID,
		Mime:        mimeType,
		SizeBytes:   int64(len(data)),
		StorageKey:  contentHash[:2] + "/" + contentHash + ext,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[s.assetKey(botID, contentHash)] = storedAsset{
		asset: asset,
		data:  append([]byte(nil), data...),
	}
	s.storageIndex[s.storageKey(botID, asset.StorageKey)] = contentHash
	return asset, nil
}

func (*MemoryAttachmentStore) assetKey(botID, contentHash string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(contentHash)
}

func (*MemoryAttachmentStore) storageKey(botID, storageKey string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(storageKey)
}

func (*MemoryAttachmentStore) containerKey(botID, containerPath string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(containerPath)
}

func firstExtension(mimeType string) string {
	if mimeType == "" {
		return ""
	}
	exts, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(exts) == 0 {
		return ""
	}
	return exts[0]
}
