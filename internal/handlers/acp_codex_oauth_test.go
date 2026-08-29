package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	testCodexAccessJWT      = "access.jwt.fixture"
	testCodexIDJWT          = "id.jwt.fixture"
	testCodexRefreshFixture = "refresh-fixture"
)

func TestGenerateACPCodexOAuthStateUsesCallbackPrefix(t *testing.T) {
	state, err := generateACPCodexOAuthState()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(state, acpCodexOAuthStatePrefix) {
		t.Fatalf("state = %q, want prefix %q", state, acpCodexOAuthStatePrefix)
	}
	handler := &ACPCodexOAuthHandler{}
	if !handler.HandlesCallbackState(state) {
		t.Fatalf("handler did not recognize ACP Codex OAuth state")
	}
	if handler.HandlesCallbackState("provider-state") {
		t.Fatalf("handler should not recognize normal provider OAuth state")
	}
}

func TestParseCodexOAuthAuthRequiresDistinctIDToken(t *testing.T) {
	valid := acpclient.ParseCodexOAuthAuth(`{
  "auth_mode": "chatgpt",
  "tokens": {
    "id_token": "id.jwt.fixture",
    "access_token": "access.jwt.fixture",
    "refresh_token": "refresh-fixture",
    "account_id": "account-123"
  }
}`)
	if !valid.Valid {
		t.Fatalf("valid auth.json was not accepted")
	}
	if valid.AccountID != "account-123" {
		t.Fatalf("account id = %q, want account-123", valid.AccountID)
	}

	invalidSameToken := acpclient.ParseCodexOAuthAuth(`{
  "auth_mode": "chatgpt",
  "tokens": {
    "id_token": "same.jwt.fixture",
    "access_token": "same.jwt.fixture",
    "refresh_token": "refresh-fixture",
    "account_id": "account-123"
  }
}`)
	if invalidSameToken.Valid {
		t.Fatalf("auth.json with id_token equal to access_token should not be accepted")
	}

	invalidWithoutAccountID := acpclient.ParseCodexOAuthAuth(`{
  "auth_mode": "chatgpt",
  "tokens": {
    "id_token": "id.jwt.fixture",
    "access_token": "access.jwt.fixture",
    "refresh_token": "refresh-fixture"
  }
}`)
	if invalidWithoutAccountID.Valid {
		t.Fatalf("auth.json without account_id should not be accepted")
	}
}

func TestGenerateACPCodexDeviceAuthSessionIDDoesNotUseCallbackPrefix(t *testing.T) {
	sessionID, err := generateACPCodexDeviceAuthSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(sessionID, acpCodexOAuthStatePrefix) {
		t.Fatalf("device session id %q must not use callback state prefix %q", sessionID, acpCodexOAuthStatePrefix)
	}
	handler := &ACPCodexOAuthHandler{}
	if handler.HandlesCallbackState(sessionID) {
		t.Fatalf("handler should not recognize device session id as callback state")
	}
}

func TestACPCodexDeviceSessionKeepsSuccessForLatePolls(t *testing.T) {
	now := time.Now().UTC()
	h := &ACPCodexOAuthHandler{deviceSessions: map[string]*acpCodexDeviceAuthSession{}}
	session := testCodexDeviceSession("session-1", now)
	h.deviceSessions[session.SessionID] = session

	polling, generation, shouldPoll, err := h.prepareDevicePoll(session.SessionID, session.BotID, session.ChannelIdentityID, now)
	if err != nil {
		t.Fatalf("prepare poll: %v", err)
	}
	if !shouldPoll {
		t.Fatalf("expected session to be ready to poll")
	}
	if polling.Generation != generation {
		t.Fatalf("generation mismatch: %d != %d", polling.Generation, generation)
	}
	_, cancelWrite, err := h.beginDeviceAuthWrite(context.Background(), session.SessionID, generation, now)
	if err != nil {
		t.Fatalf("begin auth write: %v", err)
	}
	defer cancelWrite()
	updated := h.finishDeviceAuthWrite(session.SessionID, generation, "account-123", nil, now)
	if updated.Status != acpCodexDeviceAuthStatusSuccess {
		t.Fatalf("status = %q, want success", updated.Status)
	}

	late, _, shouldPoll, err := h.prepareDevicePoll(session.SessionID, session.BotID, session.ChannelIdentityID, now.Add(time.Second))
	if err != nil {
		t.Fatalf("prepare late poll: %v", err)
	}
	if shouldPoll {
		t.Fatalf("late duplicate poll should not call OpenAI")
	}
	if late.Status != acpCodexDeviceAuthStatusSuccess {
		t.Fatalf("late status = %q, want success", late.Status)
	}
}

