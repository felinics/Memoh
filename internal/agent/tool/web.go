package tools

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/redact"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/settings"
)

type WebProvider struct {
	logger          *slog.Logger
	settings        *settings.Service
	searchProviders *searchproviders.Service
}

func NewWebProvider(log *slog.Logger, settingsSvc *settings.Service, searchSvc *searchproviders.Service) *WebProvider {
	if log == nil {
		log = slog.Default()
	}
	return &WebProvider{
		logger:          log.With(slog.String("tool", "web")),
		settings:        settingsSvc,
		searchProviders: searchSvc,
	}
}

func (p *WebProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p.settings == nil || p.searchProviders == nil {
		return nil, nil
	}
	sess := session
	return []sdk.Tool{
		{
			Name:        ToolWebSearch().String(),
			Description: "Search web results via configured search provider.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query"},
					"count": map[string]any{"type": "integer", "description": "Number of results, default 5"},
				},
				"required": []string{"query"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execWebSearch(ctx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

func (p *WebProvider) execWebSearch(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	searchProviderID := strings.TrimSpace(botSettings.SearchProviderID)
	if searchProviderID == "" {
		return nil, errors.New("search provider not configured for this bot")
	}
	provider, err := p.searchProviders.GetRawByID(ctx, searchProviderID)
	if err != nil {
		return nil, err
	}
	if !provider.Enable {
		return nil, errors.New("search provider is disabled")
	}
	registerSearchProviderSecrets(provider)

	query := strings.TrimSpace(StringArg(args, "query"))
	if query == "" {
		return nil, errors.New("query is required")
	}
	count := 5
	if value, ok, err := IntArg(args, "count"); err != nil {
		return nil, err
	} else if ok && value > 0 {
		count = value
	}
	if count > 20 {
		count = 20
	}
	return p.callSearch(ctx, provider.Provider, provider.Config, query, count)
}

func (*WebProvider) callSearch(ctx context.Context, providerName string, configJSON []byte, query string, count int) (any, error) {
	switch strings.TrimSpace(providerName) {
	case string(searchproviders.ProviderBrave):
		return callBraveSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderBing):
		return callBingSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderGoogle):
		return callGoogleSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderTavily):
		return callTavilySearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderSogou):
		return callSogouSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderSerper):
		return callSerperSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderSearXNG):
		return callSearXNGSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderJina):
		return callJinaSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderExa):
		return callExaSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderBocha):
		return callBochaSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderDuckDuckGo):
		return callDuckDuckGoSearch(ctx, configJSON, query, count)
	case string(searchproviders.ProviderYandex):
		return callYandexSearch(ctx, configJSON, query, count)
	default:
		return nil, errors.New("unsupported search provider")
	}
}

// ---- search provider implementations ----

func callBraveSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := strings.TrimRight(firstNonEmpty(stringValue(cfg["base_url"]), "https://api.search.brave.com/res/v1/web/search"), "/")
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("invalid search provider base_url")
	}
	params := reqURL.Query()
	params.Set("q", query)
	params.Set("count", strconv.Itoa(count))
	reqURL.RawQuery = params.Encode()
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey := stringValue(cfg["api_key"]); strings.TrimSpace(apiKey) != "" {
		req.Header.Set("X-Subscription-Token", strings.TrimSpace(apiKey))
	}
	resp, err := client.Do(req) //nolint:gosec // web browsing tool intentionally fetches user-specified URLs
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Web struct {
			Results []struct {
				Title, URL, Description string
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	return buildSearchResults(query, raw.Web.Results, func(r struct{ Title, URL, Description string }) map[string]any {
		return map[string]any{"title": r.Title, "url": r.URL, "description": r.Description}
	}), nil
}

func callBingSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := strings.TrimRight(firstNonEmpty(stringValue(cfg["base_url"]), "https://api.bing.microsoft.com/v7.0/search"), "/")
	reqURL, _ := url.Parse(endpoint)
	params := reqURL.Query()
	params.Set("q", query)
	params.Set("count", strconv.Itoa(count))
	reqURL.RawQuery = params.Encode()
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	req.Header.Set("Accept", "application/json")
	if apiKey := stringValue(cfg["api_key"]); apiKey != "" {
		req.Header.Set("Ocp-Apim-Subscription-Key", apiKey)
	}
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		WebPages struct {
			Value []struct {
				Name, URL, Snippet string
			} `json:"value"`
		} `json:"webPages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	results := make([]map[string]any, 0, len(raw.WebPages.Value))
	for _, item := range raw.WebPages.Value {
		results = append(results, map[string]any{"title": item.Name, "url": item.URL, "description": item.Snippet})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callGoogleSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := strings.TrimRight(firstNonEmpty(stringValue(cfg["base_url"]), "https://customsearch.googleapis.com/customsearch/v1"), "/")
	reqURL, _ := url.Parse(endpoint)
	cx := stringValue(cfg["cx"])
	if cx == "" {
		return nil, errors.New("google custom search requires cx (search engine ID)")
	}
	if count > 10 {
		count = 10
	}
	params := reqURL.Query()
	params.Set("q", query)
	params.Set("cx", cx)
	params.Set("num", strconv.Itoa(count))
	if apiKey := stringValue(cfg["api_key"]); apiKey != "" {
		params.Set("key", apiKey)
	}
	reqURL.RawQuery = params.Encode()
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Items []struct {
			Title, Link, Snippet string
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	results := make([]map[string]any, 0, len(raw.Items))
	for _, item := range raw.Items {
		results = append(results, map[string]any{"title": item.Title, "url": item.Link, "description": item.Snippet})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callTavilySearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := firstNonEmpty(stringValue(cfg["base_url"]), "https://api.tavily.com/search")
	apiKey := stringValue(cfg["api_key"])
	if apiKey == "" {
		return nil, errors.New("tavily API key is required")
	}
	payload, _ := json.Marshal(map[string]any{"query": query, "max_results": count})
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Results []struct {
			Title, URL, Content string
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	results := make([]map[string]any, 0, len(raw.Results))
	for _, item := range raw.Results {
		results = append(results, map[string]any{"title": item.Title, "url": item.URL, "description": item.Content})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callSogouSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	host := firstNonEmpty(stringValue(cfg["base_url"]), "wsa.tencentcloudapi.com")
	secretID := stringValue(cfg["secret_id"])
	secretKey := stringValue(cfg["secret_key"])
	if secretID == "" || secretKey == "" {
		return nil, errors.New("sogou search requires Tencent Cloud SecretId and SecretKey")
	}
	action := "SearchPro"
	version := "2025-05-08"
	service := "wsa"
	payload, _ := json.Marshal(map[string]any{"Query": query, "Mode": 0})
	now := time.Now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	date := now.Format("2006-01-02")
	hashedPayload := sha256Hex(payload)
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\n", "application/json", host)
	signedHeaders := "content-type;host"
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s", "POST", "/", "", canonicalHeaders, signedHeaders, hashedPayload)
	credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, service)
	stringToSign := fmt.Sprintf("TC3-HMAC-SHA256\n%s\n%s\n%s", timestamp, credentialScope, sha256Hex([]byte(canonicalRequest)))
	secretDate := hmacSHA256([]byte("TC3"+secretKey), []byte(date))
	secretService := hmacSHA256(secretDate, []byte(service))
	secretSigning := hmacSHA256(secretService, []byte("tc3_request"))
	signature := hex.EncodeToString(hmacSHA256(secretSigning, []byte(stringToSign)))
	authorization := fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", secretID, credentialScope, signedHeaders, signature)
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Timestamp", timestamp)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var rawResp struct {
		Response struct {
			Error *struct{ Code, Message string } `json:"Error,omitempty"`
			Pages []json.RawMessage               `json:"Pages"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(body, &rawResp); err != nil {
		return nil, errors.New("invalid search response")
	}
	if rawResp.Response.Error != nil {
		return nil, fmt.Errorf("sogou search failed: %s", rawResp.Response.Error.Message)
	}
	type sogouPage struct {
		Title, URL, Passage string
		Score               float64 `json:"scour"`
	}
	var pages []sogouPage
	for _, raw := range rawResp.Response.Pages {
		var rawStr string
		if err := json.Unmarshal(raw, &rawStr); err == nil {
			var page sogouPage
			if json.Unmarshal([]byte(rawStr), &page) == nil {
				pages = append(pages, page)
			}
		} else {
			var page sogouPage
			if json.Unmarshal(raw, &page) == nil {
				pages = append(pages, page)
			}
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Score > pages[j].Score })
	results := make([]map[string]any, 0)
	for i, page := range pages {
		if i >= count {
			break
		}
		results = append(results, map[string]any{"title": page.Title, "url": page.URL, "description": page.Passage})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callSerperSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := firstNonEmpty(stringValue(cfg["base_url"]), "https://google.serper.dev/search")
	apiKey := stringValue(cfg["api_key"])
	if apiKey == "" {
		return nil, errors.New("serper API key is required")
	}
	payload, _ := json.Marshal(map[string]any{"q": query})
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-KEY", apiKey)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Organic []struct {
			Title, Link, Description string
			Position                 int
		} `json:"organic"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	sort.Slice(raw.Organic, func(i, j int) bool { return raw.Organic[i].Position < raw.Organic[j].Position })
	results := make([]map[string]any, 0)
	for i, item := range raw.Organic {
		if i >= count {
			break
		}
		results = append(results, map[string]any{"title": item.Title, "url": item.Link, "description": item.Description})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callSearXNGSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	baseURL := stringValue(cfg["base_url"])
	if baseURL == "" {
		return nil, errors.New("SearXNG base URL is required")
	}
	reqURL, _ := url.Parse(strings.TrimRight(baseURL, "/"))
	params := reqURL.Query()
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("pageno", "1")
	if lang := stringValue(cfg["language"]); lang != "" {
		params.Set("language", lang)
	}
	if ss := stringValue(cfg["safesearch"]); ss != "" {
		params.Set("safesearch", ss)
	}
	if cats := stringValue(cfg["categories"]); cats != "" {
		params.Set("categories", cats)
	}
	reqURL.RawQuery = params.Encode()
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Results []struct {
			Title, URL, Content string
			Score               float64
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	sort.Slice(raw.Results, func(i, j int) bool { return raw.Results[i].Score > raw.Results[j].Score })
	results := make([]map[string]any, 0)
	for i, item := range raw.Results {
		if i >= count {
			break
		}
		results = append(results, map[string]any{"title": item.Title, "url": item.URL, "description": item.Content})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callJinaSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := firstNonEmpty(stringValue(cfg["base_url"]), "https://s.jina.ai/")
	apiKey := stringValue(cfg["api_key"])
	if apiKey == "" {
		return nil, errors.New("jina API key is required")
	}
	if count > 10 {
		count = 10
	}
	payload, _ := json.Marshal(map[string]any{"q": query, "count": count})
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Retain-Images", "none")
	req.Header.Set("Authorization", apiKey)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Data []struct{ Title, URL, Content string } `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	results := make([]map[string]any, 0, len(raw.Data))
	for _, item := range raw.Data {
		results = append(results, map[string]any{"title": item.Title, "url": item.URL, "description": item.Content})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callExaSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := firstNonEmpty(stringValue(cfg["base_url"]), "https://api.exa.ai/search")
	apiKey := stringValue(cfg["api_key"])
	if apiKey == "" {
		return nil, errors.New("exa API key is required")
	}
	payload, _ := json.Marshal(map[string]any{"query": query, "numResults": count, "contents": map[string]any{"text": true, "highlights": true}, "type": "auto"})
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Results []struct{ Title, URL, Text string } `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	results := make([]map[string]any, 0, len(raw.Results))
	for _, item := range raw.Results {
		results = append(results, map[string]any{"title": item.Title, "url": item.URL, "description": item.Text})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callBochaSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := firstNonEmpty(stringValue(cfg["base_url"]), "https://api.bochaai.com/v1/web-search")
	apiKey := stringValue(cfg["api_key"])
	if apiKey == "" {
		return nil, errors.New("bocha API key is required")
	}
	payload, _ := json.Marshal(map[string]any{"query": query, "summary": true, "freshness": "noLimit", "count": count})
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Data struct {
			WebPages struct {
				Value []struct{ Name, URL, Summary string } `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid search response")
	}
	results := make([]map[string]any, 0, len(raw.Data.WebPages.Value))
	for _, item := range raw.Data.WebPages.Value {
		results = append(results, map[string]any{"title": item.Name, "url": item.URL, "description": item.Summary})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callDuckDuckGoSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := firstNonEmpty(stringValue(cfg["base_url"]), "https://html.duckduckgo.com/html/")
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	form := url.Values{}
	form.Set("q", query)
	form.Set("b", "")
	form.Set("kl", "")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	htmlStr := string(body)
	links := ddgResultLinkRe.FindAllStringSubmatch(htmlStr, -1)
	titles := ddgResultTitleRe.FindAllStringSubmatch(htmlStr, -1)
	snippets := ddgResultSnippetRe.FindAllStringSubmatch(htmlStr, -1)
	n := len(links)
	if len(titles) < n {
		n = len(titles)
	}
	if count < n {
		n = count
	}
	results := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		rawURL := html.UnescapeString(links[i][1])
		realURL := extractDDGURL(rawURL)
		title := html.UnescapeString(strings.TrimSpace(titles[i][1]))
		snippet := ""
		if i < len(snippets) {
			snippet = html.UnescapeString(strings.TrimSpace(ddgHTMLTagRe.ReplaceAllString(snippets[i][1], "")))
		}
		if realURL == "" {
			continue
		}
		results = append(results, map[string]any{"title": title, "url": realURL, "description": snippet})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func callYandexSearch(ctx context.Context, configJSON []byte, query string, count int) (any, error) {
	cfg := parseSearchConfig(configJSON)
	endpoint := firstNonEmpty(stringValue(cfg["base_url"]), "https://searchapi.api.cloud.yandex.net/v2/web/search")
	apiKey := stringValue(cfg["api_key"])
	if apiKey == "" {
		return nil, errors.New("yandex API key is required")
	}
	searchType := firstNonEmpty(stringValue(cfg["search_type"]), "SEARCH_TYPE_RU")
	payload, _ := json.Marshal(map[string]any{
		"query":     map[string]any{"queryText": query, "searchType": searchType},
		"groupSpec": map[string]any{"groupMode": "GROUP_MODE_DEEP", "groupsOnPage": count, "docsInGroup": 1},
	})
	timeout := parseSearchTimeout(configJSON, 15*time.Second)
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Api-Key "+apiKey)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildSearchHTTPError(resp.StatusCode, body)
	}
	var rawResp struct {
		RawData string `json:"rawData"`
	}
	if err := json.Unmarshal(body, &rawResp); err != nil {
		return nil, errors.New("invalid search response")
	}
	xmlData, err := base64.StdEncoding.DecodeString(rawResp.RawData)
	if err != nil {
		return nil, errors.New("failed to decode Yandex response")
	}
	results, err := parseYandexXML(xmlData)
	if err != nil {
		return nil, errors.New("failed to parse Yandex XML response")
	}
	return map[string]any{"query": query, "results": results}, nil
}

// ---- helpers ----

func buildSearchResults[T any](query string, items []T, mapper func(T) map[string]any) map[string]any {
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		results = append(results, mapper(item))
	}
	return map[string]any{"query": query, "results": results}
}

