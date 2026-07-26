// Package adapter maps other domains' contracts onto the Channel ports.
//
// These are translations, not wiring: they map records, resolve settings, and
// convert one domain's vocabulary into another's. They live in a named package
// so domains/channel/assembly stays what its name promises — composition.
package adapter

import (
	"context"
	"errors"
	stdpath "path"
	"strings"

	sessionpkg "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/agent/command"
	"github.com/memohai/memoh/domains/api/bot"
	runtimehttp "github.com/memohai/memoh/domains/api/http/runtime"
	"github.com/memohai/memoh/domains/api/bot/setting"
	emailpkg "github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/channel/inbound"
	"github.com/memohai/memoh/domains/channel/route"
	"github.com/memohai/memoh/domains/iam/account"
	audiopkg "github.com/memohai/memoh/domains/model/audio"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
	"github.com/memohai/memoh/internal/oauth"
)

type BotPresence struct {
	bots bot.Persistence
}

func (p BotPresence) EnsureBot(ctx context.Context, botID string) error {
	_, err := p.bots.GetBotByID(ctx, botID)
	return err
}

func (p BotPresence) TouchBot(ctx context.Context, botID string) error {
	return p.bots.TouchBot(ctx, botID)
}

type SessionEnsurer struct {
	coordinator *route.ThreadCoordinator
}

func (a *SessionEnsurer) EnsureActiveSession(ctx context.Context, botID, routeID, channelType string) (inbound.SessionResult, error) {
	sess, err := a.coordinator.EnsureActive(ctx, botID, routeID, channelType)
	if err != nil {
		return inbound.SessionResult{}, err
	}
	return inboundSessionResult(sess), nil
}

func (a *SessionEnsurer) GetActiveSession(ctx context.Context, routeID string) (inbound.SessionResult, error) {
	sess, err := a.coordinator.GetActive(ctx, routeID)
	if err != nil {
		return inbound.SessionResult{}, err
	}
	return inboundSessionResult(sess), nil
}

func (a *SessionEnsurer) CreateNewSession(ctx context.Context, botID, routeID, channelType string, spec inbound.NewSessionSpec) (inbound.SessionResult, error) {
	createdByUserID := newSessionCreatedByUserID(spec)
	sess, err := a.coordinator.CreateNew(ctx, sessionpkg.CreateInput{
		BotID:           botID,
		RouteID:         routeID,
		ChannelType:     channelType,
		Type:            spec.Type,
		SessionMode:     spec.Mode,
		RuntimeType:     spec.Runtime,
		Metadata:        spec.Metadata,
		RuntimeMetadata: spec.Metadata,
		Title:           spec.Title,
		CreatedByUserID: createdByUserID,
	})
	if err != nil {
		return inbound.SessionResult{}, err
	}
	return inboundSessionResult(sess), nil
}

func newSessionCreatedByUserID(spec inbound.NewSessionSpec) string {
	if userID := strings.TrimSpace(spec.CreatedByUserID); userID != "" {
		return userID
	}
	return strings.TrimSpace(spec.RuntimeOwnerAccountID)
}

func inboundSessionResult(sess sessionpkg.Thread) inbound.SessionResult {
	return inbound.SessionResult{
		ID:                    sess.ID,
		Type:                  sess.Type,
		Mode:                  sess.SessionMode,
		Runtime:               sess.RuntimeType,
		RuntimeOwnerAccountID: sessionRuntimeOwnerAccountID(sess),
	}
}

func sessionRuntimeOwnerAccountID(sess sessionpkg.Thread) string {
	if value := runtimeMetadataString(sess.RuntimeMetadata, "runtime_owner_account_id"); value != "" {
		return value
	}
	return runtimeMetadataString(sess.Metadata, "runtime_owner_account_id")
}

func runtimeMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

type SpeechModelResolver struct {
	settings Settings
}

func (r *SpeechModelResolver) ResolveSpeechModelID(ctx context.Context, botID string) (string, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return "", err
	}
	return s.TtsModelID, nil
}

type IMDisplayOptions struct {
	settings Settings
}

func (r *IMDisplayOptions) ShowToolCallsInIM(ctx context.Context, botID string) (bool, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return false, err
	}
	return s.ShowToolCallsInIM, nil
}

type DefaultChatRuntime struct {
	settings Settings
}

