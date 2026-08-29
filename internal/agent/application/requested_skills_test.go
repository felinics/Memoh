package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/slash"
)

func requestedSkillGuardRequest(sessionID string) ChatRequest {
	return ChatRequest{
		BotID:    "bot-1",
		ThreadID: sessionID,
		RequestedSkills: []RequestedSkillContext{{
			Name:    "alpha",
			Content: "skill content",
		}},
	}
}

func TestRejectRequestedSkillsAllowsModelChatSession(t *testing.T) {
	resolver := &Service{
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:          sessionID,
					BotID:       "bot-1",
					Type:        session.TypeChat,
					SessionMode: session.TypeChat,
					RuntimeType: session.RuntimeModel,
				}, nil
			},
		},
	}

	req := requestedSkillGuardRequest("session-1")
	req.InjectCh = make(chan InjectMessage)
	if err := resolver.rejectRequestedSkillsIfUnsupportedContext(context.Background(), req); err != nil {
		t.Fatalf("rejectRequestedSkillsIfUnsupportedContext() error = %v, want nil", err)
	}
}

func TestRejectRequestedSkillsAllowsPersistedSkillActivation(t *testing.T) {
	resolver := &Service{
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:          sessionID,
					BotID:       "bot-1",
					Type:        session.TypeChat,
					SessionMode: session.TypeChat,
					RuntimeType: session.RuntimeModel,
				}, nil
			},
		},
	}

	req := requestedSkillGuardRequest("session-1")
	req.UserMessagePersisted = true
	req.UserMessageKind = UserMessageKindSkillActivation
	if err := resolver.rejectRequestedSkillsIfUnsupportedContext(context.Background(), req); err != nil {
		t.Fatalf("rejectRequestedSkillsIfUnsupportedContext() error = %v, want nil", err)
	}
}

func TestRejectReservedSkillMetadataIfPresentRejectsAttachments(t *testing.T) {
	req := ChatRequest{
		Attachments: []ChatAttachment{{
			Type:     "image",
			Metadata: map[string]any{"model_requested_skills": []string{"alpha"}},
		}},
	}
	err := rejectReservedSkillMetadataIfPresent(req)
	var slashErr slash.Error
	if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeReservedSkillMetadata {
		t.Fatalf("err = %#v, want %s", err, slash.CodeReservedSkillMetadata)
	}
}

func TestRejectReservedSkillMetadataIfPresentRejectsMessageContentPartMetadata(t *testing.T) {
	content, err := json.Marshal([]ContentPart{{
		Type:     "text",
		Text:     "hello",
		Metadata: map[string]any{"requested_skills": []string{"alpha"}},
	}})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	req := ChatRequest{
		Messages: []ModelMessage{{
			Role:    "user",
			Content: content,
		}},
	}

	err = rejectReservedSkillMetadataIfPresent(req)
	var slashErr slash.Error
	if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeReservedSkillMetadata {
		t.Fatalf("err = %#v, want %s", err, slash.CodeReservedSkillMetadata)
	}
}

func TestRejectRequestedSkillsRejectsUnsupportedContexts(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		mutate    func(*ChatRequest)
		session   session.Thread
	}{
		{
			name:      "missing session",
			sessionID: "",
		},
		{
			name:      "discuss session",
			sessionID: "session-1",
			session: session.Thread{
				ID:          "session-1",
				BotID:       "bot-1",
				Type:        session.TypeDiscuss,
				SessionMode: session.TypeDiscuss,
				RuntimeType: session.RuntimeModel,
			},
		},
		{
			name:      "schedule run mode",
			sessionID: "session-1",
			mutate: func(req *ChatRequest) {
				req.SessionType = session.TypeSchedule
			},
			session: session.Thread{
				ID:          "session-1",
				BotID:       "bot-1",
				Type:        session.TypeChat,
				SessionMode: session.TypeChat,
				RuntimeType: session.RuntimeModel,
			},
		},
		{
			name:      "already persisted user message",
			sessionID: "session-1",
			mutate: func(req *ChatRequest) {
				req.UserMessagePersisted = true
			},
			session: session.Thread{
				ID:          "session-1",
				BotID:       "bot-1",
				Type:        session.TypeChat,
				SessionMode: session.TypeChat,
				RuntimeType: session.RuntimeModel,
			},
		},
		{
			name:      "ACP runtime",
			sessionID: "session-1",
			session: session.Thread{
				ID:          "session-1",
				BotID:       "bot-1",
				Type:        session.TypeChat,
				SessionMode: session.TypeChat,
				RuntimeType: session.RuntimeACPAgent,
			},
		},
		{
			name:      "legacy ACP session",
			sessionID: "session-1",
			session: session.Thread{
				ID:    "session-1",
				BotID: "bot-1",
				Type:  session.TypeACPAgent,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &Service{
				sessionService: &fakeBackgroundSessionService{
					getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
						if sessionID != tc.sessionID {
							t.Fatalf("session id = %q, want %q", sessionID, tc.sessionID)
						}
						return tc.session, nil
					},
				},
			}
			if tc.sessionID == "" {
				resolver.sessionService = nil
			}

			req := requestedSkillGuardRequest(tc.sessionID)
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			err := resolver.rejectRequestedSkillsIfUnsupportedContext(context.Background(), req)
			var slashErr slash.Error
			if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeUnsupportedSkillSlashContext {
				t.Fatalf("error = %v, want %s", err, slash.CodeUnsupportedSkillSlashContext)
			}
		})
	}
}

func TestResolveRejectsRequestedSkillsBeforeModelResolution(t *testing.T) {
	resolver := &Service{}
	_, _, err := resolver.resolve(context.Background(), ChatRequest{
		BotID:       "bot-1",
		ChatID:      "chat-1",
		ThreadID:    "session-1",
		SessionType: session.TypeSchedule,
		Query:       "scheduled prompt",
		RequestedSkills: []RequestedSkillContext{{
			Name:    "alpha",
			Content: "skill content",
		}},
	})
	var slashErr slash.Error
	if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeUnsupportedSkillSlashContext {
		t.Fatalf("resolve() error = %v, want %s", err, slash.CodeUnsupportedSkillSlashContext)
	}
}

func TestStreamChatWSRejectsACPRequestedSkillsBeforePool(t *testing.T) {
	pool := &recordingACPPrompter{}
	resolver := &Service{
		acpPool:        pool,
		sessionService: acpRuntimeSessionServiceForTest("user-1"),
	}

	err := resolver.StreamChatWS(
		context.Background(),
		requestedSkillGuardRequest("session-1"),
		make(chan WSStreamEvent, 1),
		make(chan struct{}),
	)
	var slashErr slash.Error
	if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeUnsupportedSkillSlashContext {
		t.Fatalf("StreamChatWS() error = %v, want %s", err, slash.CodeUnsupportedSkillSlashContext)
	}
	if pool.calls != 0 {
		t.Fatalf("ACP pool calls = %d, want 0", pool.calls)
	}
}
