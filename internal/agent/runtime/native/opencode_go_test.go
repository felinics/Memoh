package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	opencodego "github.com/felinics/twilight/provider/opencode/go"
	"github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
)

func TestOpenCodeGoUsesRunSessionForGenerateAndStream(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get(opencodego.SessionHeader) != "child-thread" {
			t.Error("runtime did not override inherited parent session")
		}
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"a\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"a","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`)
		}
	}))
	defer srv.Close()
	model := models.NewSDKChatModel(models.SDKModelConfig{ClientType: string(models.ClientTypeOpenCodeGo), ModelID: "glm-5.2", BaseURL: srv.URL})
	cfg := RunConfig{Model: model, Messages: []sdk.Message{sdk.UserMessage("hi")}, Identity: SessionContext{BotID: "bot-1", SessionID: "child-thread"}}
	ctx := models.WithModelSession(context.Background(), "parent-thread")
	a := New(Deps{})
	if _, err := a.Generate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for event := range a.Stream(ctx, cfg) {
		if event.Type == "error" {
			t.Fatalf("stream failed: %+v", event)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want two successful turns", requests.Load())
	}
}
