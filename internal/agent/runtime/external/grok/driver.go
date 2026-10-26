package grok

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/grok/grokcfg"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/botagents"
)

// contextURI names the Memoh context document embedded in a prompt.
const contextURI = "memoh://context/current-turn"

// decisionQuiesceTimeout bounds how long a stopped turn waits for its
// permission callbacks to unwind before its session is given up.
const decisionQuiesceTimeout = 3 * time.Second

// Driver runs Grok Build turns over ACP.
type Driver struct {
	workspaces  external.Workspaces
	agents      *botagents.Service
	credentials *agentcredential.Service
	approval    acpclient.ToolApprovalService
	userInput   acpclient.UserInputService
	toolGateway toolmount.Gateway
	logger      *slog.Logger
	// launchers picks the grok CLI copy to run per bot. Nil falls back to the
	// toolkit path.
	launchers external.LauncherResolver

	// procs owns the process lifecycle: one per Bot Agent, shared by its
	// conversations.
	procs *agentproc.Supervisor

	// signIns are the account sign-ins under way, by login id; signInGates
	// serialize starting, polling, and cancelling them per Agent.
	signInsMu   sync.Mutex
	signIns     map[string]*signIn
	signInGates map[string]chan struct{}
}

func NewDriver(
	workspaces external.Workspaces,
	agents *botagents.Service,
	credentials *agentcredential.Service,
	approval acpclient.ToolApprovalService,
	toolGateway toolmount.Gateway,
	procs *agentproc.Supervisor,
	logger *slog.Logger,
) *Driver {
	return &Driver{
		workspaces:  workspaces,
		agents:      agents,
		credentials: credentials,
		approval:    approval,
		toolGateway: toolGateway,
		procs:       procs,
		logger:      logger.With(slog.String("runtime", RuntimeType)),
		signIns:     map[string]*signIn{},
		signInGates: map[string]chan struct{}{},
	}
}

var (
	_ external.Driver             = (*Driver)(nil)
	_ external.DependencyRequirer = (*Driver)(nil)
)

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

// RequiredDependency implements external.DependencyRequirer.
func (*Driver) RequiredDependency() string { return dependencyID }

// SetLauncherResolver installs the workspace dependency resolver. Setter
// injection keeps the driver constructible without one.
func (d *Driver) SetLauncherResolver(r external.LauncherResolver) { d.launchers = r }

// SetUserInputService installs the service that puts Grok's questions to the
// user. Without one, Grok is told every question went unanswered.
func (d *Driver) SetUserInputService(s acpclient.UserInputService) { d.userInput = s }

func serverKey(botID, botAgentID string) string {
	return agentproc.Key(RuntimeType, botID, botAgentID)
}

// runningServers lists every live process of the Agent: the current one and
// those retired but still finishing turns.
func (d *Driver) runningServers(key string) []*server {
	var servers []*server
	for _, proc := range d.procs.Processes(key) {
		servers = append(servers, proc.(*server))
	}
	return servers
}

// agentConfig reads the Agent's configuration without its credential, which
// only a starting process needs.
func (d *Driver) agentConfig(ctx context.Context, botID, botAgentID string) (Config, error) {
	agent, err := d.agents.Get(ctx, botID, botAgentID)
	if err != nil {
		return Config{}, err
	}
	return grokcfg.ParseAgentConfig(agent.Metadata)
}

// fingerprint names what a Grok process is started from: a change to any of
// it needs a new process. Model, effort, and mode travel with each session or
// turn and are not part of it. The API key is hashed, never kept.
func fingerprint(cfg Config, launcher external.Launcher) string {
	key := sha256.Sum256([]byte(cfg.APIKey))
	return strings.Join([]string{
		string(cfg.Auth), hex.EncodeToString(key[:]), launcher.Path, string(launcher.Source), launcher.Version,
	}, "\x00")
}

