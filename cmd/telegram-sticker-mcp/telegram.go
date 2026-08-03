package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxTelegramResponseBytes = 4 << 20
	maxStickerMediaBytes     = 20 << 20
)

type telegramFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size"`
	FilePath     string `json:"file_path"`
}

type telegramSticker struct {
	FileID       string        `json:"file_id"`
	FileUniqueID string        `json:"file_unique_id"`
	Type         string        `json:"type"`
	Emoji        string        `json:"emoji"`
	IsAnimated   bool          `json:"is_animated"`
	IsVideo      bool          `json:"is_video"`
	Thumbnail    *telegramFile `json:"thumbnail"`
}

type telegramStickerSet struct {
	Name     string            `json:"name"`
	Stickers []telegramSticker `json:"stickers"`
}

type telegramMessage struct {
	MessageID int64 `json:"message_id"`
}

type telegramAPI interface {
	GetStickerSet(context.Context, string) (telegramStickerSet, error)
	DownloadStickerMedia(context.Context, telegramSticker) ([]byte, string, error)
	SendText(context.Context, string, string) (telegramMessage, error)
	SendSticker(context.Context, string, string) (telegramMessage, error)
}

type telegramClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

func newTelegramClient(token string, httpClient *http.Client) (*telegramClient, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("TELEGRAM_STICKER_MCP_BOT_TOKEN is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &telegramClient{
		token:      token,
		baseURL:    "https://api.telegram.org",
		httpClient: httpClient,
	}, nil
}

func (c *telegramClient) GetStickerSet(ctx context.Context, name string) (telegramStickerSet, error) {
	return telegramCall[telegramStickerSet](ctx, c, "getStickerSet", url.Values{
		"name": []string{strings.TrimSpace(name)},
	})
}

func (c *telegramClient) SendSticker(ctx context.Context, chatID, fileID string) (telegramMessage, error) {
	return telegramCall[telegramMessage](ctx, c, "sendSticker", url.Values{
		"chat_id": []string{strings.TrimSpace(chatID)},
		"sticker": []string{strings.TrimSpace(fileID)},
	})
}

func (c *telegramClient) SendText(ctx context.Context, chatID, text string) (telegramMessage, error) {
	return telegramCall[telegramMessage](ctx, c, "sendMessage", url.Values{
		"chat_id": []string{strings.TrimSpace(chatID)},
		"text":    []string{text},
	})
}

func (c *telegramClient) DownloadStickerMedia(
	ctx context.Context,
	sticker telegramSticker,
) ([]byte, string, error) {
	fileID := strings.TrimSpace(sticker.FileID)
	if sticker.IsAnimated || sticker.IsVideo {
		if sticker.Thumbnail == nil || strings.TrimSpace(sticker.Thumbnail.FileID) == "" {
			return nil, "", errors.New("animated or video sticker has no static thumbnail")
		}
		fileID = strings.TrimSpace(sticker.Thumbnail.FileID)
	}
	file, err := telegramCall[telegramFile](ctx, c, "getFile", url.Values{
		"file_id": []string{fileID},
	})
	if err != nil {
		return nil, "", err
	}
	filePath := strings.TrimSpace(file.FilePath)
	if filePath == "" {
		return nil, "", errors.New("telegram getFile returned an empty file_path")
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/file/bot" + c.token + "/" + strings.TrimLeft(filePath, "/")
	// #nosec G704 -- the base URL is fixed to Telegram by default and can only
	// be overridden by trusted operator configuration for compatible gateways.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build Telegram file request: %w", err)
	}
	// #nosec G704 -- see the operator-controlled endpoint boundary above.
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download Telegram sticker media: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("download Telegram sticker media failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStickerMediaBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read Telegram sticker media: %w", err)
	}
	if len(data) > maxStickerMediaBytes {
		return nil, "", errors.New("telegram sticker media exceeds 20 MiB")
	}
	return data, stickerMediaType(filePath, resp.Header.Get("Content-Type")), nil
}

func stickerMediaType(filePath, contentType string) string {
	contentType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	if contentType != "" && contentType != "application/octet-stream" {
		return contentType
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".webp":
		return "image/webp"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webm":
		return "video/webm"
	case ".tgs":
		return "application/gzip"
	default:
		if detected := mime.TypeByExtension(filepath.Ext(filePath)); detected != "" {
			return detected
		}
		return "application/octet-stream"
	}
}

