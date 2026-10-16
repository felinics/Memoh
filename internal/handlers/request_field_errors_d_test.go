package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/server"
)

func problemOf(t *testing.T, err error) server.Problem {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))(err, e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec))
	var p server.Problem
	if jsonErr := json.Unmarshal(rec.Body.Bytes(), &p); jsonErr != nil {
		t.Fatalf("decode problem: %v", jsonErr)
	}
	return p
}

func TestSessionAndMessageRequestErrors(t *testing.T) {
	descErr := func() error {
		_, _, _, err := session.ResolveDescriptor("chat", "bogus", "")
		return descriptorFieldError(err)
	}
	runtimeErr := func() error {
		_, _, _, err := session.ResolveDescriptor("chat", "", "bogus")
		return descriptorFieldError(err)
	}
	conflictErr := func() error {
		_, _, _, err := session.ResolveDescriptor(session.TypeACPAgent, "", session.RuntimeModel)
		return descriptorFieldError(err)
	}
	_, limitErr := parseSessionLimitParam("999")
	_, _, typesErr := parseSessionTypesParam(" , ", false)
	cases := []struct {
		name   string
		err    error
		code   apperror.Code
		field  string
		status int
	}{
		{"unknown session mode", descErr(), apperror.CodeRequestFieldInvalid, "session_mode", 400},
		{"unknown runtime type", runtimeErr(), apperror.CodeRequestFieldInvalid, "runtime_type", 400},
		{"acp type conflict", conflictErr(), apperror.CodeRequestFieldInvalid, "runtime_type", 400},
		{"external runtime mode", rejectSystemExternalRuntime(session.TypeSchedule, session.RuntimeACPAgent), apperror.CodeRequestFieldInvalid, "session_mode", 400},
		{"limit out of range", limitErr, apperror.CodeRequestFieldInvalid, "limit", 400},
		{"empty types", typesErr, apperror.CodeRequestFieldInvalid, "types", 400},
		{"preference invalid", modelPreferenceError(fmt.Errorf("x: %w", application.ErrModelPreferenceInvalid), "op"), apperror.CodeRequestFieldInvalid, "preferred_chat_model_id", 400},
		{"preference revision", modelPreferenceError(application.ErrModelPreferenceRevisionInvalid, "op"), apperror.CodeRequestFieldInvalid, "expected_model_preference_revision", 400},
		{"acp preference", modelPreferenceError(application.ErrACPPreferenceUnsupported, "op"), apperror.CodeACPModelSelectionUnsupported, "", 400},
		{"preference internal", modelPreferenceError(errors.New("db down"), "op"), apperror.CodeInternal, "", 500},
		{"descriptor internal", descriptorFieldError(errors.New("boom")), apperror.CodeInternal, "", 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := problemOf(t, tc.err)
			if p.Code != string(tc.code) || p.Status != tc.status || p.Args["field"] != tc.field {
				t.Fatalf("problem = %+v, want %s %d field %q", p, tc.code, tc.status, tc.field)
			}
			wantFault := apperror.FaultClient
			if tc.status == 500 {
				wantFault = apperror.FaultServer
			}
			if p.Fault != wantFault {
				t.Fatalf("fault = %s, want %s", p.Fault, wantFault)
			}
		})
	}
}

func TestNewSessionCodesAnswerAsClientFault(t *testing.T) {
	for _, code := range []apperror.Code{apperror.CodeChatMessageEmpty, apperror.CodeWorkdirRemoteUnsupportedForAgent} {
		p := problemOf(t, apperror.New(code, nil))
		if p.Code != string(code) || p.Status != http.StatusBadRequest || p.Fault != apperror.FaultClient {
			t.Fatalf("problem = %+v, want %s 400 client", p, code)
		}
	}
}
