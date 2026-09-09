//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type apiClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func newAPIClient(baseURL, username, password string) *apiClient {
	client := &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 6 * time.Minute},
	}
	var login map[string]any
	if err := client.request(http.MethodPost, "/auth/login", map[string]any{
		"username": username,
		"password": password,
	}, &login, http.StatusOK); err != nil {
		globalFixtureErr = fmt.Errorf("login: %w", err)
		return client
	}
	client.token, _ = login["access_token"].(string)
	return client
}

func (c *apiClient) forBaseURL(baseURL string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   c.token,
		client:  &http.Client{Timeout: 6 * time.Minute},
	}
}

func (c *apiClient) ensureFixture(fakeBaseURL string) (string, error) {
	if globalFixtureErr != nil {
		return "", globalFixtureErr
	}
	if c.token == "" {
		return "", errors.New("login returned an empty access token")
	}
	providerID, err := c.ensureProvider(fakeBaseURL)
	if err != nil {
		return "", err
	}
	modelID, err := c.ensureModel(providerID)
	if err != nil {
		return "", err
	}
	botID, err := c.ensureBot()
	if err != nil {
		return "", err
	}
	if err := c.configureBot(botID, modelID); err != nil {
		return "", err
	}
	return botID, nil
}

func (c *apiClient) ensureProvider(fakeBaseURL string) (string, error) {
	var body any
	if err := c.request(http.MethodGet, "/providers", nil, &body, http.StatusOK); err != nil {
		return "", err
	}
	payload := map[string]any{
		"name":        "session-runtime-acceptance",
		"client_type": "openai-completions",
		"config": map[string]any{
			"base_url": fakeBaseURL,
			"api_key":  "session-runtime-acceptance-key",
		},
	}
	for _, provider := range objectList(body) {
		if stringValue(provider["name"]) != "session-runtime-acceptance" {
			continue
		}
		id := stringValue(provider["id"])
		var updated map[string]any
		if err := c.request(http.MethodPut, "/providers/"+url.PathEscape(id), payload, &updated, http.StatusOK); err != nil {
			return "", err
		}
		return stringValue(updated["id"]), nil
	}

	var created map[string]any
	if err := c.request(http.MethodPost, "/providers", payload, &created, http.StatusCreated); err != nil {
		return "", err
	}
	return stringValue(created["id"]), nil
}

func (c *apiClient) ensureModel(providerID string) (string, error) {
	var body any
	if err := c.request(http.MethodGet, "/models", nil, &body, http.StatusOK); err != nil {
		return "", err
	}
	for _, model := range objectList(body) {
		if stringValue(model["provider_id"]) == providerID &&
			stringValue(model["model_id"]) == "session-runtime-acceptance-model" {
			return stringValue(model["id"]), nil
		}
	}

	var created map[string]any
	if err := c.request(http.MethodPost, "/models", map[string]any{
		"model_id":    "session-runtime-acceptance-model",
		"name":        "Session Runtime Acceptance Model",
		"provider_id": providerID,
		"type":        "chat",
		"enable":      true,
		"config": map[string]any{
			"compatibilities": []string{"tool-call"},
			"context_window":  32768,
			"thinking_mode":   "none",
		},
	}, &created, http.StatusCreated); err != nil {
		return "", err
	}
	return stringValue(created["id"]), nil
}

func (c *apiClient) ensureBot() (string, error) {
	var body any
	if err := c.request(http.MethodGet, "/bots", nil, &body, http.StatusOK); err != nil {
		return "", err
	}
	for _, bot := range objectList(body) {
		if stringValue(bot["name"]) == "session-runtime-acceptance" {
			return c.waitForBot(stringValue(bot["id"]))
		}
	}

	var created map[string]any
	if err := c.request(http.MethodPost, "/bots", map[string]any{
		"name":           "session-runtime-acceptance",
		"display_name":   "Session Runtime Acceptance",
		"wait_for_ready": true,
	}, &created, http.StatusCreated); err != nil {
		return "", err
	}
	return c.waitForBot(stringValue(created["id"]))
}