func telegramCall[T any](ctx context.Context, client *telegramClient, method string, form url.Values) (T, error) {
	var zero T
	endpoint := strings.TrimRight(client.baseURL, "/") + "/bot" + client.token + "/" + method
	// #nosec G704 -- the base URL is fixed to Telegram by default and can only
	// be overridden by trusted operator configuration for compatible gateways.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return zero, fmt.Errorf("build Telegram %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// #nosec G704 -- see the operator-controlled endpoint boundary above.
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return zero, fmt.Errorf("call Telegram %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTelegramResponseBytes+1))
	if err != nil {
		return zero, fmt.Errorf("read Telegram %s response: %w", method, err)
	}
	if len(body) > maxTelegramResponseBytes {
		return zero, fmt.Errorf("telegram %s response is too large", method)
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return zero, fmt.Errorf("decode Telegram %s response: %w", method, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !envelope.OK {
		description := strings.TrimSpace(envelope.Description)
		if description == "" {
			description = http.StatusText(resp.StatusCode)
		}
		return zero, fmt.Errorf("telegram %s failed (%d): %s", method, envelope.ErrorCode, description)
	}

	var result T
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return zero, fmt.Errorf("decode Telegram %s result: %w", method, err)
	}
	return result, nil
}

type stickerService struct {
	api             telegramAPI
	catalog         *stickerCatalog
	setName         string
	sets            []*stickerService
	setMetadataKey  string
	setMetadataLoad sync.Mutex

	mu        sync.RWMutex
	cachedSet telegramStickerSet

	warmMu  sync.Mutex
	warming bool
}

type stickerServiceProvider struct {
	fallbackSet   string
	fallbackToken string
	apiBase       string
	httpClient    *http.Client
	catalog       *stickerCatalog

	mu       sync.Mutex
	services map[[sha256.Size]byte]*stickerService
}

func newStickerServiceProvider(
	setName, fallbackToken, apiBase string,
	httpClient *http.Client,
	catalog *stickerCatalog,
) (*stickerServiceProvider, error) {
	setName = strings.TrimSpace(setName)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	if catalog == nil {
		return nil, errors.New("sticker catalog is required")
	}
	return &stickerServiceProvider{
		fallbackSet:   setName,
		fallbackToken: strings.TrimSpace(fallbackToken),
		apiBase:       strings.TrimSpace(apiBase),
		httpClient:    httpClient,
		catalog:       catalog,
		services:      make(map[[sha256.Size]byte]*stickerService),
	}, nil
}

func (p *stickerServiceProvider) Resolve(req *mcp.CallToolRequest) (*stickerService, error) {
	var header http.Header
	if req != nil && req.Extra != nil {
		header = req.Extra.Header
	}
	return p.ResolveHeader(header)
}

func (p *stickerServiceProvider) ResolveHeader(header http.Header) (*stickerService, error) {
	token := strings.TrimSpace(header.Get(telegramBotHeader))
	if token == "" {
		token = p.fallbackToken
	}
	if token == "" {
		return nil, fmt.Errorf(
			"%s request header is required for HTTP transport; TELEGRAM_STICKER_MCP_BOT_TOKEN is the stdio fallback",
			telegramBotHeader,
		)
	}
	rawSetNames := strings.TrimSpace(header.Get(telegramStickerSetHeader))
	if rawSetNames == "" {
		rawSetNames = p.fallbackSet
	}
	setNames := parseStickerSetNames(rawSetNames)
	if len(setNames) == 0 {
		return nil, fmt.Errorf(
			"%s request header is required when TELEGRAM_STICKER_MCP_SET_NAME has no default",
			telegramStickerSetHeader,
		)
	}
	setListKey := strings.Join(setNames, ",")
	key := sha256.Sum256([]byte(token + "\x00" + setListKey))

	p.mu.Lock()
	defer p.mu.Unlock()
	if service := p.services[key]; service != nil {
		return service, nil
	}
	client, err := newTelegramClient(token, p.httpClient)
	if err != nil {
		return nil, err
	}
	if p.apiBase != "" {
		client.baseURL = p.apiBase
	}
	sets := make([]*stickerService, 0, len(setNames))
	for _, setName := range setNames {
		setKey := sha256.Sum256([]byte(token + "\x00" + setName))
		setService, serviceErr := newStickerServiceWithMetadataKey(
			client,
			setName,
			hex.EncodeToString(setKey[:]),
			p.catalog,
		)
		if serviceErr != nil {
			return nil, serviceErr
		}
		sets = append(sets, setService)
	}
	service := newStickerCollectionService(sets)
	p.services[key] = service
	return service, nil
}

func parseStickerSetNames(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	})
	seen := make(map[string]struct{}, len(parts))
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		identity := strings.ToLower(name)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names
}

