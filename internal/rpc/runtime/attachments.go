package runtime

import (
	"context"
	"crypto/md5" //nolint:gosec // compatibility digest required by the Weixin upload protocol
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

const attachmentChunkSize = 256 * 1024

func (s *Server) ResolveAttachment(ctx context.Context, req *runtimepb.ResolveAttachmentRequest) (*runtimepb.ResolveAttachmentResponse, error) {
	if s.attachments == nil {
		return nil, status.Error(codes.Unavailable, "attachment storage is unavailable")
	}
	botID := strings.TrimSpace(req.GetBotId())
	contentHash := strings.TrimSpace(req.GetContentHash())
	containerPath := strings.TrimSpace(req.GetContainerPath())
	if botID == "" || (contentHash == "") == (containerPath == "") {
		return nil, status.Error(codes.InvalidArgument, "exactly one attachment source and bot id are required")
	}

	var (
		asset media.Asset
		err   error
	)
	sourceKind := "content_hash"
	if contentHash != "" {
		if !validContentHash(contentHash) {
			return nil, status.Error(codes.InvalidArgument, "invalid attachment content hash")
		}
		asset, err = s.attachments.Resolve(ctx, botID, contentHash)
	} else {
		sourceKind = "workspace_path"
		cleanPath := filepath.Clean(containerPath)
		subpath, ok := attachment.DataSubpath(cleanPath)
		if !ok || strings.Contains(subpath, "..") {
			return nil, status.Error(codes.InvalidArgument, "workspace attachment path must be inside /data")
		}
		asset, err = s.attachments.IngestContainerFile(ctx, botID, cleanPath)
	}
	if err != nil {
		stage := "resolve_asset"
		if sourceKind == "workspace_path" {
			stage = "ingest_workspace_file"
		}
		return nil, s.attachmentFailure(ctx, "resolve", stage, sourceKind, botID, 0, err, containerPath, contentHash)
	}
	if asset.SizeBytes > media.MaxAssetBytes {
		return nil, status.Error(codes.ResourceExhausted, "attachment exceeds the maximum size")
	}

	if strings.TrimSpace(asset.RawMD5) == "" || asset.SizeBytes <= 0 {
		reader, _, err := s.attachments.Open(ctx, botID, asset.ContentHash)
		if err != nil {
			return nil, s.attachmentFailure(ctx, "resolve", "open_ingested_asset", sourceKind, botID, 0, err, containerPath, contentHash, asset.ContentHash)
		}
		md5Hash := md5.New() //nolint:gosec // compatibility digest required by the Weixin upload protocol
		limited := &io.LimitedReader{R: reader, N: media.MaxAssetBytes + 1}
		size, copyErr := io.Copy(md5Hash, limited)
		closeErr := reader.Close()
		if copyErr != nil {
			return nil, s.attachmentFailure(ctx, "resolve", "compute_raw_md5", sourceKind, botID, size, copyErr, containerPath, contentHash, asset.ContentHash)
		}
		if closeErr != nil {
			return nil, s.attachmentFailure(ctx, "resolve", "close_ingested_asset", sourceKind, botID, size, closeErr, containerPath, contentHash, asset.ContentHash)
		}
		if size == 0 {
			return nil, status.Error(codes.InvalidArgument, "attachment is empty")
		}
		if size > media.MaxAssetBytes {
			return nil, status.Error(codes.ResourceExhausted, "attachment exceeds the maximum size")
		}
		asset.RawMD5 = hex.EncodeToString(md5Hash.Sum(nil))
		asset.SizeBytes = size
	}
	return &runtimepb.ResolveAttachmentResponse{
		BotId: asset.BotID, ContentHash: asset.ContentHash, Mime: asset.Mime,
		SizeBytes: asset.SizeBytes, StorageKey: asset.StorageKey, RawMd5: asset.RawMD5,
	}, nil
}

func (s *Server) ReadAttachment(req *runtimepb.ReadAttachmentRequest, stream runtimepb.RuntimeService_ReadAttachmentServer) error {
	if s.attachments == nil {
		return status.Error(codes.Unavailable, "attachment storage is unavailable")
	}
	botID := strings.TrimSpace(req.GetBotId())
	contentHash := strings.TrimSpace(req.GetContentHash())
	if botID == "" || !validContentHash(contentHash) {
		return status.Error(codes.InvalidArgument, "bot id and valid content hash are required")
	}
	reader, _, err := s.attachments.Open(stream.Context(), botID, contentHash)
	if err != nil {
		return s.attachmentFailure(stream.Context(), "read", "open_asset", "content_hash", botID, 0, err, contentHash)
	}
	var sent int64
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			s.logAttachmentFailure(stream.Context(), "read", "close_asset", "content_hash", botID, sent, closeErr, contentHash)
		}
	}()

	buffer := make([]byte, attachmentChunkSize)
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if sent+int64(n) > media.MaxAssetBytes {
				err := status.Error(codes.ResourceExhausted, "attachment exceeds the maximum size")
				s.logAttachmentFailure(stream.Context(), "read", "enforce_size_limit", "content_hash", botID, sent, err, contentHash)
				return err
			}
			chunk := append([]byte(nil), buffer[:n]...)
			if err := stream.Send(&runtimepb.AttachmentChunk{Data: chunk}); err != nil {
				s.logAttachmentFailure(stream.Context(), "read", "send_chunk", "content_hash", botID, sent, err, contentHash)
				return err
			}
			sent += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return s.attachmentFailure(stream.Context(), "read", "read_chunk", "content_hash", botID, sent, readErr, contentHash)
		}
	}
}

func (s *Server) attachmentFailure(ctx context.Context, operation, stage, sourceKind, botID string, bytesProcessed int64, err error, redact ...string) error {
	s.logAttachmentFailure(ctx, operation, stage, sourceKind, botID, bytesProcessed, err, redact...)
	return attachmentStatus(err)
}

func (s *Server) logAttachmentFailure(ctx context.Context, operation, stage, sourceKind, botID string, bytesProcessed int64, err error, redact ...string) {
	errorText := err.Error()
	for _, value := range redact {
		if value != "" {
			errorText = strings.ReplaceAll(errorText, value, "[redacted]")
		}
	}
	s.logger.ErrorContext(ctx, "attachment operation failed",
		slog.String("operation", operation),
		slog.String("stage", stage),
		slog.String("source_kind", sourceKind),
		slog.String("bot_id", botID),
		slog.Int64("bytes_processed", bytesProcessed),
		slog.String("error", errorText),
	)
}

func validContentHash(contentHash string) bool {
	if len(contentHash) != 64 {
		return false
	}
	_, err := hex.DecodeString(contentHash)
	return err == nil
}

func attachmentStatus(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, media.ErrAssetNotFound):
		return status.Error(codes.NotFound, "attachment not found")
	case errors.Is(err, media.ErrAssetTooLarge):
		return status.Error(codes.ResourceExhausted, "attachment exceeds the maximum size")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "attachment transfer canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "attachment transfer deadline exceeded")
	default:
		return status.Error(codes.Internal, "attachment operation failed")
	}
}
