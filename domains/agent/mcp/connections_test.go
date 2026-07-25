package mcp

import (
	"testing"
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
