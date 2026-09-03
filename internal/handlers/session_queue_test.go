package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
)

func TestSessionQueueHandlerRegistersSeparateQueueRoutes(t *testing.T) {
	e := echo.New()
	(&SessionQueueHandler{}).Register(e)
	want := map[string]bool{
		http.MethodPost + " /bots/:bot_id/sessions/:session_id/steer-queue":                    true,
		http.MethodGet + " /bots/:bot_id/sessions/:session_id/steer-queue":                     true,
		http.MethodGet + " /bots/:bot_id/sessions/:session_id/queue":                           true,
		http.MethodPut + " /bots/:bot_id/sessions/:session_id/steer-queue/reorder":             true,
		http.MethodPatch + " /bots/:bot_id/sessions/:session_id/steer-queue/:item_id":          true,
		http.MethodDelete + " /bots/:bot_id/sessions/:session_id/steer-queue/:item_id":         true,
		http.MethodPost + " /bots/:bot_id/sessions/:session_id/follow-up-queue":                true,
		http.MethodGet + " /bots/:bot_id/sessions/:session_id/follow-up-queue":                 true,
		http.MethodPut + " /bots/:bot_id/sessions/:session_id/follow-up-queue/reorder":         true,
		http.MethodPatch + " /bots/:bot_id/sessions/:session_id/follow-up-queue/:item_id":      true,
		http.MethodDelete + " /bots/:bot_id/sessions/:session_id/follow-up-queue/:item_id":     true,
		http.MethodPost + " /bots/:bot_id/sessions/:session_id/follow-up-queue/:item_id/steer": true,
	}
	for _, route := range e.Routes() {
		delete(want, route.Method+" "+route.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing queue routes: %v", want)
	}
}

func TestSessionQueueReorderRequestsDecodeTypedReferences(t *testing.T) {
	itemID := "4ed490e0-649a-41d5-8456-6fe2ebf1e031"
	beforeID := "01a9b524-fbe0-4cb0-b42a-a4fe2c284e26"
	body := []byte(`{"item":{"item_id":"` + itemID + `"},"before":{"item_id":"` + beforeID + `"}}`)

	e := echo.New()
	steerContext := e.NewContext(httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)), httptest.NewRecorder())
	steerContext.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	steer, err := decodeSteerReorderRequest(steerContext)
	if err != nil || steer.Item.ItemID != queue.SteerItemID(itemID) || steer.Before.ItemID != queue.SteerItemID(beforeID) {
		t.Fatalf("steer reorder request = %#v, %v", steer, err)
	}

	followContext := e.NewContext(httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)), httptest.NewRecorder())
	followContext.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	follow, err := decodeFollowUpReorderRequest(followContext)
	if err != nil || follow.Item.ItemID != queue.FollowUpItemID(itemID) || follow.Before.ItemID != queue.FollowUpItemID(beforeID) {
		t.Fatalf("follow-up reorder request = %#v, %v", follow, err)
	}
}

func TestSessionQueueResponsesDoNotUseMixedQueueKind(t *testing.T) {
	steerJSON, err := json.Marshal(steerQueueItemResponseFrom(queue.SteerItem{ID: "steer", Status: queue.Accepted, Position: 1, Payload: []byte(`{"text":"s"}`), TargetRunID: "run-0"}))
	if err != nil {
		t.Fatal(err)
	}
	followJSON, err := json.Marshal(followUpQueueItemResponseFrom(queue.FollowUpItem{ID: "follow", Status: queue.Accepted, Position: 2, Payload: []byte(`{"text":"f"}`), EnqueuedDuringRunID: "run-0"}))
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{"steer": steerJSON, "follow_up": followJSON} {
		var response map[string]any
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatal(err)
		}
		if _, ok := response["queue"]; ok {
			t.Fatalf("%s response contains mixed queue discriminator: %s", name, payload)
		}
		if _, ok := response["kind"]; ok {
			t.Fatalf("%s response contains mixed kind discriminator: %s", name, payload)
		}
	}
}
