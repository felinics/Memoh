package grok

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/grok/grokcfg"
	"github.com/felinics/memoh/internal/agent/runtime/servertable"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// contextURI names the Memoh context document embedded in a prompt.
const contextURI = "memoh://context/current-turn"

// decisionQuiesceTimeout bounds how long a stopped turn waits for its
// permission callbacks to unwind before its session is given up.
const decisionQuiesceTimeout = 3 * time.Second

// predecessorDrainTimeout bounds how long a start waits for an earlier
// process holding the Agent's login to finish its work (awaitLoginHolders).
const predecessorDrainTimeout = 30 * time.Second

// BridgeSource resolves the workspace bridge client for a bot.
type BridgeSource interface {
	MCPClient(ctx context.Context, botID string) (*bridge.Client, error)
	WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error)
}

// Driver runs Grok Build turns over ACP.
type Driver struct {
	bridges     BridgeSource
	agents      *botagents.Service
	credentials *agentcredential.Service
	approval    acpclient.ToolApprovalService
	userInput   acpclient.UserInputService
	toolGateway toolmount.Gateway
	logger      *slog.Logger
	// launchers picks the grok CLI copy to run per bot. Nil falls back to the
	// toolkit path.
	launchers external.LauncherResolver

	// servers shares one process between the conversations of a Bot Agent.
	servers *servertable.Table

	// running holds one record per Agent that has run here; records are kept
	// for the Server's lifetime.
	runningMu sync.Mutex
	running   map[string]*agentProcesses

	// signIns are the account sign-ins under way, by login id.
	signInsMu sync.Mutex
	signIns   map[string]*signIn
}

// agentProcesses is what shares one Agent's home, and with it the one login
// file.
type agentProcesses struct {
	// starting admits one start at a time, and holds starts off while a
	// disconnect cleans the home. It is a channel so a waiter can give up with
	// its context.
	starting chan struct{}
	// signingIn serializes creation, polling and cancellation per Agent.
	signingIn chan struct{}
	// servers lists every live process: the one the table hands out and the
	// displaced ones still finishing their turns.
	servers []*server
	// purged names the credential a disconnect removed the login of. The
	// Agent still holds it until the disconnect is stored, and a start in
	// between would write the login back. Guarded by starting.
	purged string
}

func NewDriver(
	bridges BridgeSource,
	agents *botagents.Service,
	credentials *agentcredential.Service,
	approval acpclient.ToolApprovalService,
	toolGateway toolmount.Gateway,
	logger *slog.Logger,
) *Driver {
	d := &Driver{
		bridges:     bridges,
		agents:      agents,
		credentials: credentials,
		approval:    approval,
		toolGateway: toolGateway,
		logger:      logger.With(slog.String("runtime", RuntimeType)),
		running:     map[string]*agentProcesses{},
		signIns:     map[string]*signIn{},
	}
	d.servers = servertable.New(d.startServer, d.logger)
	return d
}