func TestACPCodexDeviceCancelPreventsInflightWrite(t *testing.T) {
	now := time.Now().UTC()
	h := &ACPCodexOAuthHandler{deviceSessions: map[string]*acpCodexDeviceAuthSession{}}
	session := testCodexDeviceSession("session-1", now)
	h.deviceSessions[session.SessionID] = session

	_, generation, shouldPoll, err := h.prepareDevicePoll(session.SessionID, session.BotID, session.ChannelIdentityID, now)
	if err != nil {
		t.Fatalf("prepare poll: %v", err)
	}
	if !shouldPoll {
		t.Fatalf("expected session to be ready to poll")
	}

	h.mu.Lock()
	session.Status = acpCodexDeviceAuthStatusCancelled
	session.Polling = false
	session.Generation++
	session.TerminalExpiresAt = now.Add(acpCodexDeviceAuthTerminalTTL)
	h.mu.Unlock()

	if _, _, err := h.beginDeviceAuthWrite(context.Background(), session.SessionID, generation, now); err == nil {
		t.Fatalf("begin auth write should fail after cancellation")
	}
	updated := h.finishDeviceAuthWrite(session.SessionID, generation, "account-123", nil, now)
	if updated.Status != acpCodexDeviceAuthStatusCancelled {
		t.Fatalf("status = %q, want cancelled", updated.Status)
	}
	if updated.AccountID != "" {
		t.Fatalf("account id should not be written after cancellation")
	}
}

func TestACPCodexDevicePruneExpiresPendingThenTerminal(t *testing.T) {
	now := time.Now().UTC()
	h := &ACPCodexOAuthHandler{deviceSessions: map[string]*acpCodexDeviceAuthSession{}}
	session := testCodexDeviceSession("session-1", now)
	session.ExpiresAt = now.Add(-time.Second)
	h.deviceSessions[session.SessionID] = session

	h.mu.Lock()
	h.pruneExpiredLocked(now)
	if session.Status != acpCodexDeviceAuthStatusExpired {
		t.Fatalf("status = %q, want expired", session.Status)
	}
	if _, ok := h.deviceSessions[session.SessionID]; !ok {
		t.Fatalf("terminal session should remain until terminal grace TTL")
	}
	session.TerminalExpiresAt = now.Add(-time.Second)
	h.pruneExpiredLocked(now)
	if _, ok := h.deviceSessions[session.SessionID]; ok {
		t.Fatalf("terminal session should be pruned after grace TTL")
	}
	h.mu.Unlock()
}

