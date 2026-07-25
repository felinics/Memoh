package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	skillset "github.com/memohai/memoh/domains/agent/extension/skills"
	"github.com/memohai/memoh/domains/agent/mcp"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

type BridgeProvider struct {
	Provider bridge.Provider
}

type Service struct {
	store        Store
	mcpService   *mcp.ConnectionService
	oauthService *mcp.OAuthService
	oauthClients *OAuthClientRegistry
	bridges      bridge.Provider
	logger       *slog.Logger
}

func NewService(log *slog.Logger, store Store, mcpService *mcp.ConnectionService, oauthService *mcp.OAuthService, oauthClients *OAuthClientRegistry, bridges BridgeProvider) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:        store,
		mcpService:   mcpService,
		oauthService: oauthService,
		oauthClients: oauthClients,
		bridges:      bridges.Provider,
		logger:       log.With(slog.String("service", "plugins")),
	}
}

func (s *Service) List(ctx context.Context, botID string) ([]Installation, error) {
	rows, err := s.store.ListInstallations(ctx, botID)
	if err != nil {
		return nil, err
	}
	items := make([]Installation, 0, len(rows))
	for _, row := range rows {
		item, err := s.normalizeInstallation(ctx, row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, botID, installationID string) (Installation, error) {
	row, err := s.getRow(ctx, botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	return s.normalizeInstallation(ctx, row)
}

func (s *Service) Install(ctx context.Context, botID string, req InstallRequest) (Installation, error) {
	if s.store == nil || s.mcpService == nil {
		return Installation{}, errors.New("plugin service is not configured")
	}
	manifest := normalizeManifest(req.Manifest)
	if manifest.ID == "" {
		return Installation{}, errors.New("plugin id is required")
	}
	if manifest.Name == "" {
		manifest.Name = manifest.ID
	}
	status := s.evaluateInitialStatus(manifest, req.Variables)
	enabled := status == StatusReady

	configPayload, err := encodeJSON(map[string]any{
		"variables": req.Variables,
	})
	if err != nil {
		return Installation{}, err
	}
	metadataPayload, err := encodeJSON(manifestMetadata(manifest))
	if err != nil {
		return Installation{}, err
	}
	manifestPayload, err := encodeJSON(manifest)
	if err != nil {
		return Installation{}, err
	}

	row, err := s.store.CreateInstallation(ctx, CreateInstallationInput{
		BotID:      botID,
		PluginID:   manifest.ID,
		PluginName: manifest.Name,
		Version:    manifest.Version,
		Status:     status,
		Enabled:    enabled,
		Config:     configPayload,
		Metadata:   metadataPayload,
		Manifest:   manifestPayload,
	})
	if err != nil {
		return Installation{}, err
	}

	installationID := row.ID
	if err := s.mcpService.DeleteByPlugin(ctx, botID, installationID); err != nil {
		return Installation{}, err
	}
	if err := s.store.DeleteResources(ctx, row.ID); err != nil {
		return Installation{}, err
	}

	for _, resource := range manifest.MCPs {
		authReq := manifestAuthForResource(manifest, resource)
		connReq := buildMCPConnectionRequest(manifest, resource, authReq, req.Variables)
		active := enabled && strings.TrimSpace(strings.ToLower(authReq.Type)) != "managed_oauth"
		connReq.Active = &active
		conn, err := s.mcpService.CreateManaged(ctx, botID, connReq, mcp.ManagedConnectionRequest{
			InstallationID: installationID,
			ResourceKey:    resource.Key,
			Visible:        strings.TrimSpace(strings.ToLower(resource.Visibility)) == "visible",
			Metadata:       mcpResourceMetadata(manifest, resource, authReq),
		})
		if err != nil {
			return Installation{}, fmt.Errorf("create plugin MCP resource %q: %w", resource.Key, err)
		}
		if err := s.store.UpsertResource(ctx, ResourceUpsert{
			InstallationID: row.ID,
			Type:           "mcp",
			Key:            resource.Key,
			ResourceID:     conn.ID,
			Status:         resourceStatus(status, authReq),
			Metadata:       mustJSON(mcpResourceMetadata(manifest, resource, authReq)),
		}); err != nil {
			return Installation{}, err
		}
	}

	for _, resource := range manifest.Skills {
		if err := s.store.UpsertResource(ctx, ResourceUpsert{
			InstallationID: row.ID,
			Type:           "skill",
			Key:            resource.Key,
			ResourceID:     resource.Path,
			Status:         "bundled",
			Metadata:       mustJSON(map[string]any{"name": resource.Name}),
		}); err != nil {
			return Installation{}, err
		}
	}
	for _, skill := range manifest.BundledSkills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			name = strings.TrimSpace(skill.ID)
		}
		key := sanitizeID(name)
		if key == "" {
			continue
		}
		if err := s.store.UpsertResource(ctx, ResourceUpsert{
			InstallationID: row.ID,
			Type:           "skill",
			Key:            key,
			ResourceID:     path.Join(skillset.ManagedDir(), name, "SKILL.md"),
			Status:         "bundled",
			Metadata:       mustJSON(map[string]any{"name": name, "skill_id": skill.ID}),
		}); err != nil {
			return Installation{}, err
		}
	}
	if err := s.installBundledSkills(ctx, botID, row, manifest); err != nil {
		return Installation{}, err
	}

	return s.normalizeInstallation(ctx, row)
}

func (s *Service) SetEnabled(ctx context.Context, botID, installationID string, enabled bool) (Installation, error) {
	row, err := s.getRow(ctx, botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	if !enabled {
		if err := s.mcpService.SetPluginConnectionsActive(ctx, botID, installationID, false); err != nil {
			return Installation{}, err
		}
		updated, err := s.updateStatus(ctx, botID, installationID, StatusDisabled, false)
		if err != nil {
			return Installation{}, err
		}
		return s.normalizeInstallation(ctx, updated)
	}

	manifest, err := decodeManifest(row.Manifest)
	if err != nil {
		return Installation{}, err
	}
	if row.Status == StatusUninstalled {
		return Installation{}, errors.New("plugin is uninstalled")
	}
	variables, configErr := variablesFromConfig(row.Config)
	if configErr != nil {
		return Installation{}, configErr
	}
	status := s.evaluateInitialStatus(manifest, variables)
	if status == StatusNeedsAuth {
		status, err = s.refreshOAuthStatus(ctx, botID, row, manifest)
		if err != nil {
			return Installation{}, err
		}
	}
	if status != StatusReady {
		return Installation{}, fmt.Errorf("plugin is not ready: %s", status)
	}
	if err := s.mcpService.SetPluginConnectionsActive(ctx, botID, installationID, true); err != nil {
		return Installation{}, err
	}
	updated, err := s.updateStatus(ctx, botID, installationID, StatusReady, true)
	if err != nil {
		return Installation{}, err
	}
	return s.normalizeInstallation(ctx, updated)
}

func (s *Service) Uninstall(ctx context.Context, botID, installationID string) (Installation, error) {
	row, err := s.getRow(ctx, botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	if err := s.mcpService.DeleteByPlugin(ctx, botID, installationID); err != nil {
		return Installation{}, err
	}
	if err := s.uninstallBundledSkills(ctx, botID, row); err != nil {
		return Installation{}, err
	}
	if err := s.store.DeleteResources(ctx, row.ID); err != nil {
		return Installation{}, err
	}
	updated, err := s.updateStatus(ctx, botID, installationID, StatusUninstalled, false)
	if err != nil {
		return Installation{}, err
	}
	return s.normalizeInstallation(ctx, updated)
}

func (s *Service) Purge(ctx context.Context, botID, installationID string) error {
	row, err := s.getRow(ctx, botID, installationID)
	if err != nil {
		return err
	}
	if err := s.mcpService.DeleteByPlugin(ctx, botID, installationID); err != nil {
		return err
	}
	if err := s.uninstallBundledSkills(ctx, botID, row); err != nil {
		return err
	}
	if err := s.store.DeleteResources(ctx, row.ID); err != nil {
		return err
	}
	return s.store.DeleteInstallation(ctx, botID, installationID)
}

func (s *Service) StartOAuth(ctx context.Context, botID, installationID, callbackURL string) (*mcp.AuthorizeResult, error) {
	row, err := s.getRow(ctx, botID, installationID)
	if err != nil {
		return nil, err
	}
	manifest, err := decodeManifest(row.Manifest)
	if err != nil {
		return nil, err
	}
	resources, err := s.store.ListResources(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	resourceByKey := map[string]string{}
	for _, resource := range resources {
		if strings.TrimSpace(resource.Type) == "mcp" {
			resourceByKey[resource.Key] = resource.ResourceID
		}
	}
	for _, resource := range manifest.MCPs {
		authReq := manifestAuthForResource(manifest, resource)
		if strings.TrimSpace(strings.ToLower(authReq.Type)) != "managed_oauth" {
			continue
		}
		var client OAuthClient
		var ok bool
		if strings.TrimSpace(authReq.ClientRef) != "" {
			client, ok = s.oauthClients.Get(authReq.ClientRef)
		}
		if strings.TrimSpace(authReq.ClientRef) != "" && (!ok || strings.TrimSpace(client.ClientID) == "") {
			return nil, fmt.Errorf("OAuth client %q is not configured", authReq.ClientRef)
		}
		connID := strings.TrimSpace(resourceByKey[resource.Key])
		if connID == "" {
			return nil, fmt.Errorf("OAuth MCP resource %q is not installed", resource.Key)
		}
		if strings.TrimSpace(callbackURL) == "" {
			callbackURL = client.RedirectURI
		}
		if strings.TrimSpace(client.AuthorizationEndpoint) != "" && strings.TrimSpace(client.TokenEndpoint) != "" {
			if err := s.oauthService.SaveDiscovery(ctx, connID, &mcp.DiscoveryResult{
				AuthorizationServerURL: authorizationServerFromEndpoint(client.AuthorizationEndpoint),
				AuthorizationEndpoint:  client.AuthorizationEndpoint,
				TokenEndpoint:          client.TokenEndpoint,
				ScopesSupported:        authReq.Scopes,
				ResourceURI:            strings.TrimSpace(resource.URL),
			}); err != nil {
				return nil, err
			}
		} else {
			result, err := s.oauthService.Discover(ctx, resource.URL)
			if err != nil {
				return nil, err
			}
			applyRequestedScopes(result, authReq.Scopes)
			if err := s.oauthService.SaveDiscovery(ctx, connID, result); err != nil {
				return nil, err
			}
		}
		return s.oauthService.StartAuthorization(ctx, connID, client.ClientID, client.ClientSecret, callbackURL)
	}
	return nil, errors.New("plugin does not declare a managed OAuth MCP resource")
}

func (s *Service) RefreshOAuthStatus(ctx context.Context, botID, installationID string) (Installation, error) {
	row, err := s.getRow(ctx, botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	manifest, err := decodeManifest(row.Manifest)
	if err != nil {
		return Installation{}, err
	}
	status, err := s.refreshOAuthStatus(ctx, botID, row, manifest)
	if err != nil {
		return Installation{}, err
	}
	enabled := status == StatusReady
	if err := s.mcpService.SetPluginConnectionsActive(ctx, botID, installationID, enabled); err != nil {
		return Installation{}, err
	}
	updated, err := s.updateStatus(ctx, botID, installationID, status, enabled)
	if err != nil {
		return Installation{}, err
	}
	return s.normalizeInstallation(ctx, updated)
}

func (s *Service) getRow(ctx context.Context, botID, installationID string) (InstallationRecord, error) {
	return s.store.FindInstallation(ctx, botID, installationID)
}

func (s *Service) updateStatus(ctx context.Context, botID, installationID, status string, enabled bool) (InstallationRecord, error) {
	return s.store.UpdateInstallationStatus(ctx, InstallationStatusUpdate{
		BotID:          botID,
		InstallationID: installationID,
		Status:         status,
		Enabled:        enabled,
	})
}

func (s *Service) normalizeInstallation(ctx context.Context, row InstallationRecord) (Installation, error) {
	manifest, err := decodeManifest(row.Manifest)
	if err != nil {
		return Installation{}, err
	}
	metadata, err := decodeJSONMap(row.Metadata)
	if err != nil {
		return Installation{}, err
	}
	config, err := decodeJSONMap(row.Config)
	if err != nil {
		return Installation{}, err
	}
	resources, err := s.store.ListResources(ctx, row.ID)
	if err != nil {
		return Installation{}, err
	}
	outResources := make([]Resource, 0, len(resources))
	for _, resource := range resources {
		item, err := normalizeResource(resource)
		if err != nil {
			return Installation{}, err
		}
		outResources = append(outResources, item)
	}
	return Installation{
		ID:          row.ID,
		BotID:       row.BotID,
		PluginID:    row.PluginID,
		PluginName:  row.PluginName,
		Version:     row.Version,
		Status:      row.Status,
		Enabled:     row.Enabled,
		Config:      redactConfig(manifest, config),
		Metadata:    metadata,
		Manifest:    manifest,
		Resources:   outResources,
		InstalledAt: row.InstalledAt,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

func (s *Service) installBundledSkills(ctx context.Context, botID string, row InstallationRecord, manifest Manifest) error {
	if s.bridges == nil || len(manifest.BundledSkills) == 0 {
		return nil
	}
	client, err := s.bridges.MCPClient(ctx, botID)
	if err != nil {
		return fmt.Errorf("install plugin skills: workspace is not reachable: %w", err)
	}
	for _, skill := range manifest.BundledSkills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			name = strings.TrimSpace(skill.ID)
		}
		if !skillset.IsValidName(name) {
			return fmt.Errorf("plugin skill %q has an invalid name", name)
		}
		raw := pluginSkillRaw(skill, name, row)
		parsed := skillset.ParseFile(raw, name)
		dirPath, err := skillset.ManagedSkillDirForName(parsed.Name)
		if err != nil {
			return fmt.Errorf("plugin skill %q has an invalid name", parsed.Name)
		}
		if err := client.Mkdir(ctx, dirPath); err != nil {
			return fmt.Errorf("create plugin skill %q directory: %w", parsed.Name, err)
		}
		if err := client.WriteFile(ctx, path.Join(dirPath, "SKILL.md"), []byte(raw)); err != nil {
			return fmt.Errorf("write plugin skill %q: %w", parsed.Name, err)
		}
		owner, err := encodeJSON(pluginSkillOwner(row, manifest, skill, parsed.Name))
		if err != nil {
			return err
		}
		if err := client.WriteFile(ctx, path.Join(dirPath, ".memoh-plugin-owner.json"), owner); err != nil {
			return fmt.Errorf("write plugin skill %q owner marker: %w", parsed.Name, err)
		}
	}
	return nil
}

func (s *Service) uninstallBundledSkills(ctx context.Context, botID string, row InstallationRecord) error {
	if s.bridges == nil {
		return nil
	}
	manifest, err := decodeManifest(row.Manifest)
	if err != nil {
		return err
	}
	if len(manifest.BundledSkills) == 0 {
		return nil
	}
	client, err := s.bridges.MCPClient(ctx, botID)
	if err != nil {
		return fmt.Errorf("uninstall plugin skills: workspace is not reachable: %w", err)
	}
	for _, skill := range manifest.BundledSkills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			name = strings.TrimSpace(skill.ID)
		}
		if !skillset.IsValidName(name) {
			continue
		}
		dirPath, err := skillset.ManagedSkillDirForName(name)
		if err != nil {
			continue
		}
		if !canDeletePluginSkill(ctx, client, dirPath, row.ID) {
			continue
		}
		if err := client.DeleteFile(ctx, dirPath, true); err != nil {
			return fmt.Errorf("delete plugin skill %q: %w", name, err)
		}
	}
	return nil
}

type skillFileClient interface {
	ReadRaw(ctx context.Context, path string) (io.ReadCloser, error)
}

func canDeletePluginSkill(ctx context.Context, client skillFileClient, dirPath, installationID string) bool {
	rc, err := client.ReadRaw(ctx, path.Join(dirPath, ".memoh-plugin-owner.json"))
	if err != nil {
		return false
	}
	defer func() { _ = rc.Close() }()
	var owner struct {
		InstallationID string `json:"installation_id"`
	}
	if err := json.NewDecoder(rc).Decode(&owner); err != nil {
		return false
	}
	return strings.TrimSpace(owner.InstallationID) == installationID
}

func pluginSkillRaw(skill SkillEntry, name string, row InstallationRecord) string {
	metadata := normalizeMetadataMap(skill.Metadata)
	metadata["managed_by_plugin"] = map[string]any{
		"installation_id": row.ID,
		"plugin_id":       row.PluginID,
		"plugin_name":     row.PluginName,
	}
	frontmatter := map[string]any{
		"name":        name,
		"description": strings.TrimSpace(skill.Description),
		"metadata":    metadata,
	}
	payload, _ := encodeYAML(frontmatter)
	body := strings.TrimSpace(skill.Content)
	if body == "" {
		body = "# " + name
	}
	return "---\n" + strings.TrimSpace(string(payload)) + "\n---\n\n" + body + "\n"
}

func pluginSkillOwner(row InstallationRecord, manifest Manifest, skill SkillEntry, name string) map[string]any {
	return map[string]any{
		"installation_id": row.ID,
		"plugin_id":       manifest.ID,
		"skill_id":        skill.ID,
		"skill_name":      name,
	}
}

func normalizeMetadataMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = item
	}
	return out
}

func (s *Service) evaluateInitialStatus(manifest Manifest, variables map[string]string) string {
	status := StatusReady
	for _, resource := range manifest.MCPs {
		authReq := manifestAuthForResource(manifest, resource)
		switch strings.TrimSpace(strings.ToLower(authReq.Type)) {
		case "managed_oauth":
			if strings.TrimSpace(authReq.ClientRef) != "" && !s.oauthClients.HasUsableClient(authReq.ClientRef) {
				return StatusAdminRequired
			}
			status = StatusNeedsAuth
		case "user_secret":
			if missingRequiredVariables(manifest, resource, authReq, variables) {
				return StatusNeedsConfig
			}
		}
		if missingResourceConfig(manifest, resource, variables) {
			return StatusNeedsConfig
		}
	}
	return status
}

func (s *Service) refreshOAuthStatus(ctx context.Context, botID string, row InstallationRecord, manifest Manifest) (string, error) {
	resources, err := s.store.ListResources(ctx, row.ID)
	if err != nil {
		return "", err
	}
	resourceByKey := map[string]string{}
	for _, resource := range resources {
		if strings.TrimSpace(resource.Type) == "mcp" {
			resourceByKey[resource.Key] = resource.ResourceID
		}
	}
	hasManagedOAuth := false
	for _, resource := range manifest.MCPs {
		authReq := manifestAuthForResource(manifest, resource)
		if strings.TrimSpace(strings.ToLower(authReq.Type)) != "managed_oauth" {
			continue
		}
		hasManagedOAuth = true
		if strings.TrimSpace(authReq.ClientRef) != "" && !s.oauthClients.HasUsableClient(authReq.ClientRef) {
			return StatusAdminRequired, nil
		}
		connID := strings.TrimSpace(resourceByKey[resource.Key])
		if connID == "" {
			return StatusNeedsAuth, nil
		}
		status, err := s.oauthService.GetStatus(ctx, connID)
		if err != nil {
			s.logger.WarnContext(ctx, "failed to get plugin OAuth status", slog.String("bot_id", botID), slog.String("installation_id", row.ID), slog.Any("error", err))
			return StatusNeedsAuth, nil
		}
		if !status.HasToken || status.Expired {
			return StatusNeedsAuth, nil
		}
	}
	if hasManagedOAuth {
		return StatusReady, nil
	}
	return row.Status, nil
}

func normalizeResource(row ResourceRecord) (Resource, error) {
	metadata, err := decodeJSONMap(row.Metadata)
	if err != nil {
		return Resource{}, err
	}
	return Resource{
		ID:         row.ID,
		Type:       row.Type,
		Key:        row.Key,
		ResourceID: row.ResourceID,
		Status:     row.Status,
		Metadata:   metadata,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}, nil
}

func normalizeManifest(manifest Manifest) Manifest {
	manifest.ID = sanitizeID(manifest.ID)
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Version = strings.TrimSpace(manifest.Version)
	if manifest.Version == "" {
		manifest.Version = "0.1.0"
	}
	if manifest.SchemaVersion == "" {
		manifest.SchemaVersion = "1"
	}
	manifest.Install = normalizeInstallCommands(manifest.Install)
	for i := range manifest.MCPs {
		manifest.MCPs[i].Key = sanitizeID(manifest.MCPs[i].Key)
		if manifest.MCPs[i].Key == "" {
			manifest.MCPs[i].Key = "mcp"
		}
		if manifest.MCPs[i].Name == "" {
			manifest.MCPs[i].Name = manifest.MCPs[i].DisplayName
		}
	}
	for i := range manifest.Skills {
		manifest.Skills[i].Key = sanitizeID(manifest.Skills[i].Key)
		if manifest.Skills[i].Key == "" {
			manifest.Skills[i].Key = sanitizeID(manifest.Skills[i].Name)
		}
	}
	return manifest
}

func NormalizeManifest(manifest Manifest) Manifest {
	return normalizeManifest(manifest)
}

func normalizeInstallCommands(commands []string) InstallCommands {
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		out = append(out, command)
	}
	return InstallCommands(out)
}

