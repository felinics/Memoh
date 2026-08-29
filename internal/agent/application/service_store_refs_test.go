package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

func TestToProviderMessagesKeepsPersistedContentAndSourceAligned(t *testing.T) {
	t.Parallel()

	persisted := []messagepkg.Message{
		storedMemoryTestMessage(t, "msg-a", "sess-1", "assistant", "first stored answer"),
		storedMemoryTestMessage(t, "msg-b", "sess-1", "assistant", "second stored answer"),
	}
	got := toProviderMessages(persisted)
	if len(got) != 2 {
		t.Fatalf("provider messages = %v, want two persisted messages", got)
	}
	wantRefs := []string{"sess-1/msg-a", "sess-1/msg-b"}
	for i, want := range wantRefs {
		if got[i].SourceMessageID != want {
			t.Fatalf("provider message %d source = %q, want %q", i, got[i].SourceMessageID, want)
		}
	}
	if got[0].Content != "first stored answer" || got[1].Content != "second stored answer" {
		t.Fatalf("provider message content is misaligned: %v", got)
	}
}

func TestToProviderMessagesUsesPrunedPersistedContent(t *testing.T) {
	t.Parallel()

	service := &Service{
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}
	unit := "large tool result "
	huge := strings.Repeat(unit, gatewayToolPayloadMaxBytes/len(unit)+2)
	inputs, err := service.buildPersistInputs(context.Background(), ChatRequest{
		BotID: storeRoundBotID, ThreadID: "session-1",
	}, []ModelMessage{{Role: "tool", Content: newTextContent(huge)}}, "", storeRoundOptions{})
	if err != nil {
		t.Fatalf("buildPersistInputs() error = %v", err)
	}
	got := toProviderMessages([]messagepkg.Message{{
		ID: "msg-tool", SessionID: "session-1", Role: "tool", Content: inputs[0].Content,
	}})
	if len(got) != 1 {
		t.Fatalf("provider messages = %v, want one persisted tool message", got)
	}
	if got[0].Content == huge || !strings.Contains(got[0].Content, gatewayToolPayloadPrunedMarker) {
		t.Fatalf("provider message did not use pruned persisted content: %.120q", got[0].Content)
	}
}

type refsRecordingMessageService struct {
	recordingMessageService
	persistCount int
}

type partialRefsMessageService struct {
	refsRecordingMessageService
}

func (s *partialRefsMessageService) Persist(ctx context.Context, input messagepkg.PersistInput) (messagepkg.Message, error) {
	if s.persistCount == 1 {
		s.persistCount++
		return messagepkg.Message{}, errors.New("injected second-message failure")
	}
	return s.refsRecordingMessageService.Persist(ctx, input)
}

func (s *refsRecordingMessageService) Persist(ctx context.Context, input messagepkg.PersistInput) (messagepkg.Message, error) {
	msg, err := s.recordingMessageService.Persist(ctx, input)
	if err != nil {
		return msg, err
	}
	s.persistCount++
	msg.ID = fmt.Sprintf("msg-%d", s.persistCount)
	return msg, nil
}

func TestStoreRoundPassesSourceRefsToMemory(t *testing.T) {
	t.Parallel()

	messages := &refsRecordingMessageService{}
	memory := &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 1)}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, memory)
	resolver := &Service{
		messageService:  messages,
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}

	if err := resolver.storeRound(context.Background(), ChatRequest{
		BotID:    storeRoundBotID,
		ThreadID: "session-1",
		Query:    "hello",
	}, []ModelMessage{
		{Role: "user", Content: newTextContent("hello")},
		{Role: "assistant", Content: newTextContent("hi there")},
	}, "model-1"); err != nil {
		t.Fatalf("storeRound error: %v", err)
	}

	select {
	case got := <-memory.afterChat:
		want := []string{"session-1/msg-1", "session-1/msg-2"}
		refs := make([]string, 0, len(got.Messages))
		for _, message := range got.Messages {
			refs = append(refs, message.SourceMessageID)
		}
		if !slices.Equal(refs, want) {
			t.Fatalf("AfterChatRequest message sources = %v, want %v", refs, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("memory provider was not called")
	}
}

func TestStoreMemoryUsesPersistedUserContent(t *testing.T) {
	t.Parallel()

	user := storedMemoryTestMessage(t, "msg-user", "session-1", "user", "persisted user question")
	messages := &recordingMessageService{persisted: []messagepkg.PersistInput{{
		SessionID: user.SessionID, Role: user.Role, Content: user.Content,
	}}}
	memory := &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 1)}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, memory)
	service := &Service{
		messageService:  messages,
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}

	service.storeMemory(context.Background(), ChatRequest{
		BotID: storeRoundBotID, ThreadID: "session-1",
		Query:                "runtime query with generated headers",
		UserMessagePersisted: true, PersistedUserMessageID: "msg-user",
	}, []messagepkg.Message{
		storedMemoryTestMessage(t, "msg-assistant", "session-1", "assistant", "stored answer"),
	})

	got := <-memory.afterChat
	if len(got.Messages) != 2 {
		t.Fatalf("memory messages = %v, want persisted user and assistant", got.Messages)
	}
	if got.Messages[0].Content != "persisted user question" || got.Messages[0].SourceMessageID != "session-1/msg-user" {
		t.Fatalf("persisted user message = %#v", got.Messages[0])
	}
	if got.Messages[1].Content != "stored answer" || got.Messages[1].SourceMessageID != "session-1/msg-assistant" {
		t.Fatalf("persisted assistant message = %#v", got.Messages[1])
	}
}

func TestStoreRoundSkipsMemoryWhenPersistenceIsPartial(t *testing.T) {
	t.Parallel()

	messages := &partialRefsMessageService{}
	memory := &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 1)}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, memory)
	resolver := &Service{
		messageService:  messages,
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}

	if err := resolver.storeRound(context.Background(), ChatRequest{
		BotID: storeRoundBotID, ThreadID: "session-1", Query: "hello",
	}, []ModelMessage{
		{Role: "user", Content: newTextContent("hello")},
		{Role: "assistant", Content: newTextContent("hi there")},
	}, "model-1"); err != nil {
		t.Fatalf("storeRound error: %v", err)
	}

	select {
	case got := <-memory.afterChat:
		t.Fatalf("memory extraction ran for partial persistence: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func storedMemoryTestMessage(t *testing.T, id, sessionID, role, text string) messagepkg.Message {
	t.Helper()
	content, err := json.Marshal(ModelMessage{Role: role, Content: newTextContent(text)})
	if err != nil {
		t.Fatalf("marshal stored message: %v", err)
	}
	return messagepkg.Message{ID: id, SessionID: sessionID, Role: role, Content: content}
}
