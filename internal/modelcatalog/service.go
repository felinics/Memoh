package modelcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
)

const (
	DefaultURL      = "https://models.dev/api.json"
	maxCatalogBytes = 32 << 20
	cacheTTL        = 30 * time.Minute
	retryInterval   = time.Minute
)

var ErrUnavailable = errors.New("model catalog unavailable")

type Options struct {
	URL       string
	CachePath string
	// Now makes cache expiry deterministic in tests without wall-clock waits.
	Now func() time.Time
}

type Info struct {
	Source    string    `json:"source"`
	Revision  string    `json:"revision"`
	CheckedAt time.Time `json:"checked_at"`
	Stale     bool      `json:"stale"`
}
type Response struct {
	Info
	Models []Model `json:"models"`
}
type snapshot struct {
	data      catalog
	body      []byte
	etag      string
	revision  string
	checkedAt time.Time
}
type diskSnapshot struct {
	Version   int             `json:"version"`
	URL       string          `json:"url"`
	ETag      string          `json:"etag,omitempty"`
	CheckedAt time.Time       `json:"checked_at"`
	Body      json.RawMessage `json:"body"`
}
type Service struct {
	options    Options
	logger     *slog.Logger
	httpClient *http.Client
	mu         sync.Mutex
	current    *snapshot
	retryAt    time.Time
	lastErr    error
	diskOnce   sync.Once
	flight     singleflight.Group
}

func New(logger *slog.Logger, options Options) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if options.URL == "" {
		options.URL = DefaultURL
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Service{options: options, logger: logger.With(slog.String("service", "model_catalog")), httpClient: &http.Client{}}
}

// Models returns newly allocated normalized configs; callers cannot mutate the
// shared raw snapshot. A source ID never becomes a network address.
func (s *Service) Models(ctx context.Context, source, protocol string) (Response, error) {
	if !supportedProtocol(protocol) {
		return Response{}, ErrProtocol
	}
	value, stale, err := s.load(ctx)
	if err != nil {
		return Response{}, err
	}
	entries, err := convert(value.data, source, protocol)
	if err != nil {
		return Response{}, err
	}
	return Response{Info: Info{Source: source, Revision: value.revision, CheckedAt: value.checkedAt, Stale: stale}, Models: entries}, nil
}

func (s *Service) load(ctx context.Context) (*snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.diskOnce.Do(func() { s.loadDisk(ctx) })
	s.mu.Lock()
	current, retryAt, lastErr := s.current, s.retryAt, s.lastErr
	s.mu.Unlock()
	now := s.options.Now()
	if current != nil && now.Sub(current.checkedAt) < cacheTTL {
		return current, false, nil
	}
	if now.Before(retryAt) {
		if current != nil {
			return current, true, nil
		}
		return nil, false, fmt.Errorf("%w: %w", ErrUnavailable, lastErr)
	}
	result := s.flight.DoChan("refresh", func() (any, error) {
		// Shared, bounded work does not inherit one browser's request cancellation.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		s.mu.Lock()
		previous := s.current
		fresh := previous != nil && s.options.Now().Sub(previous.checkedAt) < cacheTTL
		s.mu.Unlock()
		if fresh {
			return previous, nil
		}
		next, err := s.fetch(fetchCtx, previous)
		s.mu.Lock()
		if err == nil {
			s.current = next
			s.lastErr = nil
			s.retryAt = time.Time{}
		} else {
			s.lastErr = err
			s.retryAt = s.options.Now().Add(retryInterval)
		}
		s.mu.Unlock()
		if err != nil {
			if previous != nil {
				result := errlog.Event(fetchCtx, "model_catalog.refresh", errs.WrapDependency(err, "refresh public model catalog"), errlog.Options{})
				s.logger.LogAttrs(fetchCtx, result.Level, "model catalog refresh failed; serving cached metadata", result.Attrs()...)
			}
			return nil, err
		}
		s.saveDisk(fetchCtx, next)
		return next, nil
	})
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case done := <-result:
		if done.Err == nil {
			return done.Val.(*snapshot), false, nil
		}
		s.mu.Lock()
		fallback := s.current
		s.mu.Unlock()
		if fallback != nil {
			return fallback, true, nil
		}
		return nil, false, fmt.Errorf("%w: %w", ErrUnavailable, done.Err)
	}
}

func (s *Service) fetch(ctx context.Context, previous *snapshot) (*snapshot, error) {
	endpoint, err := url.Parse(s.options.URL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.Fragment != "" {
		return nil, errors.New("catalog URL must be HTTP(S) without credentials or a fragment")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.options.URL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if previous != nil && previous.etag != "" {
		request.Header.Set("If-None-Match", previous.etag)
	}
	response, err := s.httpClient.Do(request) //nolint:gosec // G704: the URL is operator configuration, never an API request parameter.
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified && previous != nil {
		next := *previous
		next.checkedAt = s.options.Now()
		return &next, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maxCatalogBytes {
		return nil, errors.New("catalog exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCatalogBytes {
		return nil, errors.New("catalog exceeds size limit")
	}
	data, err := decode(body)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return nil, err
	}
	body = compact.Bytes()
	sum := sha256.Sum256(body)
	return &snapshot{data: data, body: body, etag: response.Header.Get("ETag"), revision: hex.EncodeToString(sum[:]), checkedAt: s.options.Now()}, nil
}

func (s *Service) loadDisk(ctx context.Context) {
	if s.options.CachePath == "" {
		return
	}
	file, err := os.Open(s.options.CachePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.cacheWarning(ctx, err)
		}
		return
	}
	defer func() { _ = file.Close() }()
	var disk diskSnapshot
	err = json.NewDecoder(io.LimitReader(file, maxCatalogBytes+4096)).Decode(&disk)
	if err != nil || disk.Version != 1 || disk.URL != s.options.URL || disk.CheckedAt.IsZero() || disk.CheckedAt.After(s.options.Now().Add(time.Minute)) {
		s.cacheWarning(ctx, errors.New("invalid model catalog cache metadata"))
		return
	}
	data, err := decode(disk.Body)
	if err != nil {
		s.cacheWarning(ctx, err)
		return
	}
	sum := sha256.Sum256(disk.Body)
	s.current = &snapshot{data: data, body: disk.Body, etag: disk.ETag, checkedAt: disk.CheckedAt, revision: hex.EncodeToString(sum[:])}
}

func (s *Service) saveDisk(ctx context.Context, value *snapshot) {
	if s.options.CachePath == "" {
		return
	}
	if err := writeSnapshot(s.options.CachePath, diskSnapshot{Version: 1, URL: s.options.URL, ETag: value.etag, CheckedAt: value.checkedAt, Body: value.body}); err != nil {
		s.cacheWarning(ctx, err)
	}
}

func (s *Service) cacheWarning(ctx context.Context, err error) {
	result := errlog.Event(ctx, "model_catalog.cache", errs.Wrap(err, "read or write model catalog cache"), errlog.Options{})
	s.logger.LogAttrs(ctx, result.Level, "model catalog cache unavailable", result.Attrs()...)
}

func writeSnapshot(path string, value diskSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".model-catalog-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }() //nolint:gosec // G703: remove only the file CreateTemp made under the configured cache directory.
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path) //nolint:gosec // G703: both paths belong to the operator-configured cache directory.
}
