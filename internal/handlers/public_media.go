package handlers

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register PNG decoder for image.Decode
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/channel/publicmedia"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/webhooktunnel"
)

type PublicMediaHandler struct {
	logger  *slog.Logger
	media   *media.Service
	enabled bool
	signer  *publicmedia.Signer
}

func NewPublicMediaHandler(log *slog.Logger, mediaService *media.Service, signingSecret string) *PublicMediaHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PublicMediaHandler{
		logger:  log.With(slog.String("handler", "public_media")),
		media:   mediaService,
		enabled: true,
		signer:  publicmedia.NewSigner(signingSecret, publicmedia.SignedURLTTL),
	}
}

func NewConfiguredPublicMediaHandler(log *slog.Logger, cfg config.Config, mediaService *media.Service) *PublicMediaHandler {
	handler := NewPublicMediaHandler(log, mediaService, cfg.Auth.JWTSecret)
	handler.enabled = configuredPublicBaseURL(cfg) != ""
	return handler
}

func configuredPublicBaseURL(cfg config.Config) string {
	base, err := webhooktunnel.NormalizeConfiguredPublicBase(cfg.WebhookTunnel.PublicBaseURL)
	if err != nil {
		return ""
	}
	return base
}

func (h *PublicMediaHandler) Register(e *echo.Echo) {
	if h == nil || !h.enabled {
		return
	}
	e.GET("/channels/:platform/public/media/:bot_id/:content_hash/preview.jpg", h.ServePreview)
	e.HEAD("/channels/:platform/public/media/:bot_id/:content_hash/preview.jpg", h.ServePreview)
	e.GET("/channels/:platform/public/media/:bot_id/:content_hash/original/:name", h.ServeOriginal)
	e.HEAD("/channels/:platform/public/media/:bot_id/:content_hash/original/:name", h.ServeOriginal)
}

func (h *PublicMediaHandler) ServeOriginal(c echo.Context) error {
	botID, contentHash, ok := publicMediaParams(c)
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid media reference")
	}
	if !h.authorized(c) {
		return echo.NewHTTPError(http.StatusForbidden, "invalid media signature")
	}
	reader, asset, err := h.openImage(c, botID, contentHash)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	if asset.SizeBytes > publicmedia.OriginalMaxBytes {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "media is too large")
	}
	if c.Request().Method == http.MethodHead {
		setPublicMediaHeaders(c, asset.Mime, asset.SizeBytes)
		return c.NoContent(http.StatusOK)
	}
	data, err := media.ReadAllWithLimit(reader, publicmedia.OriginalMaxBytes)
	if err != nil {
		return publicMediaTooLargeHTTPError(err)
	}
	setPublicMediaHeaders(c, asset.Mime, int64(len(data)))
	return c.Blob(http.StatusOK, asset.Mime, data)
}

func (h *PublicMediaHandler) ServePreview(c echo.Context) error {
	botID, contentHash, ok := publicMediaParams(c)
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid media reference")
	}
	if !h.authorized(c) {
		return echo.NewHTTPError(http.StatusForbidden, "invalid media signature")
	}
	reader, asset, err := h.openImage(c, botID, contentHash)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	if asset.SizeBytes > publicmedia.OriginalMaxBytes {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "media is too large")
	}
	if c.Request().Method == http.MethodHead {
		setPublicMediaHeaders(c, "image/jpeg", 0)
		return c.NoContent(http.StatusOK)
	}
	data, err := media.ReadAllWithLimit(reader, publicmedia.OriginalMaxBytes)
	if err != nil {
		return publicMediaTooLargeHTTPError(err)
	}
	preview, err := encodePublicMediaPreviewJPEG(data)
	if err != nil {
		if errors.Is(err, media.ErrAssetTooLarge) {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "media is too large")
		}
		if h.logger != nil {
			h.logger.Warn("public media preview failed",
				slog.String("bot_id", botID),
				slog.String("content_hash", contentHash),
				slog.Any("error", err),
			)
		}
		return echo.NewHTTPError(http.StatusUnsupportedMediaType, "unsupported image preview")
	}
	setPublicMediaHeaders(c, "image/jpeg", int64(len(preview)))
	return c.Blob(http.StatusOK, "image/jpeg", preview)
}