func TestACPCodexDevicePruneExpiresWritingSession(t *testing.T) {
	now := time.Now().UTC()
	h := &ACPCodexOAuthHandler{deviceSessions: map[string]*acpCodexDeviceAuthSession{}}
	session := testCodexDeviceSession("session-1", now)
	session.Status = acpCodexDeviceAuthStatusWriting
	session.Polling = true
	session.ExpiresAt = now.Add(-time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	session.WriteCancel = cancel
	h.deviceSessions[session.SessionID] = session

	h.mu.Lock()
	h.pruneExpiredLocked(now)
	h.mu.Unlock()

	if session.Status != acpCodexDeviceAuthStatusExpired {
		t.Fatalf("status = %q, want expired", session.Status)
	}
	if session.TerminalExpiresAt.IsZero() {
		t.Fatalf("terminal expiry should be set for expired writing session")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatalf("writing context was not cancelled")
	}
}

func TestACPCodexDevicePollErrorMarksTerminal(t *testing.T) {
	now := time.Now().UTC()
	h := &ACPCodexOAuthHandler{deviceSessions: map[string]*acpCodexDeviceAuthSession{}}
	session := testCodexDeviceSession("session-1", now)
	h.deviceSessions[session.SessionID] = session

	_, generation, shouldPoll, err := h.prepareDevicePoll(session.SessionID, session.BotID, session.ChannelIdentityID, now)
	if err != nil {
		t.Fatalf("prepare poll: %v", err)
	}
	if !shouldPoll {
		t.Fatalf("expected session to be ready to poll")
	}
	updated := h.finishDevicePollError(session.SessionID, generation, errors.New("boom"), now)
	if updated.Status != acpCodexDeviceAuthStatusError {
		t.Fatalf("status = %q, want error", updated.Status)
	}
	if updated.LastError != acpCodexDeviceAuthGenericError {
		t.Fatalf("last error = %q", updated.LastError)
	}
}

func TestACPCodexDeviceTransientPollErrorStaysPending(t *testing.T) {
	now := time.Now().UTC()
	h := &ACPCodexOAuthHandler{deviceSessions: map[string]*acpCodexDeviceAuthSession{}}
	session := testCodexDeviceSession("session-1", now)
	h.deviceSessions[session.SessionID] = session

	_, generation, shouldPoll, err := h.prepareDevicePoll(session.SessionID, session.BotID, session.ChannelIdentityID, now)
	if err != nil {
		t.Fatalf("prepare poll: %v", err)
	}
	if !shouldPoll {
		t.Fatalf("expected session to be ready to poll")
	}
	if !isTransientCodexDevicePollError(context.DeadlineExceeded) {
		t.Fatalf("deadline exceeded should be treated as transient")
	}
	updated := h.finishDevicePollPending(session.SessionID, generation, now)
	if updated.Status != acpCodexDeviceAuthStatusPending {
		t.Fatalf("status = %q, want pending", updated.Status)
	}
	if updated.Polling {
		t.Fatalf("polling should be cleared after transient error")
	}
}

func TestACPCodexDeviceHTTPFlowWritesManagedConfig(t *testing.T) { //nolint:gosec // test fixture validates token-shaped Codex auth JSON.
	env := newACPCodexDeviceHTTPTestEnv(t, &acpCodexDeviceIntegrationProvider{
		device: providers.OpenAICodexACPDeviceAuthorization{
			DeviceAuthID:    "device-auth-1",
			UserCode:        "CODE-123",
			VerificationURL: "https://auth.openai.com/codex/device",
			IntervalSeconds: 1,
		},
		pollResults: []providers.OpenAICodexACPDevicePollResult{
			{Pending: true},
			{
				AuthorizationCode: "authorization-code",
				CodeVerifier:      "code-verifier",
			},
		},
		creds: providers.OpenAICodexOAuthCredentials{
			AccessToken:  testCodexAccessJWT,
			IDToken:      testCodexIDJWT,
			RefreshToken: testCodexRefreshFixture,
			AccountID:    "account-123",
			BaseURL:      "https://chatgpt.com/backend-api",
			LastRefresh:  time.Date(2026, 5, 28, 1, 2, 3, 0, time.UTC),
		},
	})

	authorizeRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/authorize", nil)
	if authorizeRec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d, body = %s", authorizeRec.Code, authorizeRec.Body.String())
	}
	var authorizeResp ACPCodexOAuthDeviceAuthorizeResponse
	if err := json.Unmarshal(authorizeRec.Body.Bytes(), &authorizeResp); err != nil {
		t.Fatalf("decode authorize response: %v", err)
	}
	if authorizeResp.SessionID == "" {
		t.Fatalf("session id is empty")
	}
	if authorizeResp.UserCode != "CODE-123" {
		t.Fatalf("user code = %q, want CODE-123", authorizeResp.UserCode)
	}
	if authorizeResp.VerificationURL != "https://auth.openai.com/codex/device" {
		t.Fatalf("verification URL = %q", authorizeResp.VerificationURL)
	}

	pendingRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/poll", ACPCodexOAuthDeviceSessionRequest{
		SessionID: authorizeResp.SessionID,
	})
	if pendingRec.Code != http.StatusOK {
		t.Fatalf("pending poll status = %d, body = %s", pendingRec.Code, pendingRec.Body.String())
	}
	pending := decodeACPCodexDeviceStatus(t, pendingRec)
	if pending.Status != string(acpCodexDeviceAuthStatusPending) || pending.HasToken {
		t.Fatalf("pending response = %+v", pending)
	}
	if writes := env.recorder.writes(); len(writes) != 0 {
		t.Fatalf("pending poll should not write Codex auth files: %#v", writes)
	}
	if env.provider.pollCount() != 1 {
		t.Fatalf("provider polls after first pending response = %d, want 1", env.provider.pollCount())
	}

	throttledRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/poll", ACPCodexOAuthDeviceSessionRequest{
		SessionID: authorizeResp.SessionID,
	})
	if throttledRec.Code != http.StatusOK {
		t.Fatalf("throttled poll status = %d, body = %s", throttledRec.Code, throttledRec.Body.String())
	}
	throttled := decodeACPCodexDeviceStatus(t, throttledRec)
	if throttled.Status != string(acpCodexDeviceAuthStatusPending) || throttled.HasToken {
		t.Fatalf("throttled response = %+v", throttled)
	}
	if env.provider.pollCount() != 1 {
		t.Fatalf("early poll should not call provider again, got %d polls", env.provider.pollCount())
	}

	env.handler.mu.Lock()
	env.handler.deviceSessions[authorizeResp.SessionID].NextPollAfter = time.Now().Add(-time.Second)
	env.handler.mu.Unlock()

	successRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/poll", ACPCodexOAuthDeviceSessionRequest{
		SessionID: authorizeResp.SessionID,
	})
	if successRec.Code != http.StatusOK {
		t.Fatalf("success poll status = %d, body = %s", successRec.Code, successRec.Body.String())
	}
	success := decodeACPCodexDeviceStatus(t, successRec)
	if success.Status != string(acpCodexDeviceAuthStatusSuccess) || !success.HasToken {
		t.Fatalf("success response = %+v", success)
	}
	if success.AccountID != "account-123" {
		t.Fatalf("account id = %q, want account-123", success.AccountID)
	}

	writes := env.recorder.writes()
	if len(writes) != 2 {
		t.Fatalf("writes len = %d, want config.toml + auth.json: %#v", len(writes), writes)
	}
	configWrite, ok := findUsersACPConfigWrite(writes, acpclient.CodexManagedConfigDir+"/config.toml")
	if !ok {
		t.Fatalf("missing Codex config.toml write: %#v", writes)
	}
	config := string(configWrite.Content)
	for _, want := range []string{
		`model_provider = "chatgpt-http"`,
		`base_url = "https://chatgpt.com/backend-api/codex"`,
		`requires_openai_auth = true`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex config missing %q:\n%s", want, config)
		}
	}
	authWrite, ok := findUsersACPConfigWrite(writes, acpclient.CodexManagedConfigDir+"/auth.json")
	if !ok {
		t.Fatalf("missing Codex auth.json write: %#v", writes)
	}
	var auth map[string]any
	if err := json.Unmarshal(authWrite.Content, &auth); err != nil {
		t.Fatalf("invalid auth json: %v\n%s", err, string(authWrite.Content))
	}
	if auth["auth_mode"] != "chatgpt" {
		t.Fatalf("auth_mode = %#v, want chatgpt", auth["auth_mode"])
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens missing from auth json: %#v", auth)
	}
	for key, want := range map[string]string{
		"id_token":      testCodexIDJWT,
		"access_token":  testCodexAccessJWT,
		"refresh_token": testCodexRefreshFixture,
		"account_id":    "account-123",
	} {
		if got := tokens[key]; got != want {
			t.Fatalf("tokens[%s] = %#v, want %q", key, got, want)
		}
	}
	if env.provider.pollCount() != 2 {
		t.Fatalf("provider polls = %d, want 2", env.provider.pollCount())
	}
	if got := env.provider.exchangeCode(); got != "authorization-code" {
		t.Fatalf("exchange code = %q, want authorization-code", got)
	}
	if got := env.provider.exchangeCodeVerifier(); got != "code-verifier" {
		t.Fatalf("exchange code verifier = %q, want code-verifier", got)
	}
}