func (r *DefaultChatRuntime) DefaultChatRuntime(ctx context.Context, botID string) (inbound.DefaultChatRuntimeSettings, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return inbound.DefaultChatRuntimeSettings{}, err
	}
	return inbound.DefaultChatRuntimeSettings{
		Runtime:     s.ChatRuntime,
		ACPAgentID:  s.ChatACPAgentID,
		ProjectPath: s.ChatACPProjectPath,
		ProjectMode: s.ChatACPProjectMode,
	}, nil
}

type ACPAgentSetupReader struct {
	bots *bot.Service
}

func (r *ACPAgentSetupReader) ACPAgentSetupMetadata(ctx context.Context, botID string) (map[string]any, error) {
	if r == nil || r.bots == nil {
		return nil, errors.New("bot setup reader not configured")
	}
	bot, err := r.bots.Get(ctx, botID)
	if err != nil {
		return nil, err
	}
	return bot.Metadata, nil
}

type BotPermissionChecker struct {
	bots     *bot.Service
	accounts *account.Service
}

func (a *BotPermissionChecker) HasBotPermission(ctx context.Context, botID, accountID, permission string) (bool, error) {
	if a == nil || a.bots == nil || a.accounts == nil {
		return false, errors.New("bot permission services not configured")
	}
	isAdmin, err := a.accounts.IsAdmin(ctx, accountID)
	if err != nil {
		return false, err
	}
	perms, err := a.bots.ResolveUserPermissions(ctx, botID, accountID, isAdmin)
	if err != nil {
		return false, err
	}
	return bot.HasPermission(perms, permission), nil
}

type TranscriptionModelResolver struct {
	settings Settings
}

func (r *TranscriptionModelResolver) ResolveTranscriptionModelID(ctx context.Context, botID string) (string, error) {
	s, err := r.settings.GetBot(ctx, botID)
	if err != nil {
		return "", err
	}
	return s.TranscriptionModelID, nil
}

type Audio interface {
	Synthesize(ctx context.Context, modelID string, text string, overrideCfg map[string]any) ([]byte, string, error)
	Transcribe(ctx context.Context, modelID string, audio []byte, filename string, contentType string, overrideCfg map[string]any) (inbound.TranscriptionResult, error)
}

type LocalAudio struct{ service *audiopkg.Service }

func (a *LocalAudio) Synthesize(ctx context.Context, modelID, text string, overrideCfg map[string]any) ([]byte, string, error) {
	return a.service.Synthesize(ctx, modelID, text, overrideCfg)
}

func (a *LocalAudio) Transcribe(ctx context.Context, modelID string, data []byte, filename, contentType string, overrideCfg map[string]any) (inbound.TranscriptionResult, error) {
	result, err := a.service.Transcribe(ctx, modelID, data, filename, contentType, overrideCfg)
	if err != nil {
		return nil, err
	}
	return transcriptionResult{text: result.Text}, nil
}

type Settings interface {
	GetBot(context.Context, string) (setting.Settings, error)
}

type EmailOAuthClientResolver struct {
	inner oauth.Resolver
}

func (r EmailOAuthClientResolver) Get(ref string) (emailpkg.OAuthClient, bool) {
	if r.inner == nil {
		return emailpkg.OAuthClient{}, false
	}
	client, ok := r.inner.Get(ref)
	if !ok {
		return emailpkg.OAuthClient{}, false
	}
	return emailpkg.OAuthClient{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		RedirectURI:  client.RedirectURI,
	}, true
}

func (r EmailOAuthClientResolver) HasUsableClient(ref string) bool {
	return r.inner != nil && r.inner.HasUsableClient(ref)
}

type CommandSkillLoader struct {
	handler *runtimehttp.ContainerdHandler
}

func (a *CommandSkillLoader) LoadSkills(ctx context.Context, botID string) ([]command.Skill, error) {
	items, err := a.handler.LoadSkills(ctx, botID)
	if err != nil {
		return nil, err
	}
	skills := make([]command.Skill, len(items))
	for i, item := range items {
		skills[i] = command.Skill{Name: item.Name, Description: item.Description}
	}
	return skills, nil
}