func newStickerCollectionService(sets []*stickerService) *stickerService {
	names := make([]string, 0, len(sets))
	for _, set := range sets {
		if set != nil {
			names = append(names, set.setName)
		}
	}
	return &stickerService{setName: strings.Join(names, ", "), sets: sets}
}

func stickerSetAlias(setName string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(setName))))
	return strings.ToUpper(hex.EncodeToString(sum[:8]))
}

func collectionStickerID(setName, localID string) string {
	return stickerSetAlias(setName) + "-" + normalizeStickerID(localID)
}

func (s *stickerService) collectionSticker(stickerID string) (*stickerService, string, error) {
	stickerID = normalizeStickerID(stickerID)
	var matched *stickerService
	matchedLocalID := ""
	for _, set := range s.sets {
		prefix := stickerSetAlias(set.setName) + "-"
		if strings.HasPrefix(stickerID, prefix) {
			localID := strings.TrimPrefix(stickerID, prefix)
			if localID != "" {
				if matched != nil {
					return nil, "", fmt.Errorf("sticker_id %q has an ambiguous Sticker Set prefix", stickerID)
				}
				matched = set
				matchedLocalID = localID
			}
		}
	}
	if matched != nil {
		return matched, matchedLocalID, nil
	}
	return nil, "", fmt.Errorf("sticker_id %q was not found in configured Sticker Sets", stickerID)
}

func newStickerService(api telegramAPI, setName string, catalog *stickerCatalog) (*stickerService, error) {
	key := sha256.Sum256([]byte("standalone\x00" + strings.TrimSpace(setName)))
	return newStickerServiceWithMetadataKey(
		api,
		setName,
		hex.EncodeToString(key[:]),
		catalog,
	)
}

func newStickerServiceWithMetadataKey(
	api telegramAPI,
	setName string,
	setMetadataKey string,
	catalog *stickerCatalog,
) (*stickerService, error) {
	setName = strings.TrimSpace(setName)
	setMetadataKey = strings.TrimSpace(setMetadataKey)
	if api == nil {
		return nil, errors.New("telegram API client is required")
	}
	if setName == "" {
		return nil, errors.New("TELEGRAM_STICKER_MCP_SET_NAME is required")
	}
	if catalog == nil {
		return nil, errors.New("sticker catalog is required")
	}
	if setMetadataKey == "" {
		return nil, errors.New("sticker set metadata cache key is required")
	}
	return &stickerService{
		api:            api,
		catalog:        catalog,
		setName:        setName,
		setMetadataKey: setMetadataKey,
	}, nil
}

type sendStickerInput struct {
	ChatID    string `json:"chat_id" jsonschema:"Telegram target from the current message metadata"`
	Text      string `json:"text,omitempty" jsonschema:"Optional visible text to send before the sticker"`
	StickerID string `json:"sticker_id,omitempty" jsonschema:"Exact content-stable sticker ID from the tool schema"`
}

type sendStickerOutput struct {
	OK bool `json:"ok"`
}

type describedStickerSet struct {
	Name       string
	Stickers   []describedSticker
	TotalCount int
	Loaded     bool
	Sets       []describedStickerSet
}

func (s *stickerService) SendByID(ctx context.Context, input sendStickerInput) (sendStickerOutput, error) {
	chatID := strings.TrimSpace(input.ChatID)
	text := strings.TrimSpace(input.Text)
	stickerID := strings.ToUpper(strings.TrimSpace(input.StickerID))
	if chatID == "" {
		return sendStickerOutput{}, errors.New("chat_id is required")
	}
	if text == "" && stickerID == "" {
		return sendStickerOutput{}, errors.New("text or sticker_id is required")
	}
	if len(s.sets) > 0 {
		if stickerID == "" {
			input.Text = text
			return s.sets[0].SendByID(ctx, input)
		}
		set, localID, err := s.collectionSticker(stickerID)
		if err != nil {
			return sendStickerOutput{}, err
		}
		input.Text = text
		input.StickerID = localID
		return set.SendByID(ctx, input)
	}

	var stickerFileID string
	if stickerID != "" {
		_, sticker, err := s.stickerByID(ctx, stickerID)
		if err != nil {
			return sendStickerOutput{}, err
		}
		stickerFileID = sticker.FileID
	}

	if text != "" {
		if _, err := s.api.SendText(ctx, chatID, text); err != nil {
			return sendStickerOutput{}, err
		}
	}
	if stickerFileID != "" {
		if _, err := s.api.SendSticker(ctx, chatID, stickerFileID); err != nil {
			return sendStickerOutput{}, err
		}
	}
	return sendStickerOutput{OK: true}, nil
}