func TestACPCodexDeviceHTTPWriteFailureMarksTerminal(t *testing.T) { //nolint:gosec // test fixture uses token-shaped Codex credentials.
	env := newACPCodexDeviceHTTPTestEnv(t, &acpCodexDeviceIntegrationProvider{
		device: providers.OpenAICodexACPDeviceAuthorization{
			DeviceAuthID:    "device-auth-1",
			UserCode:        "CODE-123",
			VerificationURL: "https://auth.openai.com/codex/device",
			IntervalSeconds: 1,
		},
		pollResults: []providers.OpenAICodexACPDevicePollResult{
			{
				AuthorizationCode: "authorization-code",
				CodeVerifier:      "code-verifier",
			},
		},
		creds: providers.OpenAICodexOAuthCredentials{
			AccessToken:  testCodexAccessJWT,
			IDToken:      testCodexIDJWT,
			RefreshToken: testCodexRefreshFixture,
			AccountID:    "account-123",
		},
	})
	env.recorder.writeErr = errors.New("write failed")

	authorizeRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/authorize", nil)
	if authorizeRec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d, body = %s", authorizeRec.Code, authorizeRec.Body.String())
	}
	var authorizeResp ACPCodexOAuthDeviceAuthorizeResponse
	if err := json.Unmarshal(authorizeRec.Body.Bytes(), &authorizeResp); err != nil {
		t.Fatalf("decode authorize response: %v", err)
	}

	pollRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/poll", ACPCodexOAuthDeviceSessionRequest{
		SessionID: authorizeResp.SessionID,
	})
	if pollRec.Code != http.StatusOK {
		t.Fatalf("poll status = %d, body = %s", pollRec.Code, pollRec.Body.String())
	}
	status := decodeACPCodexDeviceStatus(t, pollRec)
	if status.Status != string(acpCodexDeviceAuthStatusError) || status.HasToken {
		t.Fatalf("write failure response = %+v", status)
	}
	if status.Error != acpCodexDeviceAuthGenericError {
		t.Fatalf("error = %q, want generic device auth error", status.Error)
	}
	if writes := env.recorder.writes(); len(writes) != 0 {
		t.Fatalf("failed write should not be recorded: %#v", writes)
	}
}