var (
	_ external.Driver             = (*Driver)(nil)
	_ external.DependencyRequirer = (*Driver)(nil)
	_ external.BotResetter        = (*Driver)(nil)
	_ external.BotAgentResetter   = (*Driver)(nil)
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

// ResetBot recycles the bot's processes: new work starts on current
// configuration while running turns finish on the displaced process.
func (d *Driver) ResetBot(botID string) {
	prefix := botID + "\x00"
	d.servers.RecycleWhere(func(key string) bool { return strings.HasPrefix(key, prefix) })
}

func (d *Driver) ResetBotAgent(botID, botAgentID string) {
	d.servers.Recycle(serverKey(botID, botAgentID))
}

// CloseAll tears every process down for Server shutdown, including displaced
// ones still finishing turns: closing is what keeps a login Grok refreshed.
// Each close waits for its process to exit, so they run side by side.
func (d *Driver) CloseAll() {
	var wg sync.WaitGroup
	wg.Go(d.servers.CloseAll)
	wg.Go(d.closeSignIns)
	d.runningMu.Lock()
	for _, agent := range d.running {
		for _, srv := range agent.servers {
			wg.Go(func() { _ = srv.Close() })
		}
	}
	d.runningMu.Unlock()
	wg.Wait()
}

func serverKey(botID, botAgentID string) string { return botID + "\x00" + botAgentID }

// agentConfig reads the Agent's configuration without its credential, which
// only a starting process needs.
func (d *Driver) agentConfig(ctx context.Context, botID, botAgentID string) (Config, error) {
	agent, err := d.agents.Get(ctx, botID, botAgentID)
	if err != nil {
		return Config{}, err
	}
	return grokcfg.ParseAgentConfig(agent.Metadata)
}

// startServer is the server table's start hook.
func (d *Driver) startServer(ctx context.Context, key string) (servertable.Resource, error) {
	botID, botAgentID, _ := strings.Cut(key, "\x00")
	// The table runs two starts of one Agent at once when a reset displaces
	// one that is still starting. Each would decide about the shared login
	// file without seeing the other, so the second waits here.
	agent := d.processes(key)
	select {
	case agent.starting <- struct{}{}:
		defer func() { <-agent.starting }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	cfg, err := d.agentConfig(ctx, botID, botAgentID)
	if err != nil {
		return nil, err
	}
	// Before the credential is read: a process that held the login has
	// written its last refresh back by the time it has closed.
	if err := awaitLoginHolders(ctx, d.runningServers(key), cfg.Auth == grokcfg.AuthGrokAccount, predecessorDrainTimeout); err != nil {
		return nil, err
	}
	credential, err := d.credentials.ResolveForBotAgent(ctx, botID, botAgentID)
	if err != nil {
		return nil, external.CredentialError(err)
	}
	if credential.ID == agent.purged {
		return nil, external.CredentialError(agentcredential.ErrNotFound)
	}
	if cfg, err = credentialFor(cfg, credential); err != nil {
		return nil, err
	}
	client, err := d.bridges.MCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("workspace bridge for bot %s: %w", botID, err)
	}
	info, err := d.bridges.WorkspaceInfo(ctx, botID)
	if err != nil {
		return nil, err
	}
	launcher, err := d.resolveLauncher(ctx, botID)
	if err != nil {
		return nil, err
	}
	if err := prepareHome(ctx, client, botAgentID, cfg); err != nil {
		return nil, err
	}
	var login *loginSync
	if cfg.Auth == grokcfg.AuthGrokAccount {
		login = &loginSync{
			store: d.credentials, botID: botID, botAgentID: botAgentID, logger: d.logger, stored: credential,
			read: func(ctx context.Context) (string, error) { return readLogin(ctx, client, botAgentID) },
		}
	}
	// The process outlives the turn that happened to start it.
	srv, err := startServer(context.WithoutCancel(ctx), botID, grokHome(botAgentID), client, info, cfg.APIKey, login, launcher, d.logger)
	if err != nil {
		return nil, err
	}
	d.track(key, srv) //nolint:contextcheck // the process outlives the turn that started it.
	return srv, nil
}

// awaitLoginHolders waits until no earlier process of the Agent can touch
// the login file a start is about to depend on. A Grok account process reads
// and refreshes that file, and Grok prefers a stored login over XAI_API_KEY,
// so a start waits whenever it or an earlier process holds a login. API key
// processes share nothing a turn changes: they run side by side, and a
// displaced one finishes its turns. A predecessor still busy when timeout
// passes answers busy rather than holding the start for a whole turn.
func awaitLoginHolders(ctx context.Context, running []*server, signedIn bool, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, other := range running {
		if !signedIn && other.login == nil {
			continue
		}
		select {
		case <-other.Done():
		case <-deadline.C:
			return external.Fail(external.FailureRuntimeBusy, nil)
		case <-ctx.Done():
			return ctx.Err()
		}
		// Done closes when teardown begins; the login is final only once Close
		// has synced it back.
		_ = other.Close() //nolint:contextcheck // predecessor teardown and credential sync outlive this request.
	}
	return nil
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

func (d *Driver) processes(key string) *agentProcesses {
	d.runningMu.Lock()
	defer d.runningMu.Unlock()
	agent := d.running[key]
	if agent == nil {
		agent = &agentProcesses{starting: make(chan struct{}, 1), signingIn: make(chan struct{}, 1)}
		d.running[key] = agent
	}
	return agent
}

func (d *Driver) track(key string, srv *server) {
	agent := d.processes(key)
	d.runningMu.Lock()
	agent.servers = append(agent.servers, srv)
	d.runningMu.Unlock()
	go func() {
		<-srv.Done()
		// Listed until teardown has finished, not merely begun: a starting
		// process waits on this one through Close.
		_ = srv.Close()
		d.runningMu.Lock()
		defer d.runningMu.Unlock()
		agent.servers = slices.DeleteFunc(agent.servers, func(other *server) bool { return other == srv })
	}()
}

func (d *Driver) runningServers(key string) []*server {
	d.runningMu.Lock()
	defer d.runningMu.Unlock()
	if agent := d.running[key]; agent != nil {
		return slices.Clone(agent.servers)
	}
	return nil
}

// acquireServer returns the Agent's live process plus a release the caller
// must defer; the reference keeps it alive across a concurrent recycle.
func (d *Driver) acquireServer(ctx context.Context, botID, botAgentID string) (*server, func(), error) {
	info, err := d.bridges.WorkspaceInfo(ctx, botID)
	if err != nil {
		return nil, nil, err
	}
	if err := external.RequireContainerWorkspace(info, RuntimeType); err != nil {
		return nil, nil, err
	}
	key := serverKey(botID, botAgentID)
	if current := d.servers.Peek(key); current != nil && d.launchers != nil {
		launcher, err := d.resolveLauncher(ctx, botID)
		if err != nil {
			return nil, nil, err
		}
		if !current.(*server).matchesLauncher(launcher) {
			// A dependency update takes effect on new work; turns holding the
			// old process keep it alive until they finish.
			d.servers.RecycleResource(key, current)
		}
	}
	resource, release, err := d.servers.Acquire(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	return resource.(*server), release, nil
}

// resolveLauncher discovers the grok CLI copy without installing it.
func (d *Driver) resolveLauncher(ctx context.Context, botID string) (external.Launcher, error) {
	if d.launchers == nil {
		return external.Launcher{Path: defaultLauncherPath, Source: external.LauncherSourceToolkit}, nil
	}
	launcher, err := d.launchers.ResolveLauncher(ctx, botID, dependencyID)
	if err != nil {
		var missing *external.DependencyMissingError
		if errors.As(err, &missing) {
			return external.Launcher{}, &external.DependencyMissingError{
				DependencyID:        firstNonEmpty(missing.DependencyID, dependencyID),
				TaskID:              strings.TrimSpace(missing.TaskID),
				OperationInProgress: missing.OperationInProgress || strings.TrimSpace(missing.TaskID) != "",
			}
		}
		return external.Launcher{}, fmt.Errorf("resolve grok launcher for bot %s: %w", botID, err)
	}
	if strings.TrimSpace(launcher.Path) == "" {
		return external.Launcher{}, errs.New(fmt.Sprintf("resolve grok launcher for bot %s: resolver returned no path", botID))
	}
	return launcher, nil
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
	defer srv.login.sync(ctx)
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

	ctx, failPrompt := context.WithCancelCause(ctx)
	defer failPrompt(nil)
	th.turn.Store(&turnContext{ctx: ctx, input: input})
	defer th.turn.Store(nil)
	sink := acpclient.NewTurnSinkWithFailure(ctx, d.logger, input.Sink, input.ToolOutputLimit, failPrompt)
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
	if sinkErr := sink.Err(); sinkErr != nil {
		return out, sinkErr
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return out, cause
	}

	if err != nil {
		if ctx.Err() != nil {
			// A stop is not a failure: the partial transcript persists. The
			// session is reusable only once Grok confirmed the cancellation
			// and its permission callbacks have unwound; otherwise new work
			// moves to a fresh process and this one dies with its last turn.
			if errors.Is(err, acpclient.ErrPromptCancellationUnconfirmed) || !th.session.WaitDecisionCallbacksIdle(decisionQuiesceTimeout) {
				srv.dropThread(input.ThreadID, th, true) //nolint:contextcheck // teardown must outlive the stopped turn.
				d.servers.RecycleResource(serverKey(input.BotID, input.BotAgentID), srv)
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
