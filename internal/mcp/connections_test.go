package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestInferTypeAndConfig_Stdio(t *testing.T) {
	req := UpsertRequest{
		Name:    "filesystem",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/path"},
		Env:     map[string]string{"TOKEN": "abc"},
		Cwd:     "/workspace",
	}
	typ, config, err := inferTypeAndConfig(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ != "stdio" {
		t.Fatalf("expected type stdio, got %s", typ)
	}
	if config["command"] != "npx" {
		t.Fatalf("expected command npx, got %v", config["command"])
	}
	args, ok := config["args"].([]string)
	if !ok || len(args) != 3 {
		t.Fatalf("expected 3 args, got %v", config["args"])
	}
	env, ok := config["env"].(map[string]string)
	if !ok || env["TOKEN"] != "abc" {
		t.Fatalf("expected env TOKEN=abc, got %v", config["env"])
	}
	if config["cwd"] != "/workspace" {
		t.Fatalf("expected cwd /workspace, got %v", config["cwd"])
	}
}

func TestInferTypeAndConfig_HTTP(t *testing.T) {
	req := UpsertRequest{
		Name:    "remote",
		URL:     "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer sk-xxx"},
	}
	typ, config, err := inferTypeAndConfig(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ != "http" {
		t.Fatalf("expected type http, got %s", typ)
	}
	if config["url"] != "https://example.com/mcp" {
		t.Fatalf("expected url, got %v", config["url"])
	}
	headers, ok := config["headers"].(map[string]string)
	if !ok || headers["Authorization"] != "Bearer sk-xxx" {
		t.Fatalf("expected headers, got %v", config["headers"])
	}
}

func TestInferTypeAndConfig_SSE(t *testing.T) {
	req := UpsertRequest{
		Name:      "sse-server",
		URL:       "https://example.com/sse",
		Transport: "sse",
	}
	typ, _, err := inferTypeAndConfig(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ != "sse" {
		t.Fatalf("expected type sse, got %s", typ)
	}
}

func TestInferTypeAndConfig_NoCommandNoURL(t *testing.T) {
	req := UpsertRequest{Name: "empty"}
	_, _, err := inferTypeAndConfig(req)
	if err == nil {
		t.Fatal("expected error for missing command and url")
	}
}

func TestInferTypeAndConfig_BothCommandAndURL(t *testing.T) {
	req := UpsertRequest{
		Name:    "conflict",
		Command: "npx",
		URL:     "https://example.com",
	}
	_, _, err := inferTypeAndConfig(req)
	if err == nil {
		t.Fatal("expected error for both command and url")
	}
}

func TestConnectionToExportEntry_Stdio(t *testing.T) {
	conn := Connection{
		Name: "fs",
		Type: "stdio",
		Config: map[string]any{
			"command": "npx",
			"args":    []any{"-y", "server"},
			"env":     map[string]any{"KEY": "val"},
			"cwd":     "/work",
		},
	}
	entry := connectionToExportEntry(conn)
	if entry.Command != "npx" {
		t.Fatalf("expected command npx, got %s", entry.Command)
	}
	if len(entry.Args) != 2 {
		t.Fatalf("expected 2 args, got %v", entry.Args)
	}
	if entry.Env["KEY"] != "val" {
		t.Fatalf("expected env KEY=val, got %v", entry.Env)
	}
	if entry.Cwd != "/work" {
		t.Fatalf("expected cwd /work, got %s", entry.Cwd)
	}
	if entry.URL != "" {
		t.Fatalf("expected empty url, got %s", entry.URL)
	}
}

func TestConnectionToExportEntry_HTTP(t *testing.T) {
	conn := Connection{
		Name: "remote",
		Type: "http",
		Config: map[string]any{
			"url":     "https://example.com/mcp",
			"headers": map[string]any{"Authorization": "Bearer xxx"},
		},
	}
	entry := connectionToExportEntry(conn)
	if entry.URL != "https://example.com/mcp" {
		t.Fatalf("expected url, got %s", entry.URL)
	}
	if entry.Headers["Authorization"] != "Bearer xxx" {
		t.Fatalf("expected headers, got %v", entry.Headers)
	}
	if entry.Transport != "" {
		t.Fatalf("expected empty transport for http, got %s", entry.Transport)
	}
}

func TestConnectionToExportEntry_SSE(t *testing.T) {
	conn := Connection{
		Name:   "sse",
		Type:   "sse",
		Config: map[string]any{"url": "https://example.com/sse"},
	}
	entry := connectionToExportEntry(conn)
	if entry.Transport != "sse" {
		t.Fatalf("expected transport sse, got %s", entry.Transport)
	}
}

func TestEntryToUpsertRequest(t *testing.T) {
	entry := MCPServerEntry{
		Command: "npx",
		Args:    []string{"-y", "server"},
		Env:     map[string]string{"KEY": "val"},
	}
	req := entryToUpsertRequest("test-server", entry)
	if req.Name != "test-server" {
		t.Fatalf("expected name test-server, got %s", req.Name)
	}
	if req.Command != "npx" {
		t.Fatalf("expected command npx, got %s", req.Command)
	}
	if len(req.Args) != 2 {
		t.Fatalf("expected 2 args, got %v", req.Args)
	}
}

// sameOAuthResource gates whether Update may preserve a stored auth_type: the
// bearer token minted for one OAuth resource must never follow the connection
// to another. The resource is origin AND path (canonicalResourceURI) — a
// same-origin path edit still invalidates the token's audience. Table covers
// the exact boundary plus every fail-safe case.
func TestSameOAuthResource(t *testing.T) {
	tests := []struct {
		name      string
		existing  Connection
		newConfig map[string]any
		want      bool
	}{
		{
			name:      "identical URL preserves",
			existing:  Connection{Type: "sse", Config: map[string]any{"url": "https://mcp.example.com/mcp"}},
			newConfig: map[string]any{"url": "https://mcp.example.com/mcp"},
			want:      true,
		},
		{
			name:      "trailing slash variants preserve",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "https://mcp.example.com/mcp/"}},
			newConfig: map[string]any{"url": "https://mcp.example.com/mcp"},
			want:      true,
		},
		{
			name:      "case-insensitive scheme and host preserve",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "HTTPS://MCP.Example.COM/mcp"}},
			newConfig: map[string]any{"url": "https://mcp.example.com/mcp"},
			want:      true,
		},
		{
			name:      "same origin, different path clears",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "https://mcp.example.com/mcp"}},
			newConfig: map[string]any{"url": "https://mcp.example.com/v2/mcp"},
			want:      false,
		},
		{
			name:      "different host clears",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "https://mcp.example.com/mcp"}},
			newConfig: map[string]any{"url": "https://mcp.other.example.com/mcp"},
			want:      false,
		},
		{
			name:      "scheme downgrade clears",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "https://mcp.example.com/mcp"}},
			newConfig: map[string]any{"url": "http://mcp.example.com/mcp"},
			want:      false,
		},
		{
			name:      "explicit port difference clears",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "https://mcp.example.com/mcp"}},
			newConfig: map[string]any{"url": "https://mcp.example.com:8443/mcp"},
			want:      false,
		},
		{
			name:      "existing stdio never preserves",
			existing:  Connection{Type: "stdio", Config: map[string]any{"command": "npx"}},
			newConfig: map[string]any{"url": "https://mcp.example.com/mcp"},
			want:      false,
		},
		{
			name:      "new stdio config clears",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "https://mcp.example.com/mcp"}},
			newConfig: map[string]any{"command": "npx"},
			want:      false,
		},
		{
			name:      "unparseable stored URL fails safe",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "://not-a-url"}},
			newConfig: map[string]any{"url": "https://mcp.example.com/mcp"},
			want:      false,
		},
		{
			name:      "missing new URL fails safe",
			existing:  Connection{Type: "http", Config: map[string]any{"url": "https://mcp.example.com/mcp"}},
			newConfig: map[string]any{},
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameOAuthResource(tt.existing, tt.newConfig); got != tt.want {
				t.Fatalf("sameOAuthResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Update must keep auth_type and the stored credential on the SAME scope:
// editing within the OAuth resource preserves both; editing out of it (other
// origin, other path, or stdio) resets auth_type to "none" AND clears the
// grant. Otherwise GetStatus keeps reporting has_token=true while requests to
// the new target go out anonymous — the UI says Authorized and the wire says
// 401, with no path back to re-authorization.
func TestUpdateOAuthScopeConsistency(t *testing.T) {
	const (
		botID  = "11111111-1111-1111-1111-111111111111"
		connID = "22222222-2222-2222-2222-222222222222"
	)
	seed := func(t *testing.T, url, authType string) (*ConnectionService, *fakeOAuthQueries) {
		t.Helper()
		queries := newFakeOAuthQueries()
		connUUID := mustParseUUID(connID)
		cfg, err := json.Marshal(map[string]any{"url": url})
		if err != nil {
			t.Fatalf("marshal config: %v", err)
		}
		queries.connections[connUUID] = sqlc.McpConnection{
			ID:       connUUID,
			BotID:    mustParseUUID(botID),
			Name:     "server",
			Type:     "http",
			Config:   cfg,
			IsActive: true,
			AuthType: authType,
		}
		queries.byConn[connUUID] = sqlc.McpOauthToken{
			ID:           connUUID,
			ConnectionID: connUUID,
			AccessToken:  "stored-access-token",
			RefreshToken: "stored-refresh-token",
		}
		return NewConnectionService(slog.Default(), queries), queries
	}

	t.Run("in-scope edit preserves auth_type and grant", func(t *testing.T) {
		svc, queries := seed(t, "https://mcp.example.com/mcp/", "oauth")
		conn, err := svc.Update(context.Background(), botID, connID, UpsertRequest{
			Name: "server",
			URL:  "https://mcp.example.com/mcp",
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if conn.AuthType != "oauth" {
			t.Fatalf("auth_type = %q, want oauth preserved", conn.AuthType)
		}
		if got := queries.byConn[mustParseUUID(connID)].AccessToken; got != "stored-access-token" {
			t.Fatalf("access token = %q, want grant untouched", got)
		}
	})

	outOfScope := map[string]struct {
		name string
		req  UpsertRequest
	}{
		"path":   {"same origin, different path", UpsertRequest{Name: "server", URL: "https://mcp.example.com/v2/mcp"}},
		"origin": {"different origin", UpsertRequest{Name: "server", URL: "https://other.example.com/mcp"}},
		"stdio":  {"switch to stdio", UpsertRequest{Name: "server", Command: "npx"}},
	}
	for _, tt := range outOfScope {
		t.Run("out-of-scope edit resets auth_type and clears grant: "+tt.name, func(t *testing.T) {
			svc, queries := seed(t, "https://mcp.example.com/mcp", "oauth")
			conn, err := svc.Update(context.Background(), botID, connID, tt.req)
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if conn.AuthType != "none" {
				t.Fatalf("auth_type = %q, want none", conn.AuthType)
			}
			if got := queries.byConn[mustParseUUID(connID)].AccessToken; got != "" {
				t.Fatalf("access token = %q, want grant cleared", got)
			}
		})
	}

	t.Run("out-of-scope edit without oauth leaves grant untargeted", func(t *testing.T) {
		// auth_type "none" means no credential is in use, so there is nothing
		// to clear — the update must not fail and must not touch the row.
		svc, queries := seed(t, "https://mcp.example.com/mcp", "none")
		conn, err := svc.Update(context.Background(), botID, connID, UpsertRequest{
			Name: "server",
			URL:  "https://other.example.com/mcp",
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if conn.AuthType != "none" {
			t.Fatalf("auth_type = %q, want none", conn.AuthType)
		}
		if got := queries.byConn[mustParseUUID(connID)].AccessToken; got != "stored-access-token" {
			t.Fatalf("access token = %q, want row untouched", got)
		}
	})
}