// acquireServer leases the Agent's process, starting one from the current
// configuration when there is none or it was started from another. The
// caller must call the returned release when done.
func (d *Driver) acquireServer(ctx context.Context, botID, botAgentID string) (*server, func(), error) {
	info, err := external.ContainerWorkspace(ctx, d.workspaces, botID, RuntimeType)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := d.agentConfig(ctx, botID, botAgentID)
	if err != nil {
		return nil, nil, err
	}
	if cfg, err = d.apiKeyFor(ctx, botID, botAgentID, cfg); err != nil {
		return nil, nil, err
	}
	launcher, err := agentproc.ResolveLauncher(ctx, d.launchers, botID, dependencyID, defaultLauncherPath)
	if err != nil {
		return nil, nil, err
	}
	start := func(ctx context.Context) (agentproc.Process, string, error) {
		client, err := d.workspaces.MCPClient(ctx, botID)
		if err != nil {
			return nil, "", fmt.Errorf("workspace bridge for bot %s: %w", botID, err)
		}
		if err := prepareHome(ctx, client, botAgentID, cfg); err != nil {
			return nil, "", err
		}
		// The process outlives the request that happened to start it.
		srv, err := startServer(context.WithoutCancel(ctx), botID, grokHome(botAgentID), client, info, cfg.APIKey, launcher, d.logger)
		if err != nil {
			return nil, "", err
		}
		version, _ := srv.conn.AgentMeta()["agentVersion"].(string)
		agentproc.ObserveVersion(ctx, d.launchers, botID, dependencyID, version)
		started := launcher
		started.Version = firstNonEmpty(version, launcher.Version)
		return srv, fingerprint(cfg, started), nil
	}
	proc, release, err := d.procs.Lease(ctx, serverKey(botID, botAgentID), fingerprint(cfg, launcher), start)
	if err != nil {
		return nil, nil, err
	}
	return proc.(*server), release, nil
}

// releaseOtherCopies keeps one process per Grok session: a copy of the
// conversation's session an earlier process of the Agent still holds is closed
// before srv opens it. Admission keeps two operations off one conversation, so
// such a copy is idle; one that is not refuses.
func (d *Driver) releaseOtherCopies(srv *server, input external.PromptInput) error {
	for _, other := range d.runningServers(serverKey(input.BotID, input.BotAgentID)) {
		if other == srv {
			continue
		}
		th := other.thread(input.ThreadID)
		if th == nil {
			continue
		}
		if th.turn.Load() != nil {
			return external.Fail(external.FailureRuntimeBusy, nil)
		}
		other.dropThread(input.ThreadID, th, false)
	}
	return nil
}

// Prompt implements external.Driver: one turn on the conversation's session.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	cfg, err := d.agentConfig(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.PromptResult{}, external.Unavailable(err)
	}
	srv, release, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.PromptResult{}, serverFailure(err)
	}
	defer release()
	if err := d.releaseOtherCopies(srv, input); err != nil { //nolint:contextcheck // an idle copy closes on the old process's own lifecycle.
		return external.PromptResult{}, err
	}

	th, resumeLost, err := srv.openThread(ctx, d.toolGateway, d.approval, d.userInput, input, permissionMode(input.RuntimeMetadata, cfg), true)
	if err != nil {
		if ctx.Err() != nil {
			return external.PromptResult{}, nil
		}
		if errors.Is(err, external.ErrThreadUnavailable) {
			return external.PromptResult{}, err
		}
		return external.PromptResult{}, turnFailure(err, external.FailureUnavailable)
	}
	if resumeLost {
		input.Sink.EmitStreamEvent(event.StreamEvent{
			Type:       event.RuntimeNotice,
			NoticeKind: event.NoticeNativeHistoryLost,
			Delta:      "Grok Build could not resume this conversation's session and started a new one. It does not remember the earlier messages shown here.",
		})
	}
	// Said every turn: silently missing tools is what this notice exists to
	// prevent. Grok reports a gateway it could not reach within a moment of
	// the session opening; a report that never comes does not hold the turn.
	if th.toolless {
		toolmount.EmitUnavailableNotice(input.Sink, "this conversation's session started without Memoh tools")
	} else if status, ok := srv.gatewayStatus(ctx, th.session.ID(), gatewayStatusTimeout); ok && status.Status == mcpStatusUnavailable {
		toolmount.EmitUnavailableNotice(input.Sink, "Grok could not connect to Memoh tools: "+firstNonEmpty(status.Reason, status.Detail, status.Status))
	}
	out := external.PromptResult{RuntimeMetadata: map[string]any{metadataSessionIDKey: th.session.ID()}}

	th.turn.Store(&turnContext{ctx: ctx, input: input})
	defer th.turn.Store(nil)
	sink := acpclient.NewTurnSink(input.Sink, input.ToolOutputLimit)
	defer sink.Finish()
	unregisterToolEvents := toolmount.RegisterTurnSink(d.toolGateway.Contexts, input.BotID, input.ThreadID, input.RunID, sink.EmitStreamEvent)
	defer unregisterToolEvents()

	// A catalog Grok did not report leaves the session's own defaults.
	catalog, _ := catalogFromState(srv.modelState(ctx))
	if err := applyTurnConfig(ctx, th.session, catalog, firstNonEmpty(input.ModelID, cfg.Model), strings.TrimSpace(input.ReasoningEffort), cfg.ReasoningEffort); err != nil {
		if ctx.Err() != nil {
			return out, nil
		}
		return out, external.Fail(external.FailureControlFailed, err)
	}

	prompt, resources, options := promptContent(input)
	result, err := th.session.PromptWithToolContextOptions(ctx, prompt, resources, srv.toolSession(th), options, sink)
	if errors.Is(err, acpclient.ErrImagePromptUnsupported) && len(options.Images) > 0 && input.CanFallbackImagesToFiles {
		options.Images = nil
		result, err = th.session.PromptWithToolContextOptions(ctx, prompt, resources, srv.toolSession(th), options, sink)
	}
	// Stop gateway deliveries before the transcript snapshot is taken.
	unregisterToolEvents()
	out.Text = result.Text
	out.StopReason = result.StopReason
	out.Usage = usageFromMeta(result.Meta)
	if out.Usage == nil {
		// A Grok that reports usage only where ACP defines it.
		out.Usage = result.Usage
	}
	// The prompt id is the turn's anchor: a fork is offered from the latest.
	if promptID, _ := result.Meta["promptId"].(string); promptID != "" {
		out.AgentTurnID = promptID
		out.RuntimeMetadata[metadataLastPromptIDKey] = promptID
	}
	out.Output = sink.Transcript(result.Text)

	if err != nil {
		if ctx.Err() != nil {
			// A stop is not a failure: the partial transcript persists. The
			// session is reusable only once Grok confirmed the cancellation
			// and its permission callbacks have unwound; otherwise new work
			// moves to a fresh process and this one dies with its last turn.
			if errors.Is(err, acpclient.ErrPromptCancellationUnconfirmed) || !th.session.WaitDecisionCallbacksIdle(decisionQuiesceTimeout) {
				srv.dropThread(input.ThreadID, th, true) //nolint:contextcheck // teardown must outlive the stopped turn.
				d.procs.MarkBroken(serverKey(input.BotID, input.BotAgentID), srv)
			}
			return out, nil
		}
		if errors.Is(err, acpclient.ErrImagePromptUnsupported) || errors.Is(err, acpclient.ErrInvalidPromptImage) {
			// The application names these input errors itself.
			return out, err
		}
		return out, turnFailure(err, 0)
	}
	// Grok also ends a turn as cancelled on its own, for one when a
	// permission it asked for is refused. That turn stopped, it did not finish.
	out.TurnCompleted = result.StopReason != string(acp.StopReasonCancelled)
	return out, nil
}