// ListRuntimeSkills exposes the runtime-usable safe catalog (the same list the
// Web slash picker shows) as the command layer's optional RuntimeSkillLister
// capability, upgrading /skill list to tap-to-activate rows.
func (a *CommandSkillLoader) ListRuntimeSkills(ctx context.Context, botID string) ([]command.Skill, error) {
	items, err := a.handler.ListSafeSkillCatalog(ctx, botID)
	if err != nil {
		return nil, err
	}
	skills := make([]command.Skill, len(items))
	for i, item := range items {
		skills[i] = command.Skill{Name: item.Name, Description: item.Description}
	}
	return skills, nil
}

type CommandContainerFS struct {
	provider bridge.Provider
}

func (a *CommandContainerFS) ListDir(ctx context.Context, botID, dirPath string) ([]command.FSEntry, error) {
	client, err := a.provider.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	entries, err := client.ListDirAll(ctx, dirPath, false)
	if err != nil {
		return nil, err
	}
	result := make([]command.FSEntry, len(entries))
	for i, e := range entries {
		name := stdpath.Base(e.GetPath())
		result[i] = command.FSEntry{Name: name, IsDir: e.GetIsDir(), Size: e.GetSize()}
	}
	return result, nil
}

func (a *CommandContainerFS) ReadFile(ctx context.Context, botID, filePath string) (string, error) {
	client, err := a.provider.MCPClient(ctx, botID)
	if err != nil {
		return "", err
	}
	resp, err := client.ReadFile(ctx, filePath, 0, 0)
	if err != nil {
		return "", err
	}
	return resp.GetContent(), nil
}

type transcriptionResult struct {
	text string
}

func (r transcriptionResult) GetText() string { return r.text }

// Constructors. The adapter structs keep unexported fields so Channel
// composition depends on these entry points rather than on field layout.

// NewBotPresence adapts bot persistence to the Channel presence port.
func NewBotPresence(bots bot.Persistence) BotPresence {
	return BotPresence{bots: bots}
}

// NewSessionEnsurer adapts the route thread coordinator to inbound sessions.
func NewSessionEnsurer(coordinator *route.ThreadCoordinator) *SessionEnsurer {
	return &SessionEnsurer{coordinator: coordinator}
}

// NewSpeechModelResolver resolves a bot's TTS model from settings.
func NewSpeechModelResolver(settings Settings) *SpeechModelResolver {
	return &SpeechModelResolver{settings: settings}
}

// NewTranscriptionModelResolver resolves a bot's transcription model from settings.
func NewTranscriptionModelResolver(settings Settings) *TranscriptionModelResolver {
	return &TranscriptionModelResolver{settings: settings}
}

// NewIMDisplayOptions resolves IM tool-call display preferences from settings.
func NewIMDisplayOptions(settings Settings) *IMDisplayOptions {
	return &IMDisplayOptions{settings: settings}
}

// NewDefaultChatRuntime resolves a bot's default chat runtime from settings.
func NewDefaultChatRuntime(settings Settings) *DefaultChatRuntime {
	return &DefaultChatRuntime{settings: settings}
}

// NewACPAgentSetupReader exposes bot-owned ACP setup metadata to inbound.
func NewACPAgentSetupReader(bots *bot.Service) *ACPAgentSetupReader {
	return &ACPAgentSetupReader{bots: bots}
}

// NewBotPermissionChecker adapts bot and account services to the inbound
// permission port.
func NewBotPermissionChecker(bots *bot.Service, accounts *account.Service) *BotPermissionChecker {
	return &BotPermissionChecker{bots: bots, accounts: accounts}
}

// NewLocalAudio adapts the Model audio service to the Channel audio port.
func NewLocalAudio(service *audiopkg.Service) Audio {
	return &LocalAudio{service: service}
}

// NewCommandSkillLoader adapts the runtime handler to the command skill loader.
func NewCommandSkillLoader(handler *runtimehttp.ContainerdHandler) *CommandSkillLoader {
	return &CommandSkillLoader{handler: handler}
}

// NewCommandContainerFS adapts a bridge provider to the command filesystem port.
func NewCommandContainerFS(provider bridge.Provider) *CommandContainerFS {
	return &CommandContainerFS{provider: provider}
}

// NewEmailOAuthClientResolver adapts the built-in OAuth registry to the email
// provider's client lookup port.
func NewEmailOAuthClientResolver(inner oauth.Resolver) EmailOAuthClientResolver {
	return EmailOAuthClientResolver{inner: inner}
}
