package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

type fakeRunner struct {
	gotReq ChatRequest
	chunks []string
	block  chan struct{} // when non-nil, stream waits before emitting
}

type testChatStreamer interface {
	StreamChat(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error)
}

func newTurnTestService(streamer testChatStreamer) *Service {
	return &Service{
		turnIdempotency: newIdempotencyRegistry(idempotencyCapacity),
		turnHooks: &turnRuntimeHooks{
			streamChat: streamer.StreamChat,
		},
	}
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
	_, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{Mode: agentdomain.ModeChat})
	if err == nil {
		t.Fatal("want error for empty TeamID")
	}
}

func TestStartTurnInitializesIdempotencyRegistryConcurrently(t *testing.T) {
	service := &Service{
		turnHooks: &turnRuntimeHooks{
			streamChat: func(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error) {
				chunks := make(chan StreamChunk)
				errs := make(chan error)
				close(chunks)
				close(errs)
				return chunks, errs
			},
		},
	}

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle, err := service.StartTurn(context.Background(), agentdomain.StartTurnCommand{
				TeamID:         "team-1",
				IdempotencyKey: fmt.Sprintf("message-%d", i),
			})
			if err != nil {
				t.Errorf("start turn: %v", err)
				return
			}
			for range handle.Events() {
			}
			for err := range handle.Errs() {
				t.Errorf("turn error: %v", err)
			}
		}()
	}
	wg.Wait()
	if service.turnIdempotency == nil {
		t.Fatal("idempotency registry was not initialized")
	}
}

func TestStartTurnStreamsEvents(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"text_delta","text":"a"}`, `{"type":"done"}`}}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{
		TeamID: "team1", Mode: agentdomain.ModeChat, BotID: "b", ThreadID: "s", Query: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []agentdomain.Event
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

func TestInjectAndAssets(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"done"}`}, block: make(chan struct{})}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Inject(context.Background(), agentdomain.InjectMessage{Text: "more"}); err != nil {
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
	h.AddOutboundAssets([]agentdomain.OutboundAssetRef{{ContentHash: "h1", Role: "attachment", Ordinal: 0}})
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
	h, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat})
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

