package application

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

type fakeRunner struct {
	gotReq ChatRequest
	chunks []string
	block  chan struct{} // when non-nil, stream waits before emitting
}

type testChatStreamer interface {
	StreamChat(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error)
}

type testChatStreamerFunc func(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error)

func (f testChatStreamerFunc) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	return f(ctx, req)
}

// scriptedAdmitter stands in for the session runtime so these tests can exercise
// what this package owns: the translation of an admission answer into the turn
// port's vocabulary, and the terminal write a finished run must produce. The
// answers themselves are scripted because their correctness is established where
// admission is implemented, not here.
type scriptedAdmitter struct {
	mu sync.Mutex

	// admitErr, when set, is what Admit returns instead of an admission.
	admitErr error
	// started reports whether the admission owns execution. False models a
	// replay of a run owned elsewhere or already finished.
	started bool

	inputs     []sessionruntime.AdmitInput
	finishes   []recordedFinish
	published  []native.StreamEvent
	publishErr error
}

type recordedFinish struct {
	handle  sessionruntime.RunHandle
	status  string
	message string
}

func newScriptedAdmitter() *scriptedAdmitter {
	return &scriptedAdmitter{started: true}
}

func (a *scriptedAdmitter) Admit(_ context.Context, in sessionruntime.AdmitInput) (sessionruntime.Admission, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inputs = append(a.inputs, in)
	if a.admitErr != nil {
		return sessionruntime.Admission{}, a.admitErr
	}
	runID := "run-" + strconv.Itoa(len(a.inputs))
	if !a.started {
		return sessionruntime.Admission{RunID: runID, Replay: true}, nil
	}
	return sessionruntime.Admission{
		RunID:        runID,
		TurnID:       "turn-" + strconv.Itoa(len(a.inputs)),
		TurnPosition: int64(len(a.inputs) - 1),
		Started:      true,
		Handle: sessionruntime.RunHandle{
			BotID:        in.BotID,
			SessionID:    in.SessionID,
			RunID:        runID,
			FencingToken: int64(len(a.inputs)),
		},
	}, nil
}

func (a *scriptedAdmitter) FinishRun(_ context.Context, handle sessionruntime.RunHandle, status, message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finishes = append(a.finishes, recordedFinish{handle: handle, status: status, message: message})
	return nil
}

func (a *scriptedAdmitter) PublishAgentEvent(
	_ context.Context,
	_ sessionruntime.RunHandle,
	event native.StreamEvent,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.published = append(a.published, event)
	return a.publishErr
}

func (a *scriptedAdmitter) admitted() []sessionruntime.AdmitInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.inputs)
}

