package serverruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/channel/inbound"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/handlers"
	intrpc "github.com/felinics/memoh/internal/rpc"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
	"github.com/felinics/memoh/internal/skills"
)

const (
	MethodCommandAccess        = "server.command.access"
	MethodCommandContext       = "server.command.context"
	MethodCommandExecute       = "server.command.execute"
	MethodCommandExecuteText   = "server.command.execute_text"
	MethodCommandHasResource   = "server.command.has_resource"
	MethodCommandMemberRole    = "server.command.member_role"
	MethodCommandResolveLocale = "server.command.resolve_locale"
	MethodResolveSkills        = "server.skills.resolve"
	MethodSynthesize           = "server.audio.synthesize"
	MethodTranscribe           = "server.audio.transcribe"
)

type Client struct{ rpc *runtimeRpc.Client }

func NewClient(rpc *runtimeRpc.Client) *Client { return &Client{rpc: rpc} }

func (c *Client) CommandAccess(ctx context.Context, input command.ExecuteInput) (bool, error) {
	var out bool
	return out, c.call(ctx, MethodCommandAccess, input, &out)
}

func (c *Client) CurrentContext(ctx context.Context, botID string) (command.CurrentContext, error) {
	var out command.CurrentContext
	return out, c.call(ctx, MethodCommandContext, botID, &out)
}

func (c *Client) ExecuteResult(ctx context.Context, input command.ExecuteInput) (*command.Result, error) {
	var out command.Result
	if err := c.call(ctx, MethodCommandExecute, input, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ExecuteWithInput(ctx context.Context, input command.ExecuteInput) (string, error) {
	var out string
	return out, c.call(ctx, MethodCommandExecuteText, input, &out)
}

func (c *Client) HasCommandResource(resource string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bool
	return c.call(ctx, MethodCommandHasResource, resource, &out) == nil && out
}

func (c *Client) MemberRole(ctx context.Context, botID, channelIdentityID string) (string, error) {
	var out string
	in := struct{ BotID, ChannelIdentityID string }{botID, channelIdentityID}
	return out, c.call(ctx, MethodCommandMemberRole, in, &out)
}

func (c *Client) ResolveLocale(ctx context.Context, botID string) string {
	var out string
	if c.call(ctx, MethodCommandResolveLocale, botID, &out) != nil {
		return "en"
	}
	return out
}

func (c *Client) ResolveTextRequestedSkills(ctx context.Context, botID string, names []string) ([]skills.ResolvedSkill, error) {
	var out []skills.ResolvedSkill
	in := struct {
		BotID string
		Names []string
	}{botID, names}
	return out, c.call(ctx, MethodResolveSkills, in, &out)
}

func (c *Client) Synthesize(ctx context.Context, modelID, text string, overrideCfg map[string]any) ([]byte, string, error) {
	in := struct {
		ModelID, Text string
		Config        map[string]any
	}{modelID, text, overrideCfg}
	var out struct {
		Audio       []byte
		ContentType string
	}
	if err := c.call(ctx, MethodSynthesize, in, &out); err != nil {
		return nil, "", err
	}
	return out.Audio, out.ContentType, nil
}

// maxTranscribeAudioBytes bounds the raw audio accepted for the internal
// transcription RPC. Audio crosses the wire as base64 inside a single JSON
// unary payload (≈4/3 expansion plus envelope), so anything close to the
// transport's message cap would fail with an opaque ResourceExhausted;
// reject it up front with an actionable error instead.
const maxTranscribeAudioBytes = (intrpc.MaxMessageBytes/4)*3 - (1 << 20)

func (c *Client) Transcribe(ctx context.Context, modelID string, data []byte, filename, contentType string, overrideCfg map[string]any) (inbound.TranscriptionResult, error) {
	if len(data) > maxTranscribeAudioBytes {
		return nil, fmt.Errorf("audio is too large for internal transcription rpc: %d bytes (limit %d)", len(data), maxTranscribeAudioBytes)
	}
	in := struct {
		ModelID               string
		Audio                 []byte
		Filename, ContentType string
		Config                map[string]any
	}{modelID, data, filename, contentType, overrideCfg}
	var out transcriptionResult
	if err := c.call(ctx, MethodTranscribe, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) call(ctx context.Context, method string, input, output any) error {
	return c.rpc.Call(ctx, method, input, output)
}

type transcriptionResult struct {
	Text string `json:"text"`
}

func (r transcriptionResult) GetText() string { return r.Text }

func Handlers(commandHandler *command.Handler, skillHandler *handlers.ContainerdHandler, audioService *audio.Service) map[string]runtimeRpc.Handler {
	decode := func(raw json.RawMessage, dst any) error { return json.Unmarshal(raw, dst) }
	return map[string]runtimeRpc.Handler{
		MethodCommandAccess: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in command.ExecuteInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return commandHandler.CommandAccess(ctx, in)
		},
		MethodCommandContext: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var botID string
			if err := decode(raw, &botID); err != nil {
				return nil, err
			}
			return commandHandler.CurrentContext(ctx, botID)
		},
		MethodCommandExecute: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in command.ExecuteInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return commandHandler.ExecuteResult(ctx, in)
		},
		MethodCommandExecuteText: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in command.ExecuteInput
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return commandHandler.ExecuteWithInput(ctx, in)
		},
		MethodCommandHasResource: func(_ context.Context, raw json.RawMessage) (any, error) {
			var resource string
			if err := decode(raw, &resource); err != nil {
				return nil, err
			}
			return commandHandler.HasCommandResource(resource), nil
		},
		MethodCommandMemberRole: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct{ BotID, ChannelIdentityID string }
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return commandHandler.MemberRole(ctx, in.BotID, in.ChannelIdentityID)
		},
		MethodCommandResolveLocale: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var botID string
			if err := decode(raw, &botID); err != nil {
				return nil, err
			}
			return commandHandler.ResolveLocale(ctx, botID), nil
		},
		MethodResolveSkills: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				BotID string
				Names []string
			}
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			return skillHandler.ResolveTextRequestedSkills(ctx, in.BotID, in.Names)
		},
		MethodSynthesize: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				ModelID, Text string
				Config        map[string]any
			}
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			data, contentType, err := audioService.Synthesize(ctx, in.ModelID, in.Text, in.Config)
			return struct {
				Audio       []byte
				ContentType string
			}{data, contentType}, err
		},
		MethodTranscribe: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				ModelID               string
				Audio                 []byte
				Filename, ContentType string
				Config                map[string]any
			}
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			result, err := audioService.Transcribe(ctx, in.ModelID, in.Audio, in.Filename, in.ContentType, in.Config)
			if err != nil {
				return nil, err
			}
			return transcriptionResultFromSDK(result), nil
		},
	}
}

func transcriptionResultFromSDK(result *sdk.TranscriptionResult) transcriptionResult {
	if result == nil {
		return transcriptionResult{}
	}
	return transcriptionResult{Text: result.Text}
}

var (
	_ inbound.CommandHandler         = (*Client)(nil)
	_ inbound.RequestedSkillResolver = (*Client)(nil)
)