func TestCommandFieldTranslation(t *testing.T) {
	r := &fakeRunner{}
	a := newTurnTestService(r)
	cmd := agentdomain.StartTurnCommand{
		TeamID: "t", Mode: agentdomain.ModeChat,
		BotID: "bot", ChatID: "chat", ThreadID: "sess", RouteID: "route",
		Token: "tok", ChatToken: "ctok", UserID: "u", SourceChannelIdentityID: "ci",
		DisplayName: "dn", AvatarURL: "https://example.com/avatar.png", ExternalMessageID: "ext", EventID: "ev",
		Query: "q", ModelQuery: "mq", UserMessageKind: "kind", UserVisibleText: "uvt",
		Attachments: []agentdomain.Attachment{{Type: "image", ContentHash: "ch1", Name: "a.png"}},
		ReplyTarget: "rt", ConversationType: "group", ConversationName: "cn",
		SourceReplyToMessageID: "srm", ReplySender: "rs", ReplyPreview: "rp",
		ReplyAttachments: []agentdomain.Attachment{{Type: "file"}},
		MentionsBot:      true, RepliesToBot: true,
		ForwardMessageID: "fm", ForwardFromUserID: "fu", ForwardFromConversationID: "fc",
		ForwardSender: "fs", ForwardDate: 42,
		CurrentChannel: "telegram", Channels: []string{"telegram"},
		Model: "m1", ReasoningEffort: "high", WorkspaceTargetID: "wt",
		SkillActivation:      &agentdomain.SkillActivation{Prompt: "p", Skills: []agentdomain.SkillActivationSkill{{Name: "sk"}}},
		RequestedSkills:      []agentdomain.RequestedSkillContext{{Name: "rs1", ContentHash: "rh"}},
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
		"RouteID":                   {got.RouteID, "route"},
		"Token":                     {got.Token, "tok"},
		"ChatToken":                 {got.ChatToken, "ctok"},
		"UserID":                    {got.UserID, "u"},
		"SourceChannelIdentityID":   {got.SourceChannelIdentityID, "ci"},
		"DisplayName":               {got.DisplayName, "dn"},
		"AvatarURL":                 {got.AvatarURL, "https://example.com/avatar.png"},
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
	if len(got.Attachments) != 1 || got.Attachments[0].ContentHash != "ch1" || got.Attachments[0].Name != "a.png" {
		t.Errorf("Attachments = %+v", got.Attachments)
	}
	if len(got.ReplyAttachments) != 1 || got.ReplyAttachments[0].Type != "file" {
		t.Errorf("ReplyAttachments = %+v", got.ReplyAttachments)
	}
	if got.SkillActivation == nil || got.SkillActivation.Prompt != "p" || len(got.SkillActivation.Skills) != 1 {
		t.Errorf("agentdomain.SkillActivation = %+v", got.SkillActivation)
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
	attachment := agentdomain.Attachment{
		Type: "image", Base64: "base64", Path: "/tmp/a", URL: "https://example.test/a",
		PlatformKey: "platform", ContentHash: "hash", Name: "a.png", Mime: "image/png",
		Size: 42, Metadata: metadata,
	}
	activation := &agentdomain.SkillActivation{
		Skills: []agentdomain.SkillActivationSkill{{
			Name: "skill", DisplayName: "Skill", Description: "desc",
			SourceKind: "plugin", State: "effective",
		}},
		Prompt: "prompt",
	}
	requested := []agentdomain.RequestedSkillContext{{
		Name: "skill", Description: "desc", Content: "body", SourceKind: "plugin",
		OpaqueSourceID: "opaque", ContentHash: "hash", Identity: "identity",
	}}
	request := chatRequestFromCommand(agentdomain.StartTurnCommand{
		ReplyAttachments: []agentdomain.Attachment{attachment},
		Attachments:      []agentdomain.Attachment{attachment},
		SkillActivation:  activation,
		RequestedSkills:  requested,
	})
	if !reflect.DeepEqual(request.ReplyAttachments, []agentdomain.Attachment{attachment}) {
		t.Errorf("reply attachments = %#v", request.ReplyAttachments)
	}
	if !reflect.DeepEqual(request.Attachments, []agentdomain.Attachment{attachment}) {
		t.Errorf("attachments = %#v", request.Attachments)
	}
	if request.SkillActivation != activation {
		t.Error("skill activation pointer was not passed through")
	}
	if !reflect.DeepEqual(request.RequestedSkills, requested) {
		t.Errorf("requested skills = %#v, want %#v", request.RequestedSkills, requested)
	}

	answers := []agentdomain.QuestionAnswer{{
		QuestionID: "question", OptionIDs: []string{"a"}, CustomText: "custom",
		Text: "text", Skipped: true,
	}}
	gotInput := userInputInputFromResponse(agentdomain.UserInputResponse{Answers: answers})
	if !reflect.DeepEqual(gotInput.Answers, answers) {
		t.Errorf("question answers = %#v, want %#v", gotInput.Answers, answers)
	}
}

func TestStartTurnRejectsForeignTeam(t *testing.T) {
	a := newTurnTestService(&fakeRunner{})
	a.SetAllowedTeam("team-home")
	_, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{TeamID: "team-other", Mode: agentdomain.ModeChat})
	if !errors.Is(err, agentdomain.ErrTeamNotServed) {
		t.Fatalf("err = %v, want ErrTeamNotServed", err)
	}
	if _, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{TeamID: "team-home", Mode: agentdomain.ModeChat}); err != nil {
		t.Fatalf("home team rejected: %v", err)
	}
}

func TestStartTurnClaimsIdempotencyKey(t *testing.T) {
	a := newTurnTestService(&fakeRunner{})
	first, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{
		TeamID: "t", Mode: agentdomain.ModeChat, IdempotencyKey: "msg-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range first.Events() {
	}
	// Redelivery of the same platform message must be rejected even after
	// the first run completed.
	if _, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{
		TeamID: "t", Mode: agentdomain.ModeChat, IdempotencyKey: "msg-1",
	}); !errors.Is(err, agentdomain.ErrDuplicateTurn) {
		t.Fatalf("err = %v, want ErrDuplicateTurn", err)
	}
	// Same key under another team is a distinct claim.
	if _, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{
		TeamID: "t2", Mode: agentdomain.ModeChat, IdempotencyKey: "msg-1",
	}); err != nil {
		t.Fatalf("cross-team claim rejected: %v", err)
	}
	// Empty keys are never deduplicated.
	for range 2 {
		if _, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat}); err != nil {
			t.Fatalf("empty key rejected: %v", err)
		}
	}
}