func (h *PublicMediaHandler) openImage(c echo.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error) {
	if h == nil || h.media == nil {
		return nil, media.Asset{}, echo.NewHTTPError(http.StatusInternalServerError, "media service unavailable")
	}
	reader, asset, err := h.media.Open(c.Request().Context(), botID, contentHash)
	if err != nil {
		if errors.Is(err, media.ErrAssetNotFound) {
			return nil, media.Asset{}, echo.NewHTTPError(http.StatusNotFound, "media not found")
		}
		return nil, media.Asset{}, echo.NewHTTPError(http.StatusInternalServerError, "open media failed")
	}
	asset.Mime = attachment.NormalizeMime(asset.Mime)
	if asset.Mime != "image/jpeg" && asset.Mime != "image/png" {
		_ = reader.Close()
		return nil, media.Asset{}, echo.NewHTTPError(http.StatusUnsupportedMediaType, "unsupported image type")
	}
	return reader, asset, nil
}

func (h *PublicMediaHandler) authorized(c echo.Context) bool {
	if h == nil || h.signer == nil || c == nil || c.Request() == nil || c.Request().URL == nil {
		return false
	}
	return h.signer.Validate(c.Request().URL.EscapedPath(), c.Request().URL.Query(), time.Now().UTC())
}

func publicMediaParams(c echo.Context) (string, string, bool) {
	platform := strings.TrimSpace(c.Param("platform"))
	if !publicmedia.IsChannelType(platform) {
		return "", "", false
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	contentHash := strings.ToLower(strings.TrimSpace(c.Param("content_hash")))
	if !publicmedia.IsBotID(botID) {
		return "", "", false
	}
	if !publicmedia.IsContentHash(contentHash) {
		return "", "", false
	}
	return botID, contentHash, true
}

func setPublicMediaHeaders(c echo.Context, mimeType string, size int64) {
	header := c.Response().Header()
	header.Set(echo.HeaderCacheControl, "private, max-age=300")
	header.Set("X-Content-Type-Options", "nosniff")
	if size > 0 {
		header.Set(echo.HeaderContentLength, strconv.FormatInt(size, 10))
	}
	if mimeType != "" {
		header.Set(echo.HeaderContentType, mimeType)
	}
}

func publicMediaTooLargeHTTPError(err error) error {
	if errors.Is(err, media.ErrAssetTooLarge) {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "media is too large")
	}
	return echo.NewHTTPError(http.StatusBadRequest, "read media failed")
}

func encodePublicMediaPreviewJPEG(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > publicmedia.PreviewMaxDimension ||
		cfg.Height > publicmedia.PreviewMaxDimension ||
		int64(cfg.Width)*int64(cfg.Height) > publicmedia.PreviewMaxPixels {
		return nil, media.ErrAssetTooLarge
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for _, maxDim := range []int{1024, 768, 512, 360, 240} {
		resized := resizeForPreview(src, maxDim)
		for _, quality := range []int{82, 72, 62, 52} {
			var out bytes.Buffer
			if err := jpeg.Encode(&out, resized, &jpeg.Options{Quality: quality}); err != nil {
				return nil, err
			}
			if int64(out.Len()) <= publicmedia.PreviewMaxBytes {
				return out.Bytes(), nil
			}
		}
	}
	return nil, media.ErrAssetTooLarge
}

func resizeForPreview(src image.Image, maxDim int) *image.RGBA {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	dstW, dstH := width, height
	if maxDim > 0 && (width > maxDim || height > maxDim) {
		if width >= height {
			dstW = maxDim
			dstH = max(1, height*maxDim/width)
		} else {
			dstH = maxDim
			dstW = max(1, width*maxDim/height)
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		sy := bounds.Min.Y + y*height/dstH
		for x := 0; x < dstW; x++ {
			sx := bounds.Min.X + x*width/dstW
			r, g, b, a := src.At(sx, sy).RGBA()
			dst.SetRGBA(x, y, flattenWhite(r, g, b, a))
		}
	}
	return dst
}

func flattenWhite(r, g, b, a uint32) color.RGBA {
	alpha := a >> 8
	if alpha >= 255 {
		return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255} //nolint:gosec // G115: 16-bit color downsample, always 0-255.
	}
	blend := func(v uint32) uint8 {
		v8 := v >> 8
		return uint8((v8*alpha + 255*(255-alpha)) / 255) //nolint:gosec // G115: alpha blend result is always 0-255.
	}
	return color.RGBA{R: blend(r), G: blend(g), B: blend(b), A: 255}
}