func TestACPCodexDeviceHTTPRequiresSessionID(t *testing.T) {
	env := newACPCodexDeviceHTTPTestEnv(t, &acpCodexDeviceIntegrationProvider{})

	for _, endpoint := range []string{
		"/bots/" + env.botID + "/acp/codex/oauth/device/poll",
		"/bots/" + env.botID + "/acp/codex/oauth/device/cancel",
	} {
		rec := env.postJSON(t, http.MethodPost, endpoint, ACPCodexOAuthDeviceSessionRequest{})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, body = %s", endpoint, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "session_id is required") {
			t.Fatalf("%s body = %s, want session_id validation", endpoint, rec.Body.String())
		}
	}
}

func TestACPCodexDeviceRejectsSessionFromDifferentBotOrChannel(t *testing.T) {
	now := time.Now().UTC()
	h := &ACPCodexOAuthHandler{deviceSessions: map[string]*acpCodexDeviceAuthSession{}}
	session := testCodexDeviceSession("session-1", now)
	h.deviceSessions[session.SessionID] = session

	if _, _, _, err := h.prepareDevicePoll(session.SessionID, "other-bot", session.ChannelIdentityID, now); err == nil {
		t.Fatal("expected different bot to be rejected")
	}
	if _, _, _, err := h.prepareDevicePoll(session.SessionID, session.BotID, "other-channel", now); err == nil {
		t.Fatal("expected different channel identity to be rejected")
	}
}

