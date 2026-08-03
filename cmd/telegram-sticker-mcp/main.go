package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName               = "telegram-sticker-mcp"
	serverVersion            = "0.7.0"
	sendToolName             = "send_telegram_sticker"
	sendToolDescription      = "Internal Telegram sticker delivery backend. The Memoh agent merges its stable sticker catalog into the first-party send tool."
	stickerGuideBase         = "在当前会话是 Telegram 时，通常每条对外信息都应至少配一张贴合语境的贴纸，并在同一次 `send` 调用中同时设置文本与 `sticker_id`；只有完整目录里确实没有合适候选时才省略。请根据视觉描述选择，不要只看原始 emoji，也不要重复发送含义相同的贴纸。用户明确要求 Unicode emoji 时不要用贴纸代替。"
	telegramBotHeader        = "X-Telegram-Bot-Token"
	telegramStickerSetHeader = "X-Telegram-Sticker-Set"
	stickerCatalogSchemaKey  = "x-memoh-sticker-catalog"
)

type config struct {
	BotToken    string
	SetName     string
	Transport   string
	Addr        string
	AuthToken   string
	APIBase     string
	VisionModel string
	CachePath   string
}

func main() {
	if err := runMain(); err != nil {
		log.Fatal(err)
	}
}

func runMain() error {
	envFile := flag.String("env-file", ".env", "optional dotenv file")
	flag.Parse()

	if err := loadDotenv(*envFile); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cache, err := openStickerDescriptionCache(cfg.CachePath)
	if err != nil {
		return err
	}
	defer func() { _ = cache.Close() }()
	// Sticker recognition is intentionally not performed in this process.
	// Keeping the legacy model ID here lets existing SQLite descriptions remain
	// readable until the Web-selected Memoh profile replaces them.
	describer := newCacheOnlyStickerDescriber(cfg.VisionModel)
	catalog, err := newStickerCatalog(cache, describer)
	if err != nil {
		return err
	}
	provider, err := newStickerServiceProvider(cfg.SetName, cfg.BotToken, cfg.APIBase, nil, catalog)
	if err != nil {
		return err
	}

	switch cfg.Transport {
	case "stdio":
		service, err := provider.Resolve(nil)
		if err != nil {
			return err
		}
		catalog, err := service.CachedDescribedSet(context.Background())
		if err != nil {
			return fmt.Errorf("load cached Sticker Set for MCP schema: %w", err)
		}
		server := newMCPServer(provider.Resolve, catalog, "")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			return err
		}
	case "http":
		if err := runHTTP(provider, cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	default:
		return fmt.Errorf("unsupported TELEGRAM_STICKER_MCP_TRANSPORT %q (want stdio or http)", cfg.Transport)
	}
	return nil
}

type stickerServiceResolver func(*mcp.CallToolRequest) (*stickerService, error)

func newMCPServer(resolve stickerServiceResolver, catalog describedStickerSet, catalogError string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        sendToolName,
		Description: sendToolDescription,
		InputSchema: stickerSendInputSchema(catalog, catalogError),
		Annotations: &mcp.ToolAnnotations{
			Title: "Send a visually selected Telegram sticker",
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input sendStickerInput) (*mcp.CallToolResult, sendStickerOutput, error) {
		service, err := resolve(req)
		if err != nil {
			return nil, sendStickerOutput{}, err
		}
		output, err := service.SendByID(ctx, input)
		return nil, output, err
	})
	return server
}

func stickerSendInputSchema(output describedStickerSet, catalogError string) map[string]any {
	stickers := append([]describedSticker(nil), output.Stickers...)
	sort.Slice(stickers, func(i, j int) bool { return stickers[i].ID < stickers[j].ID })
	ids := make([]string, 0, len(stickers))
	for _, sticker := range stickers {
		ids = append(ids, sticker.ID)
	}
	guide := formatStickerGuide(describedStickerSet{
		Name:       output.Name,
		Stickers:   stickers,
		TotalCount: output.TotalCount,
		Loaded:     output.Loaded,
	})
	if strings.TrimSpace(catalogError) != "" {
		guide = strings.TrimSpace(guide + "\n\n目录暂不可用。")
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Server-injected Telegram target.",
			},
			"sticker_id": map[string]any{
				"type":                  "string",
				"enum":                  ids,
				"description":           guide,
				stickerCatalogSchemaKey: structuredStickerCatalog(stickers),
			},
		},
		"required": []string{"chat_id", "sticker_id"},
	}
}

func structuredStickerCatalog(stickers []describedSticker) []any {
	entries := make([]any, 0, len(stickers))
	for _, sticker := range stickers {
		description := strings.TrimSpace(sticker.Description)
		status := strings.ToLower(strings.TrimSpace(sticker.Status))
		if description != "" {
			status = descriptionStatusReady
		}
		switch status {
		case descriptionStatusReady, descriptionStatusFailed:
		default:
			status = descriptionStatusPending
		}
		entries = append(entries, map[string]any{
			"id":          strings.ToUpper(strings.TrimSpace(sticker.ID)),
			"description": description,
			"emoji":       strings.TrimSpace(sticker.Emoji),
			"status":      status,
		})
	}
	return entries
}

