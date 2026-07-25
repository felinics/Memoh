package wecom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type HTTPClientOptions struct {
	Logger                *slog.Logger
	Client                *http.Client
	Transport             *http.Transport
	Timeout               time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
}

type HTTPClient struct {
	client *http.Client
	logger *slog.Logger
}

type DownloadedFile struct {
	Data        []byte
	FileName    string
	ContentType string
}

func NewHTTPClient(opts HTTPClientOptions) *HTTPClient {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.IdleConnTimeout <= 0 {
		opts.IdleConnTimeout = 90 * time.Second
	}
	if opts.TLSHandshakeTimeout <= 0 {
		opts.TLSHandshakeTimeout = 10 * time.Second
	}
	if opts.ResponseHeaderTimeout <= 0 {
		opts.ResponseHeaderTimeout = 15 * time.Second
	}
	if opts.MaxIdleConns <= 0 {
		opts.MaxIdleConns = 100
	}
	if opts.MaxIdleConnsPerHost <= 0 {
		opts.MaxIdleConnsPerHost = 10
	}
	client := opts.Client
	if client == nil {
		transport := opts.Transport
		if transport == nil {
			transport = &http.Transport{
				MaxIdleConns:          opts.MaxIdleConns,
				MaxIdleConnsPerHost:   opts.MaxIdleConnsPerHost,
				IdleConnTimeout:       opts.IdleConnTimeout,
				TLSHandshakeTimeout:   opts.TLSHandshakeTimeout,
				ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
			}
		}
		client = &http.Client{
			Transport: transport,
			Timeout:   opts.Timeout,
		}
	}
	return &HTTPClient{
		client: client,
		logger: opts.Logger.With(slog.String("component", "wecom_http_client")),
	}
}

func (c *HTTPClient) DownloadFile(ctx context.Context, rawURL string) (DownloadedFile, error) {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return DownloadedFile{}, errors.New("download url is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return DownloadedFile{}, err
	}
	resp, err := c.client.Do(req) //nolint:gosec // G704: URL is provided by channel payload and consumed as attachment download endpoint
	if err != nil {
		return DownloadedFile{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DownloadedFile{}, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return DownloadedFile{}, err
	}
	fileName := extractFilename(resp.Header.Get("Content-Disposition"), u)
	return DownloadedFile{
		Data:        data,
		FileName:    fileName,
		ContentType: strings.TrimSpace(resp.Header.Get("Content-Type")),
	}, nil
}

func (c *HTTPClient) DownloadAndDecryptFile(ctx context.Context, rawURL string, aesKey string) (DownloadedFile, error) {
	file, err := c.DownloadFile(ctx, rawURL)
	if err != nil {
		return DownloadedFile{}, err
	}
	plain, err := DecryptFileAES256CBC(file.Data, aesKey)
	if err != nil {
		return DownloadedFile{}, err
	}
	file.Data = plain
	return file, nil
}

func extractFilename(contentDisposition, rawURL string) string {
	cd := strings.TrimSpace(contentDisposition)
	if cd != "" {
		_, params, err := mime.ParseMediaType(cd)
		if err == nil {
			if v := strings.TrimSpace(params["filename*"]); v != "" {
				if decoded := decodeRFC5987Filename(v); decoded != "" {
					return decoded
				}
				return v
			}
			if v := strings.TrimSpace(params["filename"]); v != "" {
				return v
			}
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	base := strings.TrimSpace(path.Base(parsed.Path))
	if base == "." || base == "/" {
		return ""
	}
	return base
}

func decodeRFC5987Filename(value string) string {
	parts := strings.SplitN(strings.TrimSpace(value), "''", 2)
	encoded := strings.TrimSpace(value)
	if len(parts) == 2 {
		encoded = strings.TrimSpace(parts[1])
	}
	if encoded == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(decoded)
}
