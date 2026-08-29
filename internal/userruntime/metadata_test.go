package userruntime

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func encodeMetadataForTest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestParseHandshakeMetadataUnicodeAndCanonicalCapabilities(t *testing.T) {
	t.Parallel()
	encoded := encodeMetadataForTest(t, map[string]any{
		"version":        1,
		"hostname":       "工作站.local",
		"os":             "darwin",
		"arch":           "arm64",
		"client_version": "1.2.3",
		"workspace_base": "/Users/张三/项目",
		"capabilities": []string{
			"exec",
			"fs",
			"host_fs",
			"exec",
			" ACP_CODEX ",
			"acp_codex",
			"ACP_CLAUDE_CODE",
			"acp_claude_code",
			"workspace_scope",
			"tunnel_v9",
		},
	})

	info, err := ParseHandshakeMetadata(encoded)
	if err != nil {
		t.Fatalf("ParseHandshakeMetadata() error = %v", err)
	}
	if info.Hostname != "工作站.local" || info.WorkspaceBase != "/Users/张三/项目" {
		t.Fatalf("unicode metadata changed: %#v", info)
	}
	if got := strings.Join(info.Capabilities, ","); got != "acp_claude_code,acp_codex,exec,fs,host_fs" {
		t.Fatalf("capabilities = %q, want canonical ACP and workspace capabilities (unknown capability must be dropped, not rejected)", got)
	}
}

func TestParseHandshakeMetadataRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	valid := map[string]any{
		"version":        1,
		"hostname":       "host.local",
		"os":             "linux",
		"arch":           "amd64",
		"client_version": "1.0.0",
		"workspace_base": "/home/alice/work",
		"capabilities":   []string{"fs"},
	}
	tests := map[string]func(map[string]any) string{
		"unknown field": func(value map[string]any) string {
			value["unexpected"] = true
			return encodeMetadataForTest(t, value)
		},
		"unsupported version": func(value map[string]any) string {
			value["version"] = 2
			return encodeMetadataForTest(t, value)
		},
		"relative path": func(value map[string]any) string {
			value["workspace_base"] = "relative/path"
			return encodeMetadataForTest(t, value)
		},
		"only unknown capabilities": func(value map[string]any) string {
			value["capabilities"] = []string{"browser"}
			return encodeMetadataForTest(t, value)
		},
		"missing hostname": func(value map[string]any) string {
			delete(value, "hostname")
			return encodeMetadataForTest(t, value)
		},
		"padded base64": func(value map[string]any) string {
			return encodeMetadataForTest(t, value) + "="
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			value := make(map[string]any, len(valid))
			for key, item := range valid {
				value[key] = item
			}
			_, err := ParseHandshakeMetadata(mutate(value))
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("error = %v, want ErrInvalidMetadata", err)
			}
		})
	}

	if _, err := ParseHandshakeMetadata(strings.Repeat("A", base64.RawURLEncoding.EncodedLen(maxMetadataBytes)+1)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized metadata error = %v", err)
	}
}

func TestParseHandshakeMetadataAcceptsWindowsAbsolutePath(t *testing.T) {
	t.Parallel()
	encoded := encodeMetadataForTest(t, map[string]any{
		"version":        1,
		"hostname":       "workstation",
		"os":             "win32",
		"arch":           "x64",
		"client_version": "1.0.0",
		"workspace_base": "C:\\Users\\alice\\work",
		"capabilities":   []string{"fs"},
	})
	if _, err := ParseHandshakeMetadata(encoded); err != nil {
		t.Fatalf("ParseHandshakeMetadata() error = %v", err)
	}
}