func TestACPCodexDeviceHTTPCancelPreventsInflightWrite(t *testing.T) { //nolint:gosec // test fixture validates token-shaped Codex auth JSON.
	pollStarted := make(chan struct{})
	unblockPoll := make(chan struct{})
	env := newACPCodexDeviceHTTPTestEnv(t, &acpCodexDeviceIntegrationProvider{
		device: providers.OpenAICodexACPDeviceAuthorization{
			DeviceAuthID:    "device-auth-1",
			UserCode:        "CODE-123",
			VerificationURL: "https://auth.openai.com/codex/device",
			IntervalSeconds: 1,
		},
		pollResults: []providers.OpenAICodexACPDevicePollResult{
			{
				AuthorizationCode: "authorization-code",
				CodeVerifier:      "code-verifier",
			},
		},
		creds: providers.OpenAICodexOAuthCredentials{
			AccessToken:  testCodexAccessJWT,
			IDToken:      testCodexIDJWT,
			RefreshToken: testCodexRefreshFixture,
			AccountID:    "account-123",
		},
		pollStarted: pollStarted,
		unblockPoll: unblockPoll,
	})

	authorizeRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/authorize", nil)
	if authorizeRec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d, body = %s", authorizeRec.Code, authorizeRec.Body.String())
	}
	var authorizeResp ACPCodexOAuthDeviceAuthorizeResponse
	if err := json.Unmarshal(authorizeRec.Body.Bytes(), &authorizeResp); err != nil {
		t.Fatalf("decode authorize response: %v", err)
	}

	pollDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		pollDone <- env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/poll", ACPCodexOAuthDeviceSessionRequest{
			SessionID: authorizeResp.SessionID,
		})
	}()

	select {
	case <-pollStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for inflight poll")
	}

	cancelRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/cancel", ACPCodexOAuthDeviceSessionRequest{
		SessionID: authorizeResp.SessionID,
	})
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelRec.Code, cancelRec.Body.String())
	}
	cancelled := decodeACPCodexDeviceStatus(t, cancelRec)
	if cancelled.Status != string(acpCodexDeviceAuthStatusCancelled) || cancelled.HasToken {
		t.Fatalf("cancel response = %+v", cancelled)
	}

	close(unblockPoll)
	var pollRec *httptest.ResponseRecorder
	select {
	case pollRec = <-pollDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poll response")
	}
	if pollRec.Code != http.StatusOK {
		t.Fatalf("poll status = %d, body = %s", pollRec.Code, pollRec.Body.String())
	}
	pollResp := decodeACPCodexDeviceStatus(t, pollRec)
	if pollResp.Status != string(acpCodexDeviceAuthStatusCancelled) || pollResp.HasToken {
		t.Fatalf("poll response after cancel = %+v", pollResp)
	}
	if writes := env.recorder.writes(); len(writes) != 0 {
		t.Fatalf("cancelled inflight poll should not write Codex auth files: %#v", writes)
	}
}

func TestACPCodexDeviceHTTPCancelDuringWritePreventsSuccess(t *testing.T) { //nolint:gosec // test fixture validates token-shaped Codex auth JSON.
	writeStarted := make(chan string, 1)
	unblockWrite := make(chan struct{})
	defer close(unblockWrite)
	env := newACPCodexDeviceHTTPTestEnv(t, &acpCodexDeviceIntegrationProvider{
		device: providers.OpenAICodexACPDeviceAuthorization{
			DeviceAuthID:    "device-auth-1",
			UserCode:        "CODE-123",
			VerificationURL: "https://auth.openai.com/codex/device",
			IntervalSeconds: 1,
		},
		pollResults: []providers.OpenAICodexACPDevicePollResult{
			{
				AuthorizationCode: "authorization-code",
				CodeVerifier:      "code-verifier",
			},
		},
		creds: providers.OpenAICodexOAuthCredentials{
			AccessToken:  testCodexAccessJWT,
			IDToken:      testCodexIDJWT,
			RefreshToken: testCodexRefreshFixture,
			AccountID:    "account-123",
		},
	})
	env.recorder.writeStarted = writeStarted
	env.recorder.unblockWrite = unblockWrite

	authorizeRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/authorize", nil)
	if authorizeRec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d, body = %s", authorizeRec.Code, authorizeRec.Body.String())
	}
	var authorizeResp ACPCodexOAuthDeviceAuthorizeResponse
	if err := json.Unmarshal(authorizeRec.Body.Bytes(), &authorizeResp); err != nil {
		t.Fatalf("decode authorize response: %v", err)
	}

	pollDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		pollDone <- env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/poll", ACPCodexOAuthDeviceSessionRequest{
			SessionID: authorizeResp.SessionID,
		})
	}()

	select {
	case path := <-writeStarted:
		if path != acpclient.CodexManagedConfigDir+"/auth.json" {
			t.Fatalf("first write path = %q, want auth.json", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for auth write")
	}

	cancelRec := env.postJSON(t, http.MethodPost, "/bots/"+env.botID+"/acp/codex/oauth/device/cancel", ACPCodexOAuthDeviceSessionRequest{
		SessionID: authorizeResp.SessionID,
	})
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelRec.Code, cancelRec.Body.String())
	}
	cancelled := decodeACPCodexDeviceStatus(t, cancelRec)
	if cancelled.Status != string(acpCodexDeviceAuthStatusCancelled) || cancelled.HasToken {
		t.Fatalf("cancel response = %+v", cancelled)
	}

	var pollRec *httptest.ResponseRecorder
	select {
	case pollRec = <-pollDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poll response")
	}
	if pollRec.Code != http.StatusOK {
		t.Fatalf("poll status = %d, body = %s", pollRec.Code, pollRec.Body.String())
	}
	pollResp := decodeACPCodexDeviceStatus(t, pollRec)
	if pollResp.Status != string(acpCodexDeviceAuthStatusCancelled) || pollResp.HasToken {
		t.Fatalf("poll response after cancel = %+v", pollResp)
	}
	if writes := env.recorder.writes(); len(writes) != 0 {
		t.Fatalf("cancelled write should not record Codex auth files: %#v", writes)
	}
}

