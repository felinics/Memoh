package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/toolcontext"
)

type ToolSessionContext = toolcontext.Session

type PromptResult struct {
	// SessionID is the ACP session the prompt was sent to.
	SessionID  string     `json:"session_id,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
	Text       string     `json:"text,omitempty"`
	Usage      *sdk.Usage `json:"usage,omitempty"`
	// Output is the in-process transcript used for persistence.
	Output []sdk.Message `json:"-"`
	// Meta is the prompt response's _meta, where agents report what ACP has
	// no field for.
	Meta map[string]any `json:"-"`
}

// PromptResource is an embedded text resource sent alongside an ACP prompt.
type PromptResource struct {
	URI      string
	MimeType string
	Text     string
}

// PromptImage is an inline image sent as an ACP image content block. Data is
// raw base64 without the data URL prefix; MimeType must be an image MIME type.
type PromptImage struct {
	Data     string
	MimeType string
}

type PromptOptions struct {
	ToolOutputLimit   ToolOutputLimit
	Images            []PromptImage
	AllowResourceOnly bool
	// RequiredCommand is an exact, opaque Agent command name. When set, the
	// Session rechecks its latest available_commands snapshot immediately
	// before dispatching the prompt.
	RequiredCommand string
}

type Session struct {
	callbacks         *clientCallbacks
	conn              *clientConnection
	sessionID         acp.SessionId
	projectPath       string
	modelConfigID     string
	modelState        ModelState
	reasoningConfigID string
	reasoningState    ReasoningState
	modeState         ModeState
	availableCommands []AvailableCommandInfo
	// commandsAdvertised closes when the agent first advertises commands,
	// which ACP has it do by notification after the session opens.
	commandsAdvertised     chan struct{}
	commandsAdvertisedOnce sync.Once
	modeRevision           uint64
	embeddedContext        bool
	imagePromptSupported   bool
	closeSessionSupported  bool
	defaultSink            EventSink
	lifecycleCtx           context.Context
	cancel                 context.CancelFunc
	// stderr returns the agent's recent stderr for a failed prompt.
	stderr func() string
	// release detaches a session from a shared Conn, whose process it does
	// not own.
	release func()

	promptMu     sync.Mutex
	mu           sync.Mutex
	promptCancel context.CancelFunc
	promptDone   <-chan struct{}
	promptToken  *struct{}
	closed       bool
	// closeDone closes once the first close has finished; closeErr is its
	// result, which every later or concurrent close returns as well: a
	// close never turns an unconfirmed one into a confirmed one.
	closeDone chan struct{}
	closeErr  error
}

// ErrNativeSessionGone means the agent cannot restore a stored session and a
// new one may take its place.
var ErrNativeSessionGone = errors.New("agent cannot restore the native session")

// ErrSessionRestoreFailed means a running agent did not restore its stored
// session: it timed out, exited or broke the connection. The session may be
// intact, so it is kept; only the user can decide to give it up.
var ErrSessionRestoreFailed = errors.New("ACP agent could not restore its session")

// authRequiredCode is ACP's auth_required JSON-RPC error.
const authRequiredCode = -32000

// agentAnswer returns the error the agent answered a restore with. ACP names
// no error for a missing session, and agents answer -32002, -32602 and -32603
// alike, so any answer counts. A request that timed out or lost its
// connection was not answered, though the SDK reports those as internal
// errors too.
func agentAnswer(ctx context.Context, conn *clientConnection, err error) (*acp.RequestError, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	select {
	case <-conn.conn.Done():
		return nil, false
	default:
	}
	var answer *acp.RequestError
	if !errors.As(err, &answer) {
		return nil, false
	}
	return answer, true
}

func (s *Session) ID() string {
	if s == nil {
		return ""
	}
	return string(s.sessionID)
}

func (s *Session) ProjectPath() string {
	if s == nil {
		return ""
	}
	return s.projectPath
}

// CancelPrompt asks an in-flight prompt to unwind without closing the ACP
// session. Pool shutdown uses this before waiting for the per-runtime operation
// lock: a prompt that already completed can finish its durable snapshot, while
// a prompt still blocked in the agent receives session/cancel and releases the
// lock. The actual session/process close happens only after that boundary.
func (s *Session) CancelPrompt() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.promptCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) Prompt(ctx context.Context, prompt string, sinks ...EventSink) (PromptResult, error) {
	return s.PromptWithResources(ctx, prompt, nil, sinks...)
}

// PromptWithResources sends a user prompt plus optional embedded resources.
func (s *Session) PromptWithResources(ctx context.Context, prompt string, resources []PromptResource, sinks ...EventSink) (PromptResult, error) {
	return s.PromptWithToolContext(ctx, prompt, resources, ToolSessionContext{}, sinks...)
}

// PromptWithToolContext sends a user prompt and binds request-scoped tool
// identity to ACP callbacks while that prompt is active.
func (s *Session) PromptWithToolContext(ctx context.Context, prompt string, resources []PromptResource, toolSession ToolSessionContext, sinks ...EventSink) (PromptResult, error) {
	return s.PromptWithToolContextOptions(ctx, prompt, resources, toolSession, PromptOptions{}, sinks...)
}

func (s *Session) PromptWithToolContextOptions(ctx context.Context, prompt string, resources []PromptResource, toolSession ToolSessionContext, options PromptOptions, sinks ...EventSink) (PromptResult, error) {
	if s == nil || s.conn == nil {
		return PromptResult{}, ErrSessionNotInitialized
	}
	prompt = strings.TrimSpace(prompt)
	images, err := NormalizePromptImages(options.Images)
	if err != nil {
		return PromptResult{}, err
	}
	if prompt == "" && len(images) == 0 && (!options.AllowResourceOnly || len(cleanPromptResources(resources)) == 0) {
		return PromptResult{}, ErrPromptRequired
	}
	if len(images) > 0 && !s.imagePromptSupported {
		return PromptResult{}, ErrImagePromptUnsupported
	}

	s.promptMu.Lock()
	defer s.promptMu.Unlock()

	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()
	promptToken := &struct{}{}
	promptDone := make(chan struct{})

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		close(promptDone)
		return PromptResult{}, ErrSessionClosed
	}
	s.promptCancel = cancelPrompt
	s.promptDone = promptDone
	s.promptToken = promptToken
	conn := s.conn
	sessionID := s.sessionID
	callbacks := s.callbacks
	stderr := s.stderr
	defaultSink := s.defaultSink
	s.mu.Unlock()
	defer func() {
		close(promptDone)
		s.mu.Lock()
		if s.promptToken == promptToken {
			s.promptCancel = nil
			s.promptDone = nil
			s.promptToken = nil
		}
		s.mu.Unlock()
	}()
	if conn == nil {
		return PromptResult{}, ErrSessionNotInitialized
	}

	promptBlocks := s.promptBlocks(prompt, resources, images)
	collector := newEventCollector(options.ToolOutputLimit)
	// The prompt context is the output boundary. Once Stop/Close cancels it,
	// late adapter notifications must not reach either history or the live UI.
	collector.bindContext(promptCtx)
	sink := defaultSink
	if len(sinks) > 0 {
		sink = sinks[0]
	}
	if callbacks != nil {
		callbacks.setPromptState(collector, sink, toolSession, options.ToolOutputLimit)
	}
	defer func() {
		if callbacks != nil {
			if promptCtx.Err() != nil {
				// Record the cancelled turn's tool calls before the per-prompt
				// states are wiped, so a late permission callback for one of
				// them resolves as cancelled instead of correlating against
				// the next turn.
				callbacks.markPromptCancelled()
			}
			callbacks.setPromptState(nil, nil, ToolSessionContext{}, ToolOutputLimit{})
		}
	}()
	if options.RequiredCommand != "" && !s.AdvertisesCommand(options.RequiredCommand) &&
		!slices.ContainsFunc(s.AwaitCommands(promptCtx), func(command AvailableCommandInfo) bool { return command.Name == options.RequiredCommand }) {
		return PromptResult{}, ErrAgentCommandUnavailable
	}

	resp, err := conn.Prompt(promptCtx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    promptBlocks,
	})
	collected := collector.result()
	usage := promptUsageFromACP(resp.Usage)
	result := PromptResult{
		StopReason: string(resp.StopReason),
		Text:       collected.Text,
		Usage:      usage,
		Output:     attachUsageToLastAssistant(collected.Output, usage),
		Meta:       resp.Meta,
	}
	if !errors.Is(err, errPromptNotSent) {
		result.SessionID = string(sessionID)
	}
	if err != nil {
		return result, withStderr(fmt.Errorf("send ACP prompt: %w", err), stderr)
	}
	return result, nil
}

func promptUsageFromACP(usage *acp.Usage) *sdk.Usage {
	if usage == nil {
		return nil
	}
	out := &sdk.Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
	}
	read, write := 0, 0
	if usage.CachedReadTokens != nil {
		read = *usage.CachedReadTokens
	}
	if usage.CachedWriteTokens != nil {
		write = *usage.CachedWriteTokens
	}
	// ACP leaves open whether inputTokens includes the cache counters;
	// claude-agent-acp reports them beside it and counts them in totalTokens.
	if cached := read + write; cached > 0 && (usage.TotalTokens == usage.InputTokens+usage.OutputTokens+cached || usage.InputTokens < cached) {
		out.InputTokens += cached
	}
	out.CachedInputTokens = read
	out.InputTokenDetails = sdk.InputTokenDetail{NoCacheTokens: out.InputTokens - read - write, CacheReadTokens: read, CacheWriteTokens: write}
	if usage.ThoughtTokens != nil {
		out.ReasoningTokens = *usage.ThoughtTokens
		out.OutputTokenDetails.ReasoningTokens = *usage.ThoughtTokens
	}
	return out
}

func attachUsageToLastAssistant(output []sdk.Message, usage *sdk.Usage) []sdk.Message {
	if usage == nil {
		return output
	}
	for i := len(output) - 1; i >= 0; i-- {
		if output[i].Role == sdk.MessageRoleAssistant {
			output[i].Usage = usage
			return output
		}
	}
	return output
}

func (s *Session) promptBlocks(prompt string, resources []PromptResource, images []PromptImage) []acp.ContentBlock {
	cleaned := cleanPromptResources(resources)
	blocks := make([]acp.ContentBlock, 0, 1+len(cleaned)+len(images))
	switch {
	case len(cleaned) == 0:
		if prompt != "" {
			blocks = append(blocks, acp.TextBlock(prompt))
		}
	case s != nil && s.embeddedContext:
		for _, resource := range cleaned {
			mimeType := resource.MimeType
			blocks = append(blocks, acp.ResourceBlock(acp.EmbeddedResourceResource{
				TextResourceContents: &acp.TextResourceContents{
					Uri:      resource.URI,
					MimeType: &mimeType,
					Text:     resource.Text,
				},
			}))
		}
		if prompt != "" {
			blocks = append(blocks, acp.TextBlock(prompt))
		}
	default:
		var sb strings.Builder
		for _, resource := range cleaned {
			sb.WriteString("<context ref=\"")
			sb.WriteString(resource.URI)
			sb.WriteString("\">\n")
			sb.WriteString(resource.Text)
			sb.WriteString("\n</context>\n\n")
		}
		sb.WriteString(prompt)
		if text := strings.TrimSpace(sb.String()); text != "" {
			blocks = append(blocks, acp.TextBlock(text))
		}
	}
	for _, image := range images {
		blocks = append(blocks, acp.ImageBlock(image.Data, image.MimeType))
	}
	return blocks
}

const maxPromptImageBytes int64 = 20 * 1024 * 1024

// NormalizePromptImages validates and normalizes images before a runtime is
// started. Session prompt dispatch calls it again as a defense-in-depth check.
func NormalizePromptImages(images []PromptImage) ([]PromptImage, error) {
	out := make([]PromptImage, 0, len(images))
	for i, image := range images {
		data := strings.TrimSpace(image.Data)
		mimeType := strings.ToLower(strings.TrimSpace(image.MimeType))
		if idx := strings.Index(mimeType, ";"); idx >= 0 {
			mimeType = strings.TrimSpace(mimeType[:idx])
		}
		if data == "" || !strings.HasPrefix(mimeType, "image/") || !validPromptImageBase64(data) {
			return nil, fmt.Errorf("%w at index %d", ErrInvalidPromptImage, i)
		}
		out = append(out, PromptImage{
			Data:     data,
			MimeType: mimeType,
		})
	}
	return out, nil
}

func validPromptImageBase64(data string) bool {
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(data))
	n, err := io.Copy(io.Discard, io.LimitReader(decoder, maxPromptImageBytes+1))
	return err == nil && n <= maxPromptImageBytes
}

func cleanPromptResources(resources []PromptResource) []PromptResource {
	out := make([]PromptResource, 0, len(resources))
	for _, resource := range resources {
		text := strings.TrimSpace(resource.Text)
		if text == "" {
			continue
		}
		uri := strings.TrimSpace(resource.URI)
		if uri == "" {
			continue
		}
		mimeType := strings.TrimSpace(resource.MimeType)
		if mimeType == "" {
			mimeType = "text/plain"
		}
		out = append(out, PromptResource{
			URI:      uri,
			MimeType: mimeType,
			Text:     text,
		})
	}
	return out
}

// AdvertisesCommand reports whether the live session currently declares the
// named agent command. Names are opaque and case-sensitive; the caller passes
// the exact selector, never a normalized form.
func (s *Session) AdvertisesCommand(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, command := range s.availableCommands {
		if command.Name == name {
			return true
		}
	}
	return false
}

// WaitDecisionCallbacksIdle reports whether every in-flight permission/Form
// callback finished before the timeout. A cancelled prompt uses it as the
// quiescence barrier before the warm runtime is handed to the next turn.
func (s *Session) WaitDecisionCallbacksIdle(timeout time.Duration) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	callbacks := s.callbacks
	s.mu.Unlock()
	return callbacks.waitDecisionCallbacksIdle(timeout)
}

// Close ends the session: session/close when the agent offers it. It returns
// ErrSessionCloseUnconfirmed unless the agent confirmed the close, which is
// the only proof the agent let go of the session before its process ends.
func (s *Session) Close() error {
	return s.close(false)
}

// ForceClose drops the session locally — its context and callbacks — without
// asking the agent: it is reserved for an unconfirmed prompt cancellation,
// where a write blocked on the shared connection could keep session/close
// from ever being sent. The session stays unconfirmed; the process owner
// retires the process.
func (s *Session) ForceClose() error {
	return s.close(true)
}

func (s *Session) close(force bool) (result error) {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return s.closeErr
	}
	s.closed = true
	done := make(chan struct{})
	s.closeDone = done
	defer func() {
		s.closeErr = result
		close(done)
	}()
	conn := s.conn
	sessionID := s.sessionID
	callbacks := s.callbacks
	cancel := s.cancel
	closeSessionSupported := s.closeSessionSupported
	lifecycleCtx := s.lifecycleCtx
	promptCancel := s.promptCancel
	promptDone := s.promptDone
	release := s.release
	s.mu.Unlock()
	if release != nil {
		defer release()
	}

	if promptCancel != nil {
		promptCancel()
	}
	if !force && promptDone != nil {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-promptDone:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	if force {
		// Close the process/pipe before any graceful JSON-RPC request. This is the
		// operation that releases a writer stuck behind connection.writeMu.
		if cancel != nil {
			cancel()
		}
		if callbacks != nil {
			callbacks.close()
		}
		// Nothing asked the agent to let the session go.
		return ErrSessionCloseUnconfirmed
	}
	closeErr := ErrSessionCloseUnconfirmed
	if closeSessionSupported && conn != nil && sessionID != "" {
		if lifecycleCtx == nil {
			lifecycleCtx = context.Background()
		}
		ctx, cancelClose := context.WithTimeout(context.WithoutCancel(lifecycleCtx), 2*time.Second)
		if _, err := conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sessionID}); err == nil {
			closeErr = nil
		}
		cancelClose()
	}
	if callbacks != nil {
		callbacks.close()
	}
	if cancel != nil {
		cancel()
	}
	return closeErr
}

// withStderr adds the agent's recent stderr to err; an agent that crashed
// usually said why there.
func withStderr(err error, stderr func() string) error {
	if stderr == nil {
		return err
	}
	if tail := strings.TrimSpace(stderr()); tail != "" {
		return fmt.Errorf("%w: %s", err, tail)
	}
	return err
}