func manifestAuthForResource(manifest Manifest, resource MCPResource) AuthRequirement {
	authRef := strings.TrimSpace(resource.AuthRef)
	if authRef != "" {
		for _, req := range manifest.AuthRequirements {
			if strings.TrimSpace(req.Key) == authRef {
				return req
			}
		}
	}
	if len(manifest.AuthRequirements) == 1 {
		return manifest.AuthRequirements[0]
	}
	return AuthRequirement{Key: "anonymous", Type: "none"}
}

func buildMCPConnectionRequest(manifest Manifest, resource MCPResource, authReq AuthRequirement, variables map[string]string) mcp.UpsertRequest {
	resolved := resolveVariables(manifest, resource, variables)
	headers := map[string]string{}
	for _, header := range resource.Headers {
		value := resolveConfigValue(header, resolved)
		if value != "" {
			headers[header.Key] = expandTemplateVars(value, resolved)
		}
	}
	env := map[string]string{}
	for _, item := range resource.Env {
		value := resolveConfigValue(item, resolved)
		if value != "" {
			env[item.Key] = expandTemplateVars(value, resolved)
		}
	}
	args := make([]string, 0, len(resource.Args))
	for _, arg := range resource.Args {
		args = append(args, expandTemplateVars(arg, resolved))
	}
	authType := "none"
	if strings.TrimSpace(strings.ToLower(authReq.Type)) == "managed_oauth" {
		authType = "oauth"
	}
	return mcp.UpsertRequest{
		Name:      stableResourceName(manifest.ID, resource.Key),
		Command:   expandTemplateVars(resource.Command, resolved),
		Args:      args,
		Env:       env,
		Cwd:       expandTemplateVars(resource.Cwd, resolved),
		URL:       expandTemplateVars(resource.URL, resolved),
		Headers:   headers,
		Transport: resource.Transport,
		AuthType:  authType,
	}
}