func TestFailedStartDoesNotClaimIdempotencyKey(t *testing.T) {
	a := newTurnTestService(&fakeRunner{})
	cmd := agentdomain.StartTurnCommand{
		TeamID: "t", Mode: agentdomain.ModeDiscuss, IdempotencyKey: "msg-1",
	}
	for range 2 {
		_, err := a.StartTurn(context.Background(), cmd)
		if err == nil {
			t.Fatal("expected unconfigured discuss runtime error")
		}
		if errors.Is(err, agentdomain.ErrDuplicateTurn) {
			t.Fatalf("failed start claimed idempotency key: %v", err)
		}
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
	h, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat})
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

func drainHandle(h agentdomain.RunHandle) {
	for range h.Events() {
	}
	for range h.Errs() {
	}
}

// retryStartTurn retries a duplicate-rejected StartTurn until the claim is
// released (or the deadline passes), returning the final error.
func retryStartTurn(t *testing.T, a *Service, cmd agentdomain.StartTurnCommand) error {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h, err := a.StartTurn(context.Background(), cmd)
		if err == nil {
			drainHandle(h)
			return nil
		}
		if !errors.Is(err, agentdomain.ErrDuplicateTurn) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunEndClosesInjectChannel pins the fix for the per-turn goroutine
// leak: the application's inject-forwarding goroutine exits by ranging over
// InjectCh, so the service must close it when the run ends.
func TestRunEndClosesInjectChannel(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	a := newTurnTestService(r)
	h, err := a.StartTurn(context.Background(), agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat})
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
	if err := h.Inject(context.Background(), agentdomain.InjectMessage{Text: "late"}); err == nil {
		t.Fatal("expected error injecting after run end")
	}
}

// TestFailedRunReleasesIdempotencyKey: a run that ends in an error must
// free its claim so the platform redelivery retries instead of being
// swallowed as a duplicate.
func TestFailedRunReleasesIdempotencyKey(t *testing.T) {
	a := newTurnTestService(&errRunner{err: errors.New("provider exploded")})
	cmd := agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat, IdempotencyKey: "msg-1"}
	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainHandle(h)
	if err := retryStartTurn(t, a, cmd); err != nil {
		t.Fatalf("claim not released after failed run: %v", err)
	}
}

// TestCanceledRunReleasesIdempotencyKey: cancellation (consumer stop or a
// transport disconnect tearing down the run context) must also free the
// claim — the application reacts to ctx cancellation by closing both channels,
// which is indistinguishable from clean completion inside the pump loop.
func TestCanceledRunReleasesIdempotencyKey(t *testing.T) {
	r := &fakeRunner{chunks: []string{`{"type":"done"}`}, block: make(chan struct{})}
	a := newTurnTestService(r)
	cmd := agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat, IdempotencyKey: "msg-2"}
	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	h.Cancel()
	drainHandle(h)
	r2 := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	a.turnHooks.streamChat = r2.StreamChat
	if err := retryStartTurn(t, a, cmd); err != nil {
		t.Fatalf("claim not released after canceled run: %v", err)
	}
}

// TestCompletedRunKeepsIdempotencyKey guards the inverse: clean completion
// must keep the claim so true redeliveries stay deduplicated.
func TestCompletedRunKeepsIdempotencyKey(t *testing.T) {
	a := newTurnTestService(&fakeRunner{chunks: []string{`{"type":"done"}`}})
	cmd := agentdomain.StartTurnCommand{TeamID: "t", Mode: agentdomain.ModeChat, IdempotencyKey: "msg-3"}
	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainHandle(h)
	time.Sleep(50 * time.Millisecond) // give a buggy release a chance to run
	if _, err := a.StartTurn(context.Background(), cmd); !errors.Is(err, agentdomain.ErrDuplicateTurn) {
		t.Fatalf("err = %v, want ErrDuplicateTurn after clean completion", err)
	}
}

// TestDiscussInjectFailsFast: discuss handles have no inject reader, so
// Inject must fail immediately instead of blocking until the run ends.
func TestDiscussInjectFailsFast(t *testing.T) {
	h := newDiscussHandle(context.Background(), agentdomain.StartTurnCommand{TeamID: "t"}, func() {}, nil)
	done := make(chan error, 1)
	go func() { done <- h.Inject(context.Background(), agentdomain.InjectMessage{Text: "x"}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected discuss inject to fail")
		}
	case <-time.After(time.Second):
		t.Fatal("discuss inject blocked instead of failing fast")
	}
}
