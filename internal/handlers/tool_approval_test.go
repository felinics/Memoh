package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/auth"
)

type recordingToolApprovalResponder struct {
	input turn.ToolApprovalResponse
}

func (r *recordingToolApprovalResponder) RespondToolApproval(_ context.Context, input turn.ToolApprovalResponse, _ chan<- json.RawMessage) error {
	r.input = input
	return nil
}

func TestToolApprovalHTTPUsesJWTUserIDForPermissionActor(t *testing.T) {
	t.Parallel()

	const (
		secret            = "test-secret"
		accountID         = "11111111-1111-1111-1111-111111111111"
		channelIdentityID = "22222222-2222-2222-2222-222222222222"
	)
	token, _, err := auth.GenerateChatToken(auth.ChatToken{
		BotID:             "33333333-3333-3333-3333-333333333333",
		ChatID:            "chat-1",
		UserID:            accountID,
		ChannelIdentityID: channelIdentityID,
	}, secret, time.Minute)
	if err != nil {
		t.Fatalf("GenerateChatToken() error = %v", err)
	}

	responder := &recordingToolApprovalResponder{}
	handler := &ToolApprovalHandler{turnService: responder}
	e := echo.New()
	e.Use(auth.JWTMiddleware(secret, func(echo.Context) bool { return false }))
	handler.Register(e)
	req := httptest.NewRequest(http.MethodPost, "/bots/bot-1/tool-approvals/approval-1/approve", strings.NewReader(`{"control_id":"control-1","option_id":"allow-once"}`))
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("response status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if responder.input.ActorUserID != accountID {
		t.Fatalf("ActorUserID = %q, want JWT user/account id %q", responder.input.ActorUserID, accountID)
	}
	if responder.input.ActorUserID == channelIdentityID {
		t.Fatal("HTTP approval used channel identity id as the permission actor")
	}
	if responder.input.ControlID != "control-1" {
		t.Fatalf("ControlID = %q, want control-1", responder.input.ControlID)
	}
	if responder.input.OptionID != "allow-once" {
		t.Fatalf("OptionID = %q, want allow-once", responder.input.OptionID)
	}
}