func stickerGuide(ctx context.Context, service *stickerService) (string, error) {
	output, err := service.DescribedSet(ctx)
	if err != nil {
		return "", err
	}
	return formatStickerGuide(output), nil
}

func cachedStickerGuide(ctx context.Context, service *stickerService) (string, error) {
	output, err := service.CachedDescribedSet(ctx)
	if err != nil {
		return "", err
	}
	service.WarmDescriptions(ctx)
	return formatStickerGuide(output), nil
}

func formatStickerGuide(output describedStickerSet) string {
	var builder strings.Builder
	builder.WriteString(stickerGuideBase)
	if len(output.Sets) > 0 {
		readyCount := 0
		for _, sticker := range output.Stickers {
			if strings.TrimSpace(sticker.Description) != "" {
				readyCount++
			}
		}
		fmt.Fprintf(
			&builder,
			"\n\n当前已配置 %d 个 Sticker Set；模型看到的是一个合并目录，共 %d 张，其中 %d 张已有视觉描述。稳定 ID 不会因列表顺序变化而改变。",
			len(output.Sets), output.TotalCount, readyCount,
		)
		for _, set := range output.Sets {
			fmt.Fprintf(&builder, "\n\nSticker Set：`%s`（%d 张）\n", set.Name, set.TotalCount)
			if !set.Loaded {
				builder.WriteString("贴纸包元数据尚未载入。")
				continue
			}
			appendStickerGuideEntries(&builder, set.Stickers)
		}
		return strings.TrimSpace(builder.String())
	}
	if !output.Loaded {
		fmt.Fprintf(
			&builder,
			"\n\n当前 Sticker Set：`%s`\n贴纸包正在后台解析。本次会话暂时没有可用候选，请不要调用工具；稍后重新连接即可读取缓存结果。",
			output.Name,
		)
		return strings.TrimSpace(builder.String())
	}
	readyCount := 0
	for _, sticker := range output.Stickers {
		if strings.TrimSpace(sticker.Description) != "" {
			readyCount++
		}
	}
	fmt.Fprintf(
		&builder,
		"\n\n当前 Sticker Set：`%s`\n完整目录共 %d 张，其中 %d 张已有视觉描述。候选按稳定 ID 排列；标为“待识别”的条目只在没有更合适的已识别候选时使用：\n",
		output.Name,
		output.TotalCount,
		readyCount,
	)
	appendStickerGuideEntries(&builder, output.Stickers)
	return strings.TrimSpace(builder.String())
}