// applyTurnConfig moves the session onto the turn's model and reasoning
// effort. The model goes first: its efforts are what the effort is checked
// against.
//
// The turn's own effort was picked for its model, so one the model lacks is
// an error. The Agent-wide effort was configured without a model in mind and
// applies only where the model offers it.
//
// Grok opens a session on the model and effort the process last switched to,
// in whichever conversation, so what the turn leaves unnamed is set to the
// default of the catalog Grok reported at start.
func applyTurnConfig(ctx context.Context, session *acpclient.Session, catalog []external.ModelOption, modelID, effort, agentEffort string) error {
	for _, model := range catalog {
		if modelID == "" && model.Default {
			modelID = model.ID
		}
	}
	// A session that exposes no selectable models keeps the one it has.
	if state := session.ModelState(); modelID != "" && state.Supported && state.CurrentModelID != modelID {
		if _, err := session.SetModel(ctx, modelID); err != nil {
			return err
		}
	}
	offered := func(id string) bool {
		return id != "" && slices.ContainsFunc(session.ReasoningState().Available, func(option acpclient.ReasoningEffortInfo) bool {
			return option.ID == id
		})
	}
	if effort == "" && offered(agentEffort) {
		effort = agentEffort
	}
	for _, model := range catalog {
		if effort == "" && model.ID == session.ModelState().CurrentModelID && offered(model.DefaultReasoningEffort) {
			effort = model.DefaultReasoningEffort
		}
	}
	if effort != "" && session.ReasoningState().CurrentEffort != effort {
		if _, err := session.SetReasoningEffort(ctx, effort); err != nil {
			return err
		}
	}
	return nil
}

func promptContent(input external.PromptInput) (string, []acpclient.PromptResource, acpclient.PromptOptions) {
	options := acpclient.PromptOptions{ToolOutputLimit: input.ToolOutputLimit}
	for _, image := range input.Images {
		if len(image.Data) == 0 {
			continue
		}
		options.Images = append(options.Images, acpclient.PromptImage{
			MimeType: image.MimeType,
			Data:     base64.StdEncoding.EncodeToString(image.Data),
		})
	}
	var resources []acpclient.PromptResource
	if markdown := strings.TrimSpace(input.ContextMarkdown); markdown != "" {
		resources = []acpclient.PromptResource{{
			URI:      firstNonEmpty(input.ContextURI, contextURI),
			MimeType: "text/markdown",
			Text:     markdown,
		}}
	}
	options.AllowResourceOnly = len(input.AttachmentReferences) > 0 && len(resources) > 0
	return input.Prompt, resources, options
}
