package misskey

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// apiRequest sends a POST request to the Misskey API endpoint.
func apiRequest(ctx context.Context, cfg Config, endpoint string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("misskey api marshal: %w", err)
	}
	url := cfg.apiURL() + "/" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("misskey api request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req) //nolint:gosec // G704: URL is user-configured, validated at config level
	if err != nil {
		return nil, fmt.Errorf("misskey api do: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("misskey api read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("misskey api %s: status %d: %s", endpoint, resp.StatusCode, string(respBody))
	}
	return json.RawMessage(respBody), nil
}

// --- API payloads ---

// createNoteRequest is the request body for notes/create.
type createNoteRequest struct {
	I          string   `json:"i"`
	Text       string   `json:"text,omitempty"`
	Visibility string   `json:"visibility,omitempty"`
	ReplyID    string   `json:"replyId,omitempty"`
	CW         string   `json:"cw,omitempty"`
	FileIDs    []string `json:"fileIds,omitempty"`
}

// createNoteResponse is the response from notes/create.
type createNoteResponse struct {
	CreatedNote struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"user"`
	} `json:"createdNote"`
}

// meResponse is the response from i (self user info).
type meResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
}

// createNote creates a note on the Misskey instance.
func createNote(ctx context.Context, cfg Config, text, replyID, visibility string) (*createNoteResponse, error) {
	if visibility == "" {
		visibility = "public"
	}
	req := createNoteRequest{
		I:          cfg.AccessToken,
		Text:       text,
		Visibility: visibility,
		ReplyID:    replyID,
	}
	raw, err := apiRequest(ctx, cfg, "notes/create", req)
	if err != nil {
		return nil, err
	}
	var resp createNoteResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("misskey notes/create unmarshal: %w", err)
	}
	return &resp, nil
}

// getMe retrieves the authenticated user's info.
func getMe(ctx context.Context, cfg Config) (*meResponse, error) {
	raw, err := apiRequest(ctx, cfg, "i", map[string]string{"i": cfg.AccessToken})
	if err != nil {
		return nil, err
	}
	var resp meResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("misskey i unmarshal: %w", err)
	}
	return &resp, nil
}

// createReaction adds an emoji reaction to a note.
func createReaction(ctx context.Context, cfg Config, noteID, reaction string) error {
	_, err := apiRequest(ctx, cfg, "notes/reactions/create", map[string]string{
		"i":        cfg.AccessToken,
		"noteId":   noteID,
		"reaction": reaction,
	})
	return err
}

// deleteReaction removes a reaction from a note.
func deleteReaction(ctx context.Context, cfg Config, noteID string) error {
	_, err := apiRequest(ctx, cfg, "notes/reactions/delete", map[string]string{
		"i":      cfg.AccessToken,
		"noteId": noteID,
	})
	return err
}
