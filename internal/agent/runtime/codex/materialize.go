package codex

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/BurntSushi/toml"
)

const codexManagedConfigHeader = "# Managed by Memoh. Changes are overwritten when this Agent starts.\n"

type codexConfigWriter interface {
	WriteFile(ctx context.Context, path string, content []byte) error
}

type codexManagedConfig struct {
	OpenAIBaseURL string `toml:"openai_base_url,omitempty"`
}

// materializeCodexConfig projects the database-owned Bot Agent configuration
// into the user-level file Codex reads from CODEX_HOME. It always rewrites the
// managed file, including an empty configuration, so clearing a Base URL or
// switching to ChatGPT auth cannot leave a stale relay active.
func materializeCodexConfig(ctx context.Context, writer codexConfigWriter, home string, cfg Config) error {
	payload, err := renderCodexConfig(cfg)
	if err != nil {
		return fmt.Errorf("render codex config: %w", err)
	}
	configPath := path.Join(home, "config.toml")
	if err := writer.WriteFile(ctx, configPath, payload); err != nil {
		return fmt.Errorf("write codex config %s: %w", configPath, err)
	}
	return nil
}

func renderCodexConfig(cfg Config) ([]byte, error) {
	managed := codexManagedConfig{}
	if cfg.Auth == AuthAPIKey {
		managed.OpenAIBaseURL = strings.TrimSpace(cfg.BaseURL)
	}

	var payload bytes.Buffer
	payload.WriteString(codexManagedConfigHeader)
	if err := toml.NewEncoder(&payload).Encode(managed); err != nil {
		return nil, err
	}
	return payload.Bytes(), nil
}