func (s *stickerService) DescribedSet(ctx context.Context) (describedStickerSet, error) {
	if len(s.sets) > 0 {
		return s.aggregateDescribedSets(ctx, false)
	}
	set, err := s.getStickerSet(ctx)
	if err != nil {
		return describedStickerSet{}, err
	}
	stickers, err := s.catalog.DescribeSet(ctx, s.api, set)
	if err != nil {
		return describedStickerSet{}, err
	}
	return describedStickerSet{
		Name:       set.Name,
		Stickers:   stickers,
		TotalCount: len(set.Stickers),
		Loaded:     true,
	}, nil
}

func (s *stickerService) CachedDescribedSet(ctx context.Context) (describedStickerSet, error) {
	if len(s.sets) > 0 {
		return s.aggregateDescribedSets(ctx, true)
	}
	s.mu.RLock()
	set := s.cachedSet
	s.mu.RUnlock()
	if len(set.Stickers) == 0 {
		cached, found, err := s.catalog.cache.GetStickerSet(ctx, s.setMetadataKey)
		if err != nil {
			return describedStickerSet{}, err
		}
		if found {
			set = cached
			s.storeStickerSetInMemory(set)
		}
	}
	if len(set.Stickers) == 0 {
		return describedStickerSet{Name: s.setName}, nil
	}
	profile, _, err := s.descriptionProfile(ctx)
	if err != nil {
		return describedStickerSet{}, err
	}
	stickers := make([]describedSticker, 0, len(set.Stickers))
	for _, sticker := range set.Stickers {
		if strings.TrimSpace(sticker.FileID) == "" || strings.TrimSpace(sticker.FileUniqueID) == "" {
			continue
		}
		entry, found, err := s.catalog.cache.Get(
			ctx,
			sticker.FileUniqueID,
			profile.Model,
			profile.PromptVersion,
		)
		if err != nil {
			return describedStickerSet{}, err
		}
		description := ""
		status := descriptionStatusPending
		if found {
			status = strings.ToLower(strings.TrimSpace(entry.Status))
		}
		if found && entry.Status == descriptionStatusReady {
			description = strings.TrimSpace(entry.Description)
		}
		stickers = append(stickers, describedSticker{
			ID:           candidateID(sticker.FileUniqueID),
			FileID:       sticker.FileID,
			FileUniqueID: sticker.FileUniqueID,
			Emoji:        strings.TrimSpace(sticker.Emoji),
			Description:  description,
			Status:       status,
		})
	}
	return describedStickerSet{
		Name:       set.Name,
		Stickers:   stickers,
		TotalCount: len(set.Stickers),
		Loaded:     true,
	}, nil
}

func (s *stickerService) aggregateDescribedSets(ctx context.Context, cached bool) (describedStickerSet, error) {
	result := describedStickerSet{Name: s.setName, Sets: make([]describedStickerSet, 0, len(s.sets))}
	for _, set := range s.sets {
		var group describedStickerSet
		var err error
		if cached {
			group, err = set.CachedDescribedSet(ctx)
		} else {
			group, err = set.DescribedSet(ctx)
		}
		if err != nil {
			return describedStickerSet{}, err
		}
		for index := range group.Stickers {
			group.Stickers[index].ID = collectionStickerID(set.setName, group.Stickers[index].ID)
		}
		result.TotalCount += group.TotalCount
		result.Stickers = append(result.Stickers, group.Stickers...)
		result.Sets = append(result.Sets, group)
	}
	sort.Slice(result.Stickers, func(i, j int) bool { return result.Stickers[i].ID < result.Stickers[j].ID })
	result.Loaded = len(result.Stickers) > 0
	return result, nil
}