func buildSearchHTTPError(statusCode int, body []byte) error {
	detail := extractJSONErrorMessage(body)
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}
	if len(detail) > 200 {
		detail = detail[:200] + "..."
	}
	if detail != "" {
		return fmt.Errorf("search request failed (HTTP %d): %s", statusCode, detail)
	}
	return fmt.Errorf("search request failed (HTTP %d)", statusCode)
}

func extractJSONErrorMessage(body []byte) string {
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return ""
	}
	for _, key := range []string{"error", "message", "detail", "error_message"} {
		v, ok := obj[key]
		if !ok {
			continue
		}
		switch val := v.(type) {
		case string:
			return val
		case map[string]any:
			if msg, ok := val["message"].(string); ok {
				return msg
			}
		}
	}
	return ""
}

func parseSearchTimeout(configJSON []byte, fallback time.Duration) time.Duration {
	cfg := parseSearchConfig(configJSON)
	raw, ok := cfg["timeout_seconds"]
	if !ok {
		return fallback
	}
	switch value := raw.(type) {
	case float64:
		if value > 0 {
			return time.Duration(value * float64(time.Second))
		}
	case int:
		if value > 0 {
			return time.Duration(value) * time.Second
		}
	}
	return fallback
}

func parseSearchConfig(configJSON []byte) map[string]any {
	if len(configJSON) == 0 {
		return map[string]any{}
	}
	var cfg map[string]any
	if err := json.Unmarshal(configJSON, &cfg); err != nil || cfg == nil {
		return map[string]any{}
	}
	return cfg
}