func resolveVariables(manifest Manifest, resource MCPResource, variables map[string]string) map[string]string {
	resolved := map[string]string{}
	for key, value := range variables {
		key = strings.TrimSpace(key)
		if key != "" {
			resolved[key] = value
		}
	}
	for _, item := range manifest.Variables {
		seedDefaultVariable(resolved, item)
	}
	for _, item := range resource.Env {
		seedDefaultVariable(resolved, item)
	}
	for _, item := range resource.Headers {
		seedDefaultVariable(resolved, item)
	}
	return resolved
}

func seedDefaultVariable(resolved map[string]string, item ConfigVar) {
	key := strings.TrimSpace(item.Key)
	if key == "" {
		return
	}
	if _, ok := resolved[key]; ok {
		return
	}
	value := strings.TrimSpace(item.DefaultValue)
	if value == "" {
		return
	}
	value = expandTemplateVars(value, resolved)
	if hasUnresolvedTemplateVars(value) {
		return
	}
	resolved[key] = value
}

func resolveConfigValue(item ConfigVar, variables map[string]string) string {
	key := strings.TrimSpace(item.Key)
	if key == "" {
		return ""
	}
	if value, ok := variables[key]; ok {
		return value
	}
	value := strings.TrimSpace(item.DefaultValue)
	if value == "" {
		return ""
	}
	value = expandTemplateVars(value, variables)
	if hasUnresolvedTemplateVars(value) {
		return ""
	}
	return value
}

