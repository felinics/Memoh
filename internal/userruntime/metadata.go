package userruntime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	RuntimeMetadataHeader   = "X-Memoh-Runtime-Metadata"
	maxMetadataBytes        = 8 * 1024
	CapabilityFS            = "fs"
	CapabilityExec          = "exec"
	CapabilityHostFS        = "host_fs"
	CapabilityACPCodex      = "acp_codex"
	CapabilityACPClaudeCode = "acp_claude_code"
)

var (
	ErrInvalidMetadata = errors.New("invalid runtime metadata")
	windowsDrivePath   = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

func ParseHandshakeMetadata(encoded string) (HandshakeInfo, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return HandshakeInfo{}, fmt.Errorf("%w: metadata header is required", ErrInvalidMetadata)
	}
	// A raw base64url payload for 8 KiB is under 11 KiB. Reject oversized
	// headers before allocating the decoded buffer.
	if len(encoded) > base64.RawURLEncoding.EncodedLen(maxMetadataBytes) {
		return HandshakeInfo{}, fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalidMetadata, maxMetadataBytes)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return HandshakeInfo{}, fmt.Errorf("%w: decode metadata: %w", ErrInvalidMetadata, err)
	}
	if len(raw) > maxMetadataBytes {
		return HandshakeInfo{}, fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalidMetadata, maxMetadataBytes)
	}

	var info HandshakeInfo
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&info); err != nil {
		return HandshakeInfo{}, fmt.Errorf("%w: decode JSON: %w", ErrInvalidMetadata, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return HandshakeInfo{}, err
	}
	if err := validateHandshakeInfo(&info); err != nil {
		return HandshakeInfo{}, err
	}
	return info, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: metadata contains multiple JSON values", ErrInvalidMetadata)
		}
		return fmt.Errorf("%w: decode trailing JSON: %w", ErrInvalidMetadata, err)
	}
	return nil
}

func validateHandshakeInfo(info *HandshakeInfo) error {
	if info == nil || info.Version != 1 {
		return fmt.Errorf("%w: unsupported metadata version", ErrInvalidMetadata)
	}
	info.Hostname = strings.TrimSpace(info.Hostname)
	info.OS = strings.ToLower(strings.TrimSpace(info.OS))
	info.Arch = strings.TrimSpace(info.Arch)
	info.ClientVersion = strings.TrimSpace(info.ClientVersion)
	info.WorkspaceBase = strings.TrimSpace(info.WorkspaceBase)

	for name, field := range map[string]struct {
		value string
		max   int
	}{
		"hostname":       {info.Hostname, 255},
		"os":             {info.OS, 16},
		"arch":           {info.Arch, 64},
		"client_version": {info.ClientVersion, 128},
		"workspace_base": {info.WorkspaceBase, 4096},
	} {
		if field.value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidMetadata, name)
		}
		if len(field.value) > field.max || strings.ContainsRune(field.value, '\x00') {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidMetadata, name)
		}
	}
	switch info.OS {
	case "darwin", "linux":
		if !strings.HasPrefix(info.WorkspaceBase, "/") {
			return fmt.Errorf("%w: workspace_base must be absolute", ErrInvalidMetadata)
		}
	case "win32":
		if !windowsDrivePath.MatchString(info.WorkspaceBase) && !strings.HasPrefix(info.WorkspaceBase, `\\`) {
			return fmt.Errorf("%w: workspace_base must be absolute", ErrInvalidMetadata)
		}
	default:
		return fmt.Errorf("%w: unsupported os %q", ErrInvalidMetadata, info.OS)
	}

	seen := make(map[string]struct{}, len(info.Capabilities))
	capabilities := make([]string, 0, len(info.Capabilities))
	for _, capability := range info.Capabilities {
		capability = strings.ToLower(strings.TrimSpace(capability))
		switch capability {
		case CapabilityFS, CapabilityExec, CapabilityHostFS, CapabilityACPCodex, CapabilityACPClaudeCode:
		default:
			// A newer client may declare capabilities this server predates.
			// Drop them instead of rejecting: routing only ever consults the
			// known set (supportsRemoteWorkspace verifies the required trio
			// independently), and a hard reject would brick updated clients
			// against older self-hosted servers.
			continue
		}
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	if len(capabilities) == 0 {
		return fmt.Errorf("%w: at least one supported capability is required", ErrInvalidMetadata)
	}
	sort.Strings(capabilities)
	info.Capabilities = capabilities
	return nil
}