func testCodexDeviceSession(sessionID string, now time.Time) *acpCodexDeviceAuthSession {
	return &acpCodexDeviceAuthSession{
		SessionID:         sessionID,
		BotID:             "bot-1",
		ChannelIdentityID: "identity-1",
		DeviceAuthID:      "device-auth-1",
		UserCode:          "CODE-123",
		VerificationURL:   "https://auth.openai.com/codex/device",
		CreatedAt:         now,
		ExpiresAt:         now.Add(acpCodexDeviceAuthTTL),
		Interval:          acpCodexDeviceAuthMinInterval,
		NextPollAfter:     now,
		Status:            acpCodexDeviceAuthStatusPending,
		Generation:        1,
	}
}

type acpCodexDeviceHTTPTestEnv struct {
	echo     *echo.Echo
	handler  *ACPCodexOAuthHandler
	provider *acpCodexDeviceIntegrationProvider
	recorder *usersACPConfigBridgeServer
	botID    string
	userID   string
}

type recordingCodexOAuthRuntimeReset struct{}

func (*recordingCodexOAuthRuntimeReset) BeginBotHistoryReset(ctx context.Context, botID string) (context.Context, func(), error) {
	resetCtx := runtimefence.WithResetContext(ctx, runtimefence.ResetFence{
		Scope: "bot", BotID: botID, Token: "99999999-9999-4999-8999-999999999999",
	})
	return resetCtx, func() {}, nil
}

func newACPCodexDeviceHTTPTestEnv(t *testing.T, provider *acpCodexDeviceIntegrationProvider) *acpCodexDeviceHTTPTestEnv {
	t.Helper()
	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	client, recorder := newUsersACPConfigBridgeClient(t)
	queries := &acpCodexDeviceIntegrationQueries{
		bot: testBotRow(botID, map[string]any{}),
	}
	handler := &ACPCodexOAuthHandler{
		provider:       provider,
		botService:     bots.NewService(nil, queries),
		accountService: newTestAdminAccountService("member"),
		acpWorkspace: &usersACPConfigWorkspace{
			backend: bridge.WorkspaceBackendContainer,
			client:  client,
		},
		runtimeResets:  &recordingCodexOAuthRuntimeReset{},
		callbackURL:    "http://localhost:1455/auth/callback",
		states:         map[string]acpCodexOAuthState{},
		deviceSessions: map[string]*acpCodexDeviceAuthSession{},
	}
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("user", &jwt.Token{
				Valid: true,
				Claims: jwt.MapClaims{
					"sub":     userID,
					"user_id": userID,
				},
			})
			return next(c)
		}
	})
	handler.Register(e)
	return &acpCodexDeviceHTTPTestEnv{
		echo:     e,
		handler:  handler,
		provider: provider,
		recorder: recorder,
		botID:    botID,
		userID:   userID,
	}
}