func missingRequiredVariables(manifest Manifest, resource MCPResource, authReq AuthRequirement, variables map[string]string) bool {
	resolved := resolveVariables(manifest, resource, variables)
	for _, key := range authReq.Variables {
		if strings.TrimSpace(resolved[strings.TrimSpace(key)]) == "" {
			return true
		}
	}
	return false
}

func missingResourceConfig(manifest Manifest, resource MCPResource, variables map[string]string) bool {
	resolved := resolveVariables(manifest, resource, variables)
	for _, item := range append(resource.Env, resource.Headers...) {
		if !item.Required {
			continue
		}
		if strings.TrimSpace(resolveConfigValue(item, resolved)) == "" {
			return true
		}
	}
	return false
}

func resourceStatus(installationStatus string, authReq AuthRequirement) string {
	if strings.TrimSpace(strings.ToLower(authReq.Type)) == "managed_oauth" && installationStatus == StatusNeedsAuth {
		return StatusNeedsAuth
	}
	return installationStatus
}

func applyRequestedScopes(result *mcp.DiscoveryResult, scopes []string) {
	if result == nil || len(scopes) == 0 {
		return
	}
	result.ScopesSupported = scopes
}

func manifestMetadata(manifest Manifest) map[string]any {
	return map[string]any{
		"icon":         manifest.Icon,
		"tags":         manifest.Tags,
		"capabilities": manifest.Capabilities,
		"homepage":     manifest.Homepage,
	}
}

