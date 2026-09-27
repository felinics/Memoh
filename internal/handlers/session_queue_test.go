package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/application"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
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
	if err != nil || steer.Item.ItemID != sessionruntime.SteerItemID(itemID) || steer.Before.ItemID != sessionruntime.SteerItemID(beforeID) {
		t.Fatalf("steer reorder request = %#v, %v", steer, err)
	}

	followContext := e.NewContext(httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)), httptest.NewRecorder())
	followContext.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	follow, err := decodeFollowUpReorderRequest(followContext)
	if err != nil || follow.Item.ItemID != sessionruntime.FollowUpItemID(itemID) || follow.Before.ItemID != sessionruntime.FollowUpItemID(beforeID) {
		t.Fatalf("follow-up reorder request = %#v, %v", follow, err)
	}
}

func TestSessionQueueResponsesDoNotUseMixedQueueKind(t *testing.T) {
	steerJSON, err := json.Marshal(steerQueueItemResponseFrom(sessionruntime.SteerItem{ID: "steer", Status: sessionruntime.QueueAccepted, Position: 1, Payload: []byte(`{"text":"s"}`), TargetRunID: "run-0"}))
	if err != nil {
		t.Fatal(err)
	}
	followJSON, err := json.Marshal(followUpQueueItemResponseFrom(sessionruntime.FollowUpItem{ID: "follow", Status: sessionruntime.QueueAccepted, Position: 2, Payload: []byte(`{"text":"f"}`), EnqueuedDuringRunID: "run-0"}))
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

func TestSessionQueueEditBySomeoneElseIsForbidden(t *testing.T) {
	err := queueMutationError(fmt.Errorf("update: %w", application.ErrQueueItemNotEditable))
	problem, ok := apperror.ProblemFrom(err, "")
	if !ok || problem.Status != http.StatusForbidden || problem.Code != string(apperror.CodeQueueItemNotEditable) {
		t.Fatalf("queueMutationError() = %v, want %d %s", err, http.StatusForbidden, apperror.CodeQueueItemNotEditable)
	}
}

type queueSessionQueries struct {
	dbstore.Queries
	session sqlc.BotSession
	err     error
}

func (q queueSessionQueries) GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
	return q.session, q.err
}

func TestSessionQueueAuthorizeSeparatesAMissingSessionFromAFailedLookup(t *testing.T) {
	const (
		botID     = "00000000-0000-0000-0000-000000000701"
		sessionID = "00000000-0000-0000-0000-000000000702"
	)
	lookupFailure := errors.New("synthetic connection reset")
	for _, tc := range []struct {
		name    string
		queries queueSessionQueries
		code    apperror.Code
		cause   error
	}{
		{name: "no such session", queries: queueSessionQueries{err: pgx.ErrNoRows}, code: apperror.CodeSessionNotFound},
		{name: "session of another bot", queries: queueSessionQueries{session: sqlc.BotSession{BotID: testUUID("00000000-0000-0000-0000-000000000799")}}, code: apperror.CodeSessionNotFound},
		{name: "failed lookup", queries: queueSessionQueries{err: lookupFailure}, cause: lookupFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &SessionQueueHandler{queries: tc.queries, agentService: &application.Service{}}
			e := echo.New()
			c := testAuthContext(e, httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder(), "user-1")
			c.SetParamNames("bot_id", "session_id")
			c.SetParamValues(botID, sessionID)

			_, err := h.authorize(c)
			if tc.cause != nil {
				if apperror.CodeOf(err) != "" || !errors.Is(err, tc.cause) {
					t.Fatalf("authorize() = %v, want the lookup failure without a public code", err)
				}
				return
			}
			if got := apperror.CodeOf(err); got != tc.code {
				t.Fatalf("authorize() code = %q (%v), want %q", got, err, tc.code)
			}
		})
	}
}

func TestQueueAdmissionErrorKeepsPublishedCodes(t *testing.T) {
	cases := []struct {
		err  error
		want apperror.Code
	}{
		{sessionruntime.ErrQueueSteerUnsupported, apperror.CodeQueueSteerUnsupported},
		{sessionruntime.ErrQueueNoActiveRun, apperror.CodeQueueNoActiveRun},
		{sessionruntime.ErrQueueInvocationConflict, apperror.CodeSessionInvocationConflict},
		{sessionruntime.ErrQueueAdmissionOverloaded, apperror.CodeQueueAdmissionOverloaded},
		{sessionruntime.ErrQueueCapacityExceeded, apperror.CodeQueueCapacityExceeded},
		{sessionruntime.ErrQueueInvalidReference, apperror.CodeQueueRequestInvalid},
		{application.ErrQueueInputIncomplete, apperror.CodeQueueAdmissionUnavailable},
	}
	for _, tc := range cases {
		if got := apperror.CodeOf(queueAdmissionError(tc.err)); got != tc.want {
			t.Errorf("queueAdmissionError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	other := fmt.Errorf("store: %w", sessionruntime.ErrLiveQueueUnavailable)
	if got := queueAdmissionError(other); !errors.Is(got, other) {
		t.Fatalf("unclassified error was rewritten: %v", got)
	}
	if queueAdmissionError(nil) != nil {
		t.Fatal("nil error was rewritten")
	}
}