func (s *stickerService) WarmDescriptions(ctx context.Context) {
	if len(s.sets) > 0 {
		for _, set := range s.sets {
			set.WarmDescriptions(ctx)
		}
		return
	}
	if !s.canWarmDescriptions(ctx) {
		return
	}
	s.warmMu.Lock()
	if s.warming {
		s.warmMu.Unlock()
		return
	}
	s.warming = true
	s.warmMu.Unlock()

	backgroundCtx := context.WithoutCancel(ctx)
	go func(parent context.Context) {
		defer func() {
			s.warmMu.Lock()
			s.warming = false
			s.warmMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
		defer cancel()
		if _, err := s.DescribedSet(ctx); err != nil {
			// #nosec G706 -- %q escapes control characters in the remote set name.
			log.Printf("background parsing for sticker set %q failed: %v", s.setName, err)
			return
		}
		// #nosec G706 -- %q escapes control characters in the remote set name.
		log.Printf("background parsing for sticker set %q completed", s.setName)
	}(backgroundCtx)
}

func (s *stickerService) descriptionProfile(ctx context.Context) (stickerDescriptionProfile, bool, error) {
	profile, found, err := s.catalog.cache.GetProfile(ctx, s.setMetadataKey)
	if err != nil {
		return stickerDescriptionProfile{}, false, err
	}
	if found {
		return profile, true, nil
	}
	return stickerDescriptionProfile{
		Model: s.catalog.describer.Model(), PromptVersion: s.catalog.describer.PromptVersion(),
	}, false, nil
}

func (s *stickerService) canWarmDescriptions(ctx context.Context) bool {
	if !s.catalog.canDescribe() {
		return false
	}
	profile, found, err := s.descriptionProfile(ctx)
	if err != nil {
		return false
	}
	return !found || (profile.Model == s.catalog.describer.Model() &&
		profile.PromptVersion == s.catalog.describer.PromptVersion())
}

func (s *stickerService) getStickerSet(ctx context.Context) (telegramStickerSet, error) {
	if len(s.sets) > 0 {
		return telegramStickerSet{}, errors.New("a Sticker Set must be selected from the configured collection")
	}
	if cached, found := s.stickerSetInMemory(); found {
		return cached, nil
	}

	// Keep slow SQLite and Telegram work out of mu so CachedDescribedSet can
	// remain non-blocking while the first catalog load is in progress.
	s.setMetadataLoad.Lock()
	defer s.setMetadataLoad.Unlock()
	if cached, found := s.stickerSetInMemory(); found {
		return cached, nil
	}
	cached, found, err := s.catalog.cache.GetStickerSet(ctx, s.setMetadataKey)
	if err != nil {
		return telegramStickerSet{}, err
	}
	if found {
		if err := validateStickerCandidateIDs(cached); err != nil {
			return telegramStickerSet{}, err
		}
		s.storeStickerSetInMemory(cached)
		return cached, nil
	}
	return s.fetchAndCacheStickerSet(ctx)
}

// RefreshStickerSet is the only path that replaces cached Sticker Set
// metadata. Normal reads have no TTL and remain stable across process restarts.
func (s *stickerService) RefreshStickerSet(ctx context.Context) (telegramStickerSet, error) {
	if len(s.sets) > 0 {
		combined := telegramStickerSet{Name: s.setName}
		for _, set := range s.sets {
			refreshed, err := set.RefreshStickerSet(ctx)
			if err != nil {
				return telegramStickerSet{}, err
			}
			combined.Stickers = append(combined.Stickers, refreshed.Stickers...)
		}
		return combined, nil
	}
	s.setMetadataLoad.Lock()
	defer s.setMetadataLoad.Unlock()
	return s.fetchAndCacheStickerSet(ctx)
}

func (s *stickerService) fetchAndCacheStickerSet(ctx context.Context) (telegramStickerSet, error) {
	set, err := s.api.GetStickerSet(ctx, s.setName)
	if err != nil {
		return telegramStickerSet{}, err
	}
	if len(set.Stickers) == 0 {
		return telegramStickerSet{}, fmt.Errorf("telegram sticker set %q is empty", s.setName)
	}
	if err := validateStickerCandidateIDs(set); err != nil {
		return telegramStickerSet{}, err
	}
	if err := s.catalog.cache.PutStickerSet(ctx, s.setMetadataKey, set); err != nil {
		return telegramStickerSet{}, err
	}
	s.storeStickerSetInMemory(set)
	return set, nil
}

func validateStickerCandidateIDs(set telegramStickerSet) error {
	seen := make(map[string]string, len(set.Stickers))
	for _, sticker := range set.Stickers {
		uniqueID := strings.TrimSpace(sticker.FileUniqueID)
		if uniqueID == "" {
			continue
		}
		id := candidateID(uniqueID)
		if previous, exists := seen[id]; exists && previous != uniqueID {
			return fmt.Errorf("sticker set %q contains an ambiguous stable ID %q", set.Name, id)
		}
		seen[id] = uniqueID
	}
	return nil
}

func (s *stickerService) stickerSetInMemory() (telegramStickerSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cachedSet, len(s.cachedSet.Stickers) > 0
}

func (s *stickerService) storeStickerSetInMemory(set telegramStickerSet) {
	s.mu.Lock()
	s.cachedSet = set
	s.mu.Unlock()
}