func mcpResourceMetadata(manifest Manifest, resource MCPResource, authReq AuthRequirement) map[string]any {
	return map[string]any{
		"plugin_id":    manifest.ID,
		"plugin_name":  manifest.Name,
		"plugin_icon":  manifest.Icon,
		"resource_key": resource.Key,
		"display_name": resource.DisplayName,
		"capabilities": resource.Capabilities,
		"auth_type":    authReq.Type,
		"client_ref":   authReq.ClientRef,
		"tool_prefix":  stableResourceName(manifest.ID, resource.Key),
		"visibility":   resource.Visibility,
	}
}

func redactConfig(manifest Manifest, config map[string]any) map[string]any {
	rawVariables, _ := config["variables"].(map[string]any)
	variableStatus := map[string]any{}
	for _, item := range manifest.Variables {
		if item.Key == "" {
			continue
		}
		_, configured := rawVariables[item.Key]
		variableStatus[item.Key] = map[string]bool{"configured": configured}
	}
	return map[string]any{"variables": variableStatus}
}

func variablesFromConfig(raw []byte) (map[string]string, error) {
	config, err := decodeJSONMap(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	switch variables := config["variables"].(type) {
	case map[string]any:
		for key, value := range variables {
			key = strings.TrimSpace(key)
			if key == "" || value == nil {
				continue
			}
			switch typed := value.(type) {
			case string:
				out[key] = typed
			default:
				out[key] = fmt.Sprint(typed)
			}
		}
	case map[string]string:
		for key, value := range variables {
			key = strings.TrimSpace(key)
			if key != "" {
				out[key] = value
			}
		}
	}
	return out, nil
}

func encodeJSON(value any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	return json.Marshal(value)
}

func encodeYAML(value any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	return yaml.Marshal(value)
}

func mustJSON(value any) []byte {
	payload, _ := encodeJSON(value)
	return payload
}

func decodeJSONMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func decodeManifest(raw []byte) (Manifest, error) {
	if len(raw) == 0 {
		return Manifest{}, nil
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	return normalizeManifest(manifest), nil
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	return idPattern.ReplaceAllString(value, "_")
}

func stableResourceName(pluginID, resourceKey string) string {
	name := sanitizeID(pluginID + "_" + resourceKey)
	if name == "" {
		return "plugin_mcp"
	}
	return name
}

func expandTemplateVars(value string, vars map[string]string) string {
	if value == "" || len(vars) == 0 {
		return value
	}
	return templateVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		key := match[2 : len(match)-1]
		if val, ok := vars[key]; ok {
			return val
		}
		return match
	})
}

func hasUnresolvedTemplateVars(value string) bool {
	return templateVarPattern.MatchString(value)
}

func authorizationServerFromEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	idx := strings.Index(endpoint, "/oauth")
	if idx > len("https://") {
		return endpoint[:idx]
	}
	return endpoint
}

var (
	idPattern          = regexp.MustCompile(`[^a-z0-9_-]+`)
	templateVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)