func (c *apiClient) waitForBot(botID string) (string, error) {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		var bot map[string]any
		if err := c.request(http.MethodGet, "/bots/"+url.PathEscape(botID), nil, &bot, http.StatusOK); err != nil {
			return "", err
		}
		if stringValue(bot["status"]) == "ready" {
			return botID, nil
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("bot %s did not become ready", botID)
}

func (c *apiClient) configureBot(botID, modelID string) error {
	var response any
	return c.request(http.MethodPut, "/bots/"+url.PathEscape(botID)+"/settings", map[string]any{
		"chat_model_id":      modelID,
		"chat_runtime":       "model",
		"compaction_enabled": false,
		"reasoning_enabled":  false,
		"tool_approval_config": map[string]any{
			"enabled": false,
			"read":    map[string]any{"mode": "allow"},
			"write":   map[string]any{"mode": "allow"},
			"exec":    map[string]any{"mode": "allow"},
		},
	}, &response, http.StatusOK)
}

func (c *apiClient) createSession(botID, scenario string) (string, error) {
	var created map[string]any
	err := c.request(http.MethodPost, "/bots/"+url.PathEscape(botID)+"/sessions", map[string]any{
		"type":  "chat",
		"title": fmt.Sprintf("session-runtime-%s-%d", scenario, time.Now().UnixNano()),
	}, &created, http.StatusCreated)
	if err != nil {
		return "", err
	}
	return stringValue(created["id"]), nil
}

func (c *apiClient) history(botID, sessionID string) (map[string]any, error) {
	query := url.Values{
		"session_id": []string{sessionID},
		"limit":      []string{"100"},
	}
	var history map[string]any
	err := c.request(
		http.MethodGet,
		"/bots/"+url.PathEscape(botID)+"/messages?"+query.Encode(),
		nil,
		&history,
		http.StatusOK,
	)
	return history, err
}

func (c *apiClient) setToolApprovalRequired(botID string, required bool) error {
	execMode := "allow"
	if required {
		execMode = "ask"
	}
	var response map[string]any
	return c.request(
		http.MethodPut,
		"/bots/"+url.PathEscape(botID)+"/settings",
		map[string]any{
			"tool_approval_config": map[string]any{
				"enabled": required,
				"read":    map[string]any{"mode": "allow"},
				"write":   map[string]any{"mode": "allow"},
				"exec":    map[string]any{"mode": execMode},
			},
		},
		&response,
		http.StatusOK,
	)
}

func (c *apiClient) approveTool(botID, approvalID string) error {
	var response map[string]any
	return c.request(
		http.MethodPost,
		"/bots/"+url.PathEscape(botID)+"/tool-approvals/"+url.PathEscape(approvalID)+"/approve",
		map[string]any{},
		&response,
		http.StatusOK,
	)
}

func historyContainsRoleText(history map[string]any, role, text string) bool {
	for _, item := range objectList(history) {
		if stringValue(item["role"]) == role && valueContainsString(item, text) {
			return true
		}
	}
	return false
}

func historyMessageIDByRole(history map[string]any, role string) string {
	for _, item := range objectList(history) {
		if stringValue(item["role"]) == role {
			return stringValue(item["id"])
		}
	}
	return ""
}

func (c *apiClient) request(method, path string, body any, result any, statuses ...int) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	// The target is an explicit acceptance-test Server URL, optionally supplied
	// by the developer running the integration environment.
	response, err := c.client.Do(request) //nolint:gosec // intentional black-box test target
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if !containsStatus(statuses, response.StatusCode) {
		return fmt.Errorf("%s %s returned HTTP %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(encoded)))
	}
	if result == nil || len(bytes.TrimSpace(encoded)) == 0 {
		return nil
	}
	if err := json.Unmarshal(encoded, result); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

func objectList(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		return mapsFromSlice(typed)
	case map[string]any:
		for _, key := range []string{"items", "providers", "models"} {
			if raw, ok := typed[key].([]any); ok {
				return mapsFromSlice(raw)
			}
		}
	}
	return nil
}

func mapsFromSlice(items []any) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func containsStatus(statuses []int, actual int) bool {
	for _, status := range statuses {
		if status == actual {
			return true
		}
	}
	return false
}