// awaitFinish waits for the run's terminal write, which happens in the pump's
// defer stack and so can trail the closing of the handle's channels.
func (a *scriptedAdmitter) awaitFinish(t *testing.T) recordedFinish {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		finishes := slices.Clone(a.finishes)
		a.mu.Unlock()
		if len(finishes) > 0 {
			return finishes[len(finishes)-1]
		}
		if time.Now().After(deadline) {
			t.Fatal("run ended without a terminal write: the thread's slot is never released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTurnTestService(streamer testChatStreamer) *Service {
	service, _ := newAdmittedTurnTestService(streamer)
	return service
}

func newAdmittedTurnTestService(streamer testChatStreamer) (*Service, *scriptedAdmitter) {
	admitter := newScriptedAdmitter()
	return &Service{
		sessionRuntime:   admitter,
		publishTurnEvent: admitter.PublishAgentEvent,
		turnHooks: &turnRuntimeHooks{
			streamChat: streamer.StreamChat,
		},
	}, admitter
}

func (f *fakeRunner) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	f.gotReq = req
	ch := make(chan StreamChunk, len(f.chunks))
	errCh := make(chan error)
	go func() {
		defer close(ch)
		defer close(errCh)
		if f.block != nil {
			select {
			case <-f.block:
			case <-ctx.Done():
				return
			}
		}
		for _, c := range f.chunks {
			select {
			case ch <- StreamChunk(c):
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, errCh
}

func TestStartTurnRequiresTeamID(t *testing.T) {
	a := newTurnTestService(&fakeRunner{})
	_, err := a.StartTurn(context.Background(), turn.StartTurnCommand{Mode: turn.ModeChat})
	if err == nil {
		t.Fatal("want error for empty TeamID")
	}
}

// A command that cannot be admitted must not be started: without a durable
// admission there is no owner, no fencing token, and nothing that would ever
// drive the run to a terminal state.
func TestStartTurnRequiresSessionRuntime(t *testing.T) {
	service := &Service{
		turnHooks: &turnRuntimeHooks{
			streamChat: func(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error) {
				t.Fatal("unadmitted command must not reach the runtime")
				return nil, nil
			},
		},
	}
	if _, err := service.StartTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s",
	}); err == nil {
		t.Fatal("StartTurn() error = nil, want an unconfigured session runtime error")
	}
}

func TestStartTurnStreamsEvents(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"text_delta","text":"a"}`, `{"type":"done"}`}}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "team1", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", Query: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []turn.Event
	for e := range h.Events() {
		events = append(events, e)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].Kind != "text_delta" || events[1].Kind != "done" {
		t.Fatalf("kinds = %q, %q", events[0].Kind, events[1].Kind)
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seq not monotonic: %d, %d", events[0].Seq, events[1].Seq)
	}
	if string(events[0].Payload) != r.chunks[0] {
		t.Fatalf("payload mutated: %s", events[0].Payload)
	}
	if events[0].TeamID != "team1" || events[0].ThreadID != "s" {
		t.Fatalf("event context missing: %+v", events[0])
	}
	if h.RunID() == "" || events[0].RunID != h.RunID() {
		t.Fatalf("run id mismatch: handle=%q event=%q", h.RunID(), events[0].RunID)
	}
	if r.gotReq.BotID != "b" || r.gotReq.Query != "hi" {
		t.Fatalf("ChatRequest not translated: %+v", r.gotReq)
	}
	for range h.Errs() {
	}
}

func TestStartTurnPublishesNativeEventsBeforeCleanFinish(t *testing.T) {
	a, admitter := newAdmittedTurnTestService(&fakeRunner{chunks: []string{
		`{"type":"tool_approval_request","approval_id":"approval-1","status":"pending"}`,
		`{"type":"agent_end","approval_id":"approval-1","status":"pending"}`,
	}})
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainHandle(h)

	admitter.mu.Lock()
	published := append([]native.StreamEvent(nil), admitter.published...)
	admitter.mu.Unlock()
	if len(published) != 2 ||
		published[0].Type != native.EventToolApprovalRequest ||
		published[1].Type != native.EventAgentEnd {
		t.Fatalf("published events = %#v, want approval request then agent end", published)
	}
	if got := admitter.awaitFinish(t); got.status != "" {
		t.Fatalf("clean deferred finish status = %q, want unnamed runtime-derived status", got.status)
	}
}

func TestStartTurnFailsWhenRuntimeEventPublicationFails(t *testing.T) {
	cancelObserved := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCleanup) }) }
	t.Cleanup(release)
	streamer := testChatStreamerFunc(func(ctx context.Context, _ ChatRequest) (<-chan StreamChunk, <-chan error) {
		chunks := make(chan StreamChunk, 1)
		errs := make(chan error)
		chunks <- StreamChunk(`{"type":"text_delta","delta":"hello"}`)
		go func() {
			<-ctx.Done()
			close(cancelObserved)
			<-releaseCleanup
			close(chunks)
			close(errs)
		}()
		return chunks, errs
	})
	a, admitter := newAdmittedTurnTestService(streamer)
	publishErr := errors.New("private runtime publication failure")
	admitter.publishErr = publishErr
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("publication failure did not cancel the stream")
	}
	admitter.mu.Lock()
	finishedEarly := len(admitter.finishes) != 0
	admitter.mu.Unlock()
	if finishedEarly {
		t.Fatal("run finalized before publication-failure cleanup completed")
	}
	release()
	for range h.Events() {
	}
	var publicErr error
	for streamErr := range h.Errs() {
		publicErr = streamErr
	}

	if got := admitter.awaitFinish(t); got.status != sessionruntime.RunStatusErrored {
		t.Fatalf("publication failure status = %q, want %q", got.status, sessionruntime.RunStatusErrored)
	}
	if got := apperror.CodeOf(publicErr); got != apperror.CodeSessionHistoryInconsistent {
		t.Fatalf("publication failure code = %q, want %q", got, apperror.CodeSessionHistoryInconsistent)
	}
	if got := apperror.CauseOf(publicErr); !errors.Is(got, publishErr) {
		t.Fatalf("private publication cause = %v, want %v", got, publishErr)
	}
}

func TestInjectAndAssets(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"done"}`}, block: make(chan struct{})}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Inject(context.Background(), turn.InjectMessage{Text: "more"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-r.gotReq.InjectCh:
		if got.Text != "more" {
			t.Fatalf("inject text = %q", got.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("inject not delivered")
	}
	h.AddOutboundAssets([]turn.OutboundAssetRef{{ContentHash: "h1", Role: "attachment", Ordinal: 0}})
	refs := r.gotReq.OutboundAssetCollector()
	if len(refs) != 1 || refs[0].ContentHash != "h1" {
		t.Fatalf("assets = %+v", refs)
	}
	close(r.block)
	for range h.Events() {
	}
	for range h.Errs() {
	}
}

func TestCancelClosesEvents(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"done"}`}, block: make(chan struct{})}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat})
	if err != nil {
		t.Fatal(err)
	}
	h.Cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-h.Events():
			if !ok {
				return // closed as expected
			}
		case <-deadline:
			t.Fatal("events channel not closed after cancel")
		}
	}
}

func TestCancelWaitsForStreamCleanupBeforeFinalizingRun(t *testing.T) {
	cancelObserved, release := make(chan struct{}), make(chan struct{})
	streamer := testChatStreamerFunc(func(ctx context.Context, _ ChatRequest) (<-chan StreamChunk, <-chan error) {
		chunks, errs := make(chan StreamChunk), make(chan error)
		go func() {
			<-ctx.Done()
			close(cancelObserved)
			<-release
			close(chunks)
			close(errs)
		}()
		return chunks, errs
	})
	service, admitter := newAdmittedTurnTestService(streamer)
	h, err := service.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat})
	if err != nil {
		t.Fatal(err)
	}
	h.Cancel()
	<-cancelObserved
	admitter.mu.Lock()
	finishedEarly := len(admitter.finishes) != 0
	admitter.mu.Unlock()
	if finishedEarly {
		t.Fatal("run finalized before interrupted-step persistence could finish")
	}
	close(release)
	for range h.Events() {
	}
	if got := admitter.awaitFinish(t).status; got != sessionruntime.RunStatusAborted {
		t.Fatalf("finish status = %q, want aborted", got)
	}
}

func TestCommandFieldTranslation(t *testing.T) {
	r := &fakeRunner{}
	a := newTurnTestService(r)
	cmd := turn.StartTurnCommand{
		TeamID: "t", Mode: turn.ModeChat,
		BotID: "bot", ChatID: "chat", ThreadID: "sess", RouteID: "route",
		Token: "tok", ChatToken: "ctok", UserID: "u", SourceChannelIdentityID: "ci",
		DisplayName: "dn", ExternalMessageID: "ext", EventID: "ev",
		Query: "q", ModelQuery: "mq", UserMessageKind: "kind", UserVisibleText: "uvt",
		Attachments: []turn.Attachment{{Type: "image", ContentHash: "ch1", Name: "a.png"}},
		ReplyTarget: "rt", ConversationType: "group", ConversationName: "cn",
		SourceReplyToMessageID: "srm", ReplySender: "rs", ReplyPreview: "rp",
		ReplyAttachments: []turn.Attachment{{Type: "file"}},
		MentionsBot:      true, RepliesToBot: true,
		ForwardMessageID: "fm", ForwardFromUserID: "fu", ForwardFromConversationID: "fc",
		ForwardSender: "fs", ForwardDate: 42,
		CurrentChannel: "telegram", Channels: []string{"telegram"},
		Model: "m1", ReasoningEffort: "high", WorkspaceTargetID: "wt",
		SkillActivation:      &turn.SkillActivation{Prompt: "p", Skills: []turn.SkillActivationSkill{{Name: "sk"}}},
		RequestedSkills:      []turn.RequestedSkillContext{{Name: "rs1", ContentHash: "rh"}},
		SkipMemoryExtraction: true, SkipTitleGeneration: true, UserMessagePersisted: true,
	}
	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	for range h.Events() {
	}
	got := r.gotReq
	checks := map[string][2]string{
		"BotID":                     {got.BotID, "bot"},
		"ChatID":                    {got.ChatID, "chat"},
		"ThreadID":                  {got.ThreadID, "sess"},
		"RunID":                     {got.RunID, "run-1"},
		"TurnID":                    {got.TurnID, "turn-1"},
		"RouteID":                   {got.RouteID, "route"},
		"Token":                     {got.Token, "tok"},
		"ChatToken":                 {got.ChatToken, "ctok"},
		"UserID":                    {got.UserID, "u"},
		"SourceChannelIdentityID":   {got.SourceChannelIdentityID, "ci"},
		"DisplayName":               {got.DisplayName, "dn"},
		"ExternalMessageID":         {got.ExternalMessageID, "ext"},
		"EventID":                   {got.EventID, "ev"},
		"Query":                     {got.Query, "q"},
		"ModelQuery":                {got.ModelQuery, "mq"},
		"UserMessageKind":           {got.UserMessageKind, "kind"},
		"UserVisibleText":           {got.UserVisibleText, "uvt"},
		"ReplyTarget":               {got.ReplyTarget, "rt"},
		"ConversationType":          {got.ConversationType, "group"},
		"ConversationName":          {got.ConversationName, "cn"},
		"SourceReplyToMessageID":    {got.SourceReplyToMessageID, "srm"},
		"ReplySender":               {got.ReplySender, "rs"},
		"ReplyPreview":              {got.ReplyPreview, "rp"},
		"ForwardMessageID":          {got.ForwardMessageID, "fm"},
		"ForwardFromUserID":         {got.ForwardFromUserID, "fu"},
		"ForwardFromConversationID": {got.ForwardFromConversationID, "fc"},
		"ForwardSender":             {got.ForwardSender, "fs"},
		"CurrentChannel":            {got.CurrentChannel, "telegram"},
		"Model":                     {got.Model, "m1"},
		"ReasoningEffort":           {got.ReasoningEffort, "high"},
		"WorkspaceTargetID":         {got.WorkspaceTargetID, "wt"},
	}
	for name, pair := range checks {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}
	if !got.MentionsBot || !got.RepliesToBot || !got.SkipMemoryExtraction || !got.SkipTitleGeneration || !got.UserMessagePersisted {
		t.Error("bool fields not translated")
	}
	if got.ForwardDate != 42 {
		t.Errorf("ForwardDate = %d", got.ForwardDate)
	}
	if got.TurnPosition == nil || *got.TurnPosition != 0 {
		t.Errorf("TurnPosition = %v, want 0", got.TurnPosition)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].ContentHash != "ch1" || got.Attachments[0].Name != "a.png" {
		t.Errorf("Attachments = %+v", got.Attachments)
	}
	if len(got.ReplyAttachments) != 1 || got.ReplyAttachments[0].Type != "file" {
		t.Errorf("ReplyAttachments = %+v", got.ReplyAttachments)
	}
	if got.SkillActivation == nil || got.SkillActivation.Prompt != "p" || len(got.SkillActivation.Skills) != 1 {
		t.Errorf("SkillActivation = %+v", got.SkillActivation)
	}
	if len(got.RequestedSkills) != 1 || got.RequestedSkills[0].Name != "rs1" || got.RequestedSkills[0].ContentHash != "rh" {
		t.Errorf("RequestedSkills = %+v", got.RequestedSkills)
	}
	if len(got.Channels) != 1 || got.Channels[0] != "telegram" {
		t.Errorf("Channels = %+v", got.Channels)
	}
}

func TestBoundaryValuesPassThrough(t *testing.T) {
	metadata := map[string]any{"key": "value"}
	attachment := turn.Attachment{
		Type: "image", Base64: "base64", Path: "/tmp/a", URL: "https://example.test/a",
		PlatformKey: "platform", ContentHash: "hash", Name: "a.png", Mime: "image/png",
		Size: 42, Metadata: metadata,
	}
	activation := &turn.SkillActivation{
		Skills: []turn.SkillActivationSkill{{
			Name: "skill", DisplayName: "Skill", Description: "desc",
			SourceKind: "registry", State: "effective",
		}},
		Prompt: "prompt",
	}
	requested := []turn.RequestedSkillContext{{
		Name: "skill", Description: "desc", Content: "body", SourceKind: "registry",
		OpaqueSourceID: "opaque", ContentHash: "hash", Identity: "identity",
	}}
	request := chatRequestFromCommand(turn.StartTurnCommand{
		ReplyAttachments: []turn.Attachment{attachment},
		Attachments:      []turn.Attachment{attachment},
		SkillActivation:  activation,
		RequestedSkills:  requested,
	})
	if !reflect.DeepEqual(request.ReplyAttachments, []turn.Attachment{attachment}) {
		t.Errorf("reply attachments = %#v", request.ReplyAttachments)
	}
	if !reflect.DeepEqual(request.Attachments, []turn.Attachment{attachment}) {
		t.Errorf("attachments = %#v", request.Attachments)
	}
	if request.SkillActivation != activation {
		t.Error("skill activation pointer was not passed through")
	}
	if !reflect.DeepEqual(request.RequestedSkills, requested) {
		t.Errorf("requested skills = %#v, want %#v", request.RequestedSkills, requested)
	}

	answers := []turn.QuestionAnswer{{
		QuestionID: "question", OptionIDs: []string{"a"}, CustomText: "custom",
		Text: "text", Skipped: true,
	}}
	wantAnswers := []userinput.QuestionAnswer{{
		QuestionID: "question", OptionIDs: []string{"a"}, CustomText: "custom",
		Text: "text", Skipped: true,
	}}
	if got := questionAnswersToUserInput(answers); !reflect.DeepEqual(got, wantAnswers) {
		t.Errorf("question answer conversion = %#v, want %#v", got, wantAnswers)
	}
}

func TestStartTurnRejectsForeignTeam(t *testing.T) {
	a := newTurnTestService(&fakeRunner{})
	a.SetAllowedTeam("team-home")
	_, err := a.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-other", Mode: turn.ModeChat})
	if !errors.Is(err, turn.ErrTeamNotServed) {
		t.Fatalf("err = %v, want ErrTeamNotServed", err)
	}
	if _, err := a.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-home", Mode: turn.ModeChat}); err != nil {
		t.Fatalf("home team rejected: %v", err)
	}
}

// The platform's message identity must reach admission unchanged: it is what
// makes a webhook redelivery the same invocation instead of a second turn.
func TestStartTurnAdmitsUnderTheIdempotencyKey(t *testing.T) {
	a, admitter := newAdmittedTurnTestService(&fakeRunner{})
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", IdempotencyKey: "telegram:route-1:42",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainHandle(h)

	admitted := admitter.admitted()
	if len(admitted) != 1 {
		t.Fatalf("admissions = %d, want 1", len(admitted))
	}
	if admitted[0].InvocationID != "telegram:route-1:42" {
		t.Fatalf("invocation id = %q, want the command's idempotency key", admitted[0].InvocationID)
	}
	if admitted[0].BotID != "b" || admitted[0].SessionID != "s" {
		t.Fatalf("admitted %q/%q, want b/s", admitted[0].BotID, admitted[0].SessionID)
	}
	if h.RunID() != "run-1" {
		t.Fatalf("run id = %q, want the admitted run id", h.RunID())
	}
}

// A source with no stable message identity has nothing to deduplicate against,
// so each attempt must be admitted as its own submission rather than colliding
// with the previous one under a shared empty id.
func TestStartTurnMintsInvocationIDWhenKeyIsAbsent(t *testing.T) {
	a, admitter := newAdmittedTurnTestService(&fakeRunner{})
	for range 2 {
		h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{
			TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s",
		})
		if err != nil {
			t.Fatal(err)
		}
		drainHandle(h)
	}
	admitted := admitter.admitted()
	if len(admitted) != 2 {
		t.Fatalf("admissions = %d, want 2", len(admitted))
	}
	for _, in := range admitted {
		if in.InvocationID == "" {
			t.Fatal("admission reached the runtime without an invocation id")
		}
	}
	if admitted[0].InvocationID == admitted[1].InvocationID {
		t.Fatalf("both attempts minted %q: separate submissions must not share a retry identity", admitted[0].InvocationID)
	}
}

// Validation that rejects a command must run before admission. Admitting first
// would persist a run for a turn nothing can execute, and it would hold the
// thread's only slot until the reaper timed it out.
func TestStartTurnRejectsUnconfiguredModeBeforeAdmission(t *testing.T) {
	a, admitter := newAdmittedTurnTestService(&fakeRunner{})
	_, err := a.StartTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "t", Mode: turn.ModeDiscuss, BotID: "b", ThreadID: "s", IdempotencyKey: "msg-1",
	})
	if err == nil {
		t.Fatal("expected unconfigured discuss runtime error")
	}
	if admitted := admitter.admitted(); len(admitted) != 0 {
		t.Fatalf("admissions = %d, want 0 for a command that cannot run", len(admitted))
	}
}

// The two rejections a channel adapter must tell apart: busy is retryable and
// nothing was persisted, while a run that already exists has been answered and
// its redelivery must be dropped.
func TestStartTurnTranslatesAdmissionRejections(t *testing.T) {
	cmd := turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", IdempotencyKey: "msg-1"}

	for _, tc := range []struct {
		name     string
		admitErr error
		started  bool
		want     error
	}{
		{name: "busy", admitErr: sessionruntime.ErrSessionBusy, want: turn.ErrSessionBusy},
		{name: "conflict", admitErr: sessionruntime.ErrInvocationConflict, want: turn.ErrDuplicateTurn},
		{name: "replay of a run this call does not own", started: false, want: turn.ErrDuplicateTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, admitter := newAdmittedTurnTestService(&fakeRunner{})
			admitter.admitErr = tc.admitErr
			admitter.started = tc.started
			if _, err := a.StartTurn(context.Background(), cmd); !errors.Is(err, tc.want) {
				t.Fatalf("StartTurn() error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCancelUnblocksFullEventBuffer reproduces the reviewer's 32-event
// burst: with no consumer and a full buffer, Cancel must still unblock
// the pump and close both channels.
func TestCancelUnblocksFullEventBuffer(t *testing.T) {
	chunks := make([]string, 40)
	for i := range chunks {
		chunks[i] = `{"type":"text_delta","delta":"x"}`
	}
	r := &fakeRunner{chunks: chunks}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the pump fill the buffer
	h.Cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-h.Events():
			if !ok {
				for range h.Errs() {
				}
				return
			}
		case <-deadline:
			t.Fatal("events channel not closed after cancel with full buffer")
		}
	}
}

// errRunner streams optional chunks then reports an error, mimicking a
// application service whose provider failed mid-stream.
type errRunner struct {
	chunks []string
	err    error
}

func (f *errRunner) StreamChat(ctx context.Context, _ ChatRequest) (<-chan StreamChunk, <-chan error) {
	ch := make(chan StreamChunk, len(f.chunks))
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, c := range f.chunks {
			select {
			case ch <- StreamChunk(c):
			case <-ctx.Done():
				return
			}
		}
		if f.err != nil {
			errCh <- f.err
		}
	}()
	return ch, errCh
}

type canceledRunReporter struct{}

func (*canceledRunReporter) StreamChat(ctx context.Context, _ ChatRequest) (<-chan StreamChunk, <-chan error) {
	ch := make(chan StreamChunk)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		<-ctx.Done()
		errCh <- context.Cause(ctx)
	}()
	return ch, errCh
}

type canceledRunTerminalReporter struct{}

func (*canceledRunTerminalReporter) StreamChat(ctx context.Context, _ ChatRequest) (<-chan StreamChunk, <-chan error) {
	ch := make(chan StreamChunk, 1)
	errCh := make(chan error)
	go func() {
		defer close(ch)
		defer close(errCh)
		<-ctx.Done()
		ch <- StreamChunk(`{"type":"agent_abort"}`)
	}()
	return ch, errCh
}

func drainHandle(h turn.RunHandle) {
	for range h.Events() {
	}
	for range h.Errs() {
	}
}

// TestRunEndClosesInjectChannel pins the fix for the per-turn goroutine
// leak: the application's inject-forwarding goroutine exits by ranging over
// InjectCh, so the service must close it when the run ends.
func TestRunEndClosesInjectChannel(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat})
	if err != nil {
		t.Fatal(err)
	}
	drainHandle(h)
	select {
	case _, ok := <-r.gotReq.InjectCh:
		if ok {
			t.Fatal("expected closed inject channel, got a message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inject channel not closed after run end")
	}
	if err := h.Inject(context.Background(), turn.InjectMessage{Text: "late"}); err == nil {
		t.Fatal("expected error injecting after run end")
	}
}

// Every run must close its durable record, and the three outcomes must stay
// distinguishable: the record is what releases the thread's single slot, and a
// run recorded as broken when it was merely stopped misreports the turn.
func TestRunEndRecordsTerminalState(t *testing.T) {
	cmd := turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", IdempotencyKey: "msg-1"}

	t.Run("error", func(t *testing.T) {
		a, admitter := newAdmittedTurnTestService(&errRunner{err: errors.New("provider exploded")})
		h, err := a.StartTurn(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		drainHandle(h)
		if got := admitter.awaitFinish(t); got.status != sessionruntime.RunStatusErrored {
			t.Fatalf("status = %q, want %q", got.status, sessionruntime.RunStatusErrored)
		}
	})

	t.Run("unsolicited context cancellation", func(t *testing.T) {
		a, admitter := newAdmittedTurnTestService(&errRunner{err: context.Canceled})
		h, err := a.StartTurn(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		drainHandle(h)
		if got := admitter.awaitFinish(t); got.status != sessionruntime.RunStatusErrored {
			t.Fatalf("status = %q, want %q", got.status, sessionruntime.RunStatusErrored)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		a, admitter := newAdmittedTurnTestService(&fakeRunner{
			chunks: []string{`{"type":"done"}`},
			block:  make(chan struct{}),
		})
		h, err := a.StartTurn(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		h.Cancel()
		drainHandle(h)
		// A canceled run reaches the pump as two closed channels, exactly like a
		// clean finish; only the run context tells them apart.
		if got := admitter.awaitFinish(t); got.status != sessionruntime.RunStatusAborted {
			t.Fatalf("status = %q, want %q", got.status, sessionruntime.RunStatusAborted)
		}
	})

	t.Run("cancellation reported by stream", func(t *testing.T) {
		a, admitter := newAdmittedTurnTestService(&canceledRunReporter{})
		h, err := a.StartTurn(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		h.Cancel()
		for range h.Events() {
		}
		for streamErr := range h.Errs() {
			t.Fatalf("canceled run exposed stream error: %v", streamErr)
		}
		if got := admitter.awaitFinish(t); got.status != sessionruntime.RunStatusAborted {
			t.Fatalf("status = %q, want %q", got.status, sessionruntime.RunStatusAborted)
		}
	})

	t.Run("cancellation reported by detached terminal event", func(t *testing.T) {
		a, admitter := newAdmittedTurnTestService(&canceledRunTerminalReporter{})
		a.publishTurnEvent = func(
			ctx context.Context,
			handle sessionruntime.RunHandle,
			event native.StreamEvent,
		) error {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return admitter.PublishAgentEvent(ctx, handle, event)
		}
		h, err := a.StartTurn(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		h.Cancel()
		for range h.Events() {
		}
		for streamErr := range h.Errs() {
			t.Fatalf("canceled run exposed stream error: %v", streamErr)
		}
		if got := admitter.awaitFinish(t); got.status != sessionruntime.RunStatusAborted {
			t.Fatalf("status = %q, want %q", got.status, sessionruntime.RunStatusAborted)
		}
	})

	t.Run("completion", func(t *testing.T) {
		a, admitter := newAdmittedTurnTestService(&fakeRunner{chunks: []string{`{"type":"done"}`}})
		h, err := a.StartTurn(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		drainHandle(h)
		got := admitter.awaitFinish(t)
		if got.status != "" {
			t.Fatalf("status = %q, want unnamed runtime-derived completion", got.status)
		}
		if got.handle.FencingToken == 0 {
			t.Fatal("terminal write carried no fencing token: a superseded owner could close this run")
		}
	})
}

func TestRecordStreamFailureClassifiesOnlyExplicitCancellationAsAborted(t *testing.T) {
	providerErr := errors.New("provider failed")
	tests := []struct {
		name         string
		contextCause error
		err          error
		wantStored   bool
	}{
		{
			name:         "explicit cancellation",
			contextCause: context.Canceled,
			err:          context.Canceled,
		},
		{
			name:         "wrapped explicit cancellation",
			contextCause: context.Canceled,
			err:          runtimeHistoryError(context.Canceled),
		},
		{
			name:       "provider cancellation with active context",
			err:        context.Canceled,
			wantStored: true,
		},
		{
			name:         "ownership loss",
			contextCause: sessionruntime.ErrRunOwnershipLost,
			err:          context.Canceled,
			wantStored:   true,
		},
		{
			name:       "provider failure",
			err:        providerErr,
			wantStored: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.contextCause != nil {
				var cancel context.CancelCauseFunc
				ctx, cancel = context.WithCancelCause(ctx)
				cancel(tt.contextCause)
			}
			h := &runHandle{ctx: ctx}
			reported := h.recordStreamFailure(tt.err)

			if !h.failed.Load() {
				t.Fatal("stream failure did not mark the run failed")
			}
			if tt.wantStored && !errors.Is(h.streamErr, tt.err) {
				t.Fatalf("stored error = %v, want %v", h.streamErr, tt.err)
			}
			if !tt.wantStored && h.streamErr != nil {
				t.Fatalf("stored error = %v, want explicit cancellation suppressed", h.streamErr)
			}
			if reported != tt.wantStored {
				t.Fatalf("reported = %v, want %v", reported, tt.wantStored)
			}
		})
	}
}

// TestDiscussInjectFailsFast: discuss handles have no inject reader, so
// Inject must fail immediately instead of blocking until the run ends.
func TestDiscussInjectFailsFast(t *testing.T) {
	h := newDiscussHandle(context.Background(), turn.StartTurnCommand{TeamID: "t"}, func() {}, "run-1", nil)
	done := make(chan error, 1)
	go func() { done <- h.Inject(context.Background(), turn.InjectMessage{Text: "x"}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected discuss inject to fail")
		}
	case <-time.After(time.Second):
		t.Fatal("discuss inject blocked instead of failing fast")
	}
}
