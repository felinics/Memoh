package plugins

import (
	"context"
	"io"
	"path"
	"strings"
	"testing"

	skillset "github.com/memohai/memoh/domains/agent/extension/skills"
	"github.com/memohai/memoh/domains/agent/mcp"
)

func TestMissingRequiredVariablesTreatsSelfTemplateDefaultAsMissing(t *testing.T) {
	manifest := Manifest{
		Variables: []ConfigVar{
			{Key: "NOTION_TOKEN", Required: true, Secret: true},
		},
	}
	resource := MCPResource{
		Env: []ConfigVar{
			{Key: "NOTION_TOKEN", DefaultValue: "${NOTION_TOKEN}", Required: true, Secret: true},
		},
	}
	authReq := AuthRequirement{
		Type:      "user_secret",
		Variables: []string{"NOTION_TOKEN"},
	}

	if !missingRequiredVariables(manifest, resource, authReq, map[string]string{}) {
		t.Fatal("expected missing user secret when the only value is a self-template default")
	}
	if !missingResourceConfig(manifest, resource, map[string]string{}) {
		t.Fatal("expected missing resource config when the only value is a self-template default")
	}
}

func TestResolveConfigValueExpandsTemplateWhenVariableIsProvided(t *testing.T) {
	manifest := Manifest{
		Variables: []ConfigVar{
			{Key: "TOKEN", Required: true, Secret: true},
		},
	}
	resource := MCPResource{
		Headers: []ConfigVar{
			{Key: "Authorization", DefaultValue: "Bearer ${TOKEN}", Required: true, Secret: true},
		},
	}
	resolved := resolveVariables(manifest, resource, map[string]string{"TOKEN": "abc123"})

	if got := resolveConfigValue(resource.Headers[0], resolved); got != "Bearer abc123" {
		t.Fatalf("expected expanded authorization header, got %q", got)
	}
	if missingResourceConfig(manifest, resource, map[string]string{"TOKEN": "abc123"}) {
		t.Fatal("expected resource config to be present when template variable is provided")
	}
}

func TestResolveConfigValueDropsUnresolvedTemplate(t *testing.T) {
	item := ConfigVar{Key: "Authorization", DefaultValue: "Bearer ${TOKEN}", Required: true}

	if got := resolveConfigValue(item, map[string]string{}); got != "" {
		t.Fatalf("expected unresolved template to resolve to empty string, got %q", got)
	}
}

func TestVariablesFromConfigRestoresSavedInstallVariables(t *testing.T) {
	variables, err := variablesFromConfig([]byte(`{"variables":{"TOKEN":"abc123","COUNT":7,"EMPTY":null}}`))
	if err != nil {
		t.Fatalf("variablesFromConfig: %v", err)
	}
	if variables["TOKEN"] != "abc123" {
		t.Fatalf("TOKEN = %q, want abc123", variables["TOKEN"])
	}
	if variables["COUNT"] != "7" {
		t.Fatalf("COUNT = %q, want 7", variables["COUNT"])
	}
	if _, ok := variables["EMPTY"]; ok {
		t.Fatal("nil variables should be omitted")
	}
}

func TestManifestScopesOverrideDiscoveredScopes(t *testing.T) {
	result := &mcp.DiscoveryResult{ScopesSupported: []string{"repo", "read:org", "workflow"}}
	applyRequestedScopes(result, []string{"repo", "read:org"})

	if len(result.ScopesSupported) != 2 || result.ScopesSupported[0] != "repo" || result.ScopesSupported[1] != "read:org" {
		t.Fatalf("scopes = %#v, want manifest scopes", result.ScopesSupported)
	}
}

func TestPluginSkillRawAddsFrontmatterAndOwnership(t *testing.T) {
	row := InstallationRecord{
		ID:         "00000000-0000-0000-0000-000000000001",
		PluginID:   "github",
		PluginName: "GitHub",
	}
	raw := pluginSkillRaw(SkillEntry{
		ID:          "github",
		Name:        "github",
		Description: "Use GitHub.",
		Content:     "# GitHub\n\nUse the connected app.",
	}, "github", row)

	parsed := skillset.ParseFile(raw, "")
	if parsed.Name != "github" {
		t.Fatalf("parsed name = %q, want github", parsed.Name)
	}
	if parsed.Description != "Use GitHub." {
		t.Fatalf("parsed description = %q", parsed.Description)
	}
	owner, ok := parsed.Metadata["managed_by_plugin"].(map[string]any)
	if !ok {
		t.Fatalf("managed_by_plugin metadata missing: %#v", parsed.Metadata)
	}
	if owner["plugin_id"] != "github" {
		t.Fatalf("plugin_id = %#v, want github", owner["plugin_id"])
	}
}

func TestCanDeletePluginSkillRequiresMatchingOwnerMarker(t *testing.T) {
	dir := path.Join(skillset.ManagedDir(), "github")
	client := &pluginSkillFileClient{
		files: map[string]string{
			path.Join(dir, ".memoh-plugin-owner.json"): `{"installation_id":"install-1"}`,
		},
	}

	if !canDeletePluginSkill(context.Background(), client, dir, "install-1") {
		t.Fatal("expected matching owner marker to allow deletion")
	}
	if canDeletePluginSkill(context.Background(), client, dir, "install-2") {
		t.Fatal("expected mismatched owner marker to block deletion")
	}
}

type pluginSkillFileClient struct {
	files map[string]string
}

func (c *pluginSkillFileClient) ReadRaw(_ context.Context, filePath string) (io.ReadCloser, error) {
	raw, ok := c.files[filePath]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	return io.NopCloser(strings.NewReader(raw)), nil
}