func (e *acpCodexDeviceHTTPTestEnv) postJSON(t *testing.T, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(reqBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

func decodeACPCodexDeviceStatus(t *testing.T, rec *httptest.ResponseRecorder) ACPCodexOAuthDeviceStatusResponse {
	t.Helper()
	var status ACPCodexOAuthDeviceStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode device status: %v\n%s", err, rec.Body.String())
	}
	return status
}

type acpCodexDeviceIntegrationQueries struct {
	dbstore.Queries
	bot sqlc.GetBotByIDRow
}

func (q *acpCodexDeviceIntegrationQueries) GetBotByID(_ context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	if !id.Valid || id != q.bot.ID {
		return sqlc.GetBotByIDRow{}, errors.New("bot not found")
	}
	return q.bot, nil
}

func (*acpCodexDeviceIntegrationQueries) SupportsTransactions() bool { return true }

func (q *acpCodexDeviceIntegrationQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	return fn(q)
}

func (*acpCodexDeviceIntegrationQueries) LockBotForRuntimeReset(_ context.Context, botID pgtype.UUID) (pgtype.UUID, error) {
	return botID, nil
}

func (*acpCodexDeviceIntegrationQueries) ValidateLockedBotRuntimeReset(_ context.Context, arg sqlc.ValidateLockedBotRuntimeResetParams) (pgtype.UUID, error) {
	return arg.BotID, nil
}

func (*acpCodexDeviceIntegrationQueries) BumpBotRuntimeConfigEpoch(context.Context, pgtype.UUID) (int64, error) {
	return 1, nil
}

func (*acpCodexDeviceIntegrationQueries) RefreshLockedBotRuntimeReset(context.Context, sqlc.RefreshLockedBotRuntimeResetParams) (pgtype.Timestamptz, error) {
	return pgtype.Timestamptz{Valid: true}, nil
}

type acpCodexDevicePollRequest struct {
	DeviceAuthID string
	UserCode     string
}

type acpCodexDeviceIntegrationProvider struct {
	mu sync.Mutex

	device      providers.OpenAICodexACPDeviceAuthorization
	pollResults []providers.OpenAICodexACPDevicePollResult
	creds       providers.OpenAICodexOAuthCredentials

	pollStarted     chan struct{}
	pollStartedOnce sync.Once
	unblockPoll     <-chan struct{}

	polls                 []acpCodexDevicePollRequest
	exchangeCodes         []string
	exchangeCodeVerifiers []string
}

func (*acpCodexDeviceIntegrationProvider) StartOpenAICodexACPAuthorization(context.Context, string, string) (*providers.OAuthAuthorizeResponse, string, error) {
	return &providers.OAuthAuthorizeResponse{AuthURL: "https://auth.example.test/authorize"}, "code-verifier", nil
}

func (p *acpCodexDeviceIntegrationProvider) ExchangeOpenAICodexACPCode(context.Context, string, string, string) (providers.OpenAICodexOAuthCredentials, error) {
	return p.creds, nil
}

func (p *acpCodexDeviceIntegrationProvider) StartOpenAICodexACPDeviceAuthorization(context.Context) (providers.OpenAICodexACPDeviceAuthorization, error) {
	return p.device, nil
}

func (p *acpCodexDeviceIntegrationProvider) PollOpenAICodexACPDeviceAuthorization(ctx context.Context, deviceAuthID, userCode string) (providers.OpenAICodexACPDevicePollResult, error) {
	if p.pollStarted != nil {
		p.pollStartedOnce.Do(func() { close(p.pollStarted) })
	}
	if p.unblockPoll != nil {
		select {
		case <-p.unblockPoll:
		case <-ctx.Done():
			return providers.OpenAICodexACPDevicePollResult{}, ctx.Err()
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls = append(p.polls, acpCodexDevicePollRequest{
		DeviceAuthID: deviceAuthID,
		UserCode:     userCode,
	})
	if len(p.pollResults) == 0 {
		return providers.OpenAICodexACPDevicePollResult{Pending: true}, nil
	}
	result := p.pollResults[0]
	p.pollResults = p.pollResults[1:]
	return result, nil
}

func (p *acpCodexDeviceIntegrationProvider) ExchangeOpenAICodexACPDeviceCode(_ context.Context, authorizationCode, codeVerifier string) (providers.OpenAICodexOAuthCredentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exchangeCodes = append(p.exchangeCodes, authorizationCode)
	p.exchangeCodeVerifiers = append(p.exchangeCodeVerifiers, codeVerifier)
	return p.creds, nil
}

func (p *acpCodexDeviceIntegrationProvider) pollCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.polls)
}

func (p *acpCodexDeviceIntegrationProvider) exchangeCode() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.exchangeCodes) == 0 {
		return ""
	}
	return p.exchangeCodes[len(p.exchangeCodes)-1]
}

func (p *acpCodexDeviceIntegrationProvider) exchangeCodeVerifier() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.exchangeCodeVerifiers) == 0 {
		return ""
	}
	return p.exchangeCodeVerifiers[len(p.exchangeCodeVerifiers)-1]
}