func appendStickerGuideEntries(builder *strings.Builder, stickers []describedSticker) {
	for _, sticker := range stickers {
		description := strings.TrimSpace(sticker.Description)
		if description == "" {
			description = "待识别"
		}
		if sticker.Emoji == "" {
			fmt.Fprintf(builder, "- %s：%s\n", sticker.ID, description)
			continue
		}
		fmt.Fprintf(
			builder,
			"- %s：%s（原始 emoji：%s，仅供参考）\n",
			sticker.ID,
			description,
			sticker.Emoji,
		)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		BotToken:    strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_BOT_TOKEN")),
		SetName:     strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_SET_NAME")),
		Transport:   strings.ToLower(strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_TRANSPORT"))),
		Addr:        strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_ADDR")),
		AuthToken:   strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_AUTH_TOKEN")),
		APIBase:     strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_API_BASE_URL")),
		VisionModel: strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_VISION_MODEL")),
		CachePath:   strings.TrimSpace(os.Getenv("TELEGRAM_STICKER_MCP_CACHE_PATH")),
	}
	if cfg.Transport == "" {
		cfg.Transport = "stdio"
	}
	if cfg.CachePath == "" {
		cfg.CachePath = "telegram-sticker-cache.sqlite"
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8091"
	}
	switch cfg.Transport {
	case "stdio":
		if cfg.BotToken == "" {
			return config{}, errors.New("TELEGRAM_STICKER_MCP_BOT_TOKEN is required")
		}
		if cfg.SetName == "" {
			return config{}, errors.New("TELEGRAM_STICKER_MCP_SET_NAME is required")
		}
	case "http":
		if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
			return config{}, fmt.Errorf("invalid TELEGRAM_STICKER_MCP_ADDR: %w", err)
		}
		if cfg.AuthToken == "" && !isLoopbackListenAddr(cfg.Addr) {
			return config{}, errors.New("TELEGRAM_STICKER_MCP_AUTH_TOKEN is required when HTTP listens on a non-loopback address")
		}
	default:
		return config{}, fmt.Errorf("unsupported TELEGRAM_STICKER_MCP_TRANSPORT %q (want stdio or http)", cfg.Transport)
	}
	return cfg, nil
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func runHTTP(provider *stickerServiceProvider, cfg config) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		service, err := provider.ResolveHeader(req.Header)
		if err != nil {
			return newMCPServer(provider.Resolve, describedStickerSet{}, err.Error())
		}
		catalog, err := service.CachedDescribedSet(req.Context())
		service.WarmDescriptions(req.Context())
		if err != nil {
			return newMCPServer(provider.Resolve, describedStickerSet{Name: service.setName}, err.Error())
		}
		return newMCPServer(provider.Resolve, catalog, "")
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", requireBearer(cfg.AuthToken, mcpHandler))
	mux.Handle("/api/", requireBearer(cfg.AuthToken, stickerManagementHandler(provider)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func(parent context.Context) {
		<-parent.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}(ctx)
	log.Printf("%s listening on %s", serverName, cfg.Addr)
	return httpServer.ListenAndServe()
}

func stickerManagementHandler(provider *stickerServiceProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		service, err := provider.ResolveHeader(req.Header)
		if err != nil {
			http.Error(w, "sticker service configuration is unavailable", http.StatusBadRequest)
			return
		}
		if req.Method == http.MethodGet && req.URL.Path == "/api/catalog" {
			catalog, catalogErr := service.Catalog(req.Context())
			if catalogErr != nil {
				http.Error(w, "sticker catalog is unavailable", http.StatusBadGateway)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, catalog)
			return
		}
		if req.Method == http.MethodPost && req.URL.Path == "/api/profile" {
			var input struct {
				Model         string `json:"model"`
				PromptVersion string `json:"prompt_version"`
			}
			if decodeErr := decodeManagementJSON(req, &input); decodeErr != nil {
				http.Error(w, "invalid Sticker recognition profile", http.StatusBadRequest)
				return
			}
			if profileErr := service.ActivateDescriptionProfile(
				req.Context(), input.Model, input.PromptVersion,
			); profileErr != nil {
				http.Error(w, "invalid Sticker recognition profile", http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if req.Method == http.MethodPost && req.URL.Path == "/api/catalog/refresh" {
			if _, refreshErr := service.RefreshStickerSet(req.Context()); refreshErr != nil {
				http.Error(w, "Sticker Set refresh failed", http.StatusBadGateway)
				return
			}
			catalog, catalogErr := service.Catalog(req.Context())
			if catalogErr != nil {
				http.Error(w, "sticker catalog is unavailable", http.StatusBadGateway)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, catalog)
			return
		}

		path := strings.Trim(strings.TrimPrefix(req.URL.Path, "/api/stickers/"), "/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			http.NotFound(w, req)
			return
		}
		stickerID, action := parts[0], parts[1]
		switch {
		case req.Method == http.MethodGet && action == "preview":
			data, contentType, mediaErr := service.StickerMedia(req.Context(), stickerID)
			if mediaErr != nil {
				http.Error(w, "sticker preview is unavailable", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", safeStickerPreviewContentType(contentType))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "private, max-age=3600")
			w.WriteHeader(http.StatusOK)
			// #nosec G705 -- this authenticated endpoint intentionally returns
			// bounded Telegram media with an allowlisted MIME type and nosniff.
			_, _ = w.Write(data)
		case req.Method == http.MethodPost && action == "recognition":
			var input struct {
				Model         string `json:"model"`
				PromptVersion string `json:"prompt_version"`
				Description   string `json:"description"`
				Status        string `json:"status"`
				Attempts      int    `json:"attempts"`
			}
			if decodeErr := decodeManagementJSON(req, &input); decodeErr != nil {
				http.Error(w, "invalid Sticker recognition result", http.StatusBadRequest)
				return
			}
			status := strings.TrimSpace(input.Status)
			if status == "" {
				status = descriptionStatusReady
			}
			entry, storeErr := service.StoreRecognition(
				req.Context(), stickerID, input.Model, input.PromptVersion,
				status, input.Description, input.Attempts,
			)
			if storeErr != nil {
				http.Error(w, "sticker recognition result could not be stored", http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, entry)
		default:
			http.NotFound(w, req)
		}
	})
}

func safeStickerPreviewContentType(contentType string) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mediaType {
	case "image/webp", "image/png", "image/jpeg", "video/webm", "application/gzip":
		return mediaType
	default:
		return "application/octet-stream"
	}
}

func decodeManagementJSON(req *http.Request, output any) error {
	if req == nil || req.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, req.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requireBearer(token string, next http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, req)
	})
}

func loadDotenv(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	// #nosec G304 -- the dotenv path is an explicit operator-supplied CLI value.
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open env file: %w", err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, rawValue, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("parse env file line %d: expected KEY=VALUE", lineNumber)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value, err := parseDotenvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return fmt.Errorf("parse env file line %d: %w", lineNumber, err)
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env file key %q: %w", key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	return nil
}

func parseDotenvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", errors.New("unterminated single-quoted value")
		}
		return strings.TrimSuffix(strings.TrimPrefix(value, "'"), "'"), nil
	}
	if strings.HasPrefix(value, `"`) {
		if len(value) < 2 || !strings.HasSuffix(value, `"`) {
			return "", errors.New("unterminated double-quoted value")
		}
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		return decoded, nil
	}
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = value[:comment]
	}
	return strings.TrimSpace(value), nil
}