func stringValue(raw any) string {
	if value, ok := raw.(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

var searchProviderSecretFields = []string{"api_key", "secret_id", "secret_key"}

func registerSearchProviderSecrets(provider sqlc.SearchProvider) {
	cfg := parseSearchConfig(provider.Config)
	var secrets []string
	for _, key := range searchProviderSecretFields {
		if v := stringValue(cfg[key]); v != "" {
			secrets = append(secrets, v)
		}
	}
	if len(secrets) > 0 {
		redact.SetSecrets("search:"+provider.ID.String(), secrets...)
	}
}

var (
	ddgResultLinkRe    = regexp.MustCompile(`class="result__a"[^>]*href="([^"]+)"`)
	ddgResultTitleRe   = regexp.MustCompile(`class="result__a"[^>]*>([^<]+)<`)
	ddgResultSnippetRe = regexp.MustCompile(`class="result__snippet"[^>]*>([\s\S]*?)</a>`)
	ddgHTMLTagRe       = regexp.MustCompile(`<[^>]*>`)
)

func extractDDGURL(rawURL string) string {
	if strings.Contains(rawURL, "uddg=") {
		parsed, err := url.Parse(rawURL)
		if err == nil {
			if uddg := parsed.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	return rawURL
}

type xmlInnerText string

func (t *xmlInnerText) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	var buf strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case xml.CharData:
			buf.Write(v)
		case xml.StartElement:
			var inner xmlInnerText
			if err := d.DecodeElement(&inner, &v); err != nil {
				return err
			}
			buf.WriteString(string(inner))
		case xml.EndElement:
			*t = xmlInnerText(buf.String())
			return nil
		}
	}
	*t = xmlInnerText(buf.String())
	return nil
}

type yandexResponse struct {
	XMLName xml.Name      `xml:"response"`
	Results yandexResults `xml:"results"`
}
type yandexResults struct {
	Grouping yandexGrouping `xml:"grouping"`
}
type yandexGrouping struct {
	Groups []yandexGroup `xml:"group"`
}
type yandexGroup struct {
	Doc yandexDoc `xml:"doc"`
}
type yandexDoc struct {
	URL      xmlInnerText   `xml:"url"`
	Title    xmlInnerText   `xml:"title"`
	Passages yandexPassages `xml:"passages"`
}
type yandexPassages struct {
	Passage []xmlInnerText `xml:"passage"`
}

func parseYandexXML(data []byte) ([]map[string]any, error) {
	var resp yandexResponse
	if err := xml.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, len(resp.Results.Grouping.Groups))
	for _, group := range resp.Results.Grouping.Groups {
		snippet := ""
		if len(group.Doc.Passages.Passage) > 0 {
			snippet = string(group.Doc.Passages.Passage[0])
		}
		results = append(results, map[string]any{"title": string(group.Doc.Title), "url": string(group.Doc.URL), "description": snippet})
	}
	return results, nil
}
