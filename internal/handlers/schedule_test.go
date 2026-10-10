package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/schedule"
	"github.com/felinics/memoh/internal/workdir"
)

func TestScheduleServiceErrorNamesTheField(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		code  apperror.Code
		field string
	}{
		{"required", schedule.ErrTargetSessionRequired, apperror.CodeRequestFieldRequired, "target_session_id"},
		{"required model", fmt.Errorf("create: %w", schedule.ErrModelRequired), apperror.CodeScheduleModelRequired, "model_id"},
		{"invalid", schedule.ErrTargetSessionNotFound, apperror.CodeRequestFieldInvalid, "target_session_id"},
		{"workdir not found", workdir.ErrWorkdirNotFound, apperror.CodeRequestFieldInvalid, "workdir_id"},
		{"workdir archived", fmt.Errorf("bind: %w", workdir.ErrWorkdirArchived), apperror.CodeRequestFieldInvalid, "workdir_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := scheduleServiceError(tc.err)
			if got := apperror.CodeOf(err); got != tc.code {
				t.Fatalf("code = %s, want %s", got, tc.code)
			}
			if got := apperror.ArgsOf(err)["field"]; got != tc.field {
				t.Fatalf("field = %q, want %q", got, tc.field)
			}
		})
	}
}

func TestScheduleRulesMapToTheirOwnCodes(t *testing.T) {
	for rule, code := range scheduleRuleCodes {
		if _, ok := apperror.Lookup(code); !ok {
			t.Errorf("rule %s maps to %s, which is not in the catalog", rule, code)
		}
	}
	for _, rule := range []schedule.Rule{
		schedule.RuleRunTargetConflict, schedule.RuleModelConflict, schedule.RuleModelUnusable,
		schedule.RuleModelRequired, schedule.RuleSessionModeUnsupported,
	} {
		if _, ok := scheduleRuleCodes[rule]; !ok {
			t.Errorf("rule %s has no public code", rule)
		}
	}
}

func TestScheduleServiceErrorKeepsInternalErrorsInternal(t *testing.T) {
	err := scheduleServiceError(errors.New("db down"))
	answer, _ := errs.Answer(t.Context(), err)
	if apperror.CodeOf(answer) != apperror.CodeInternal {
		t.Fatalf("code = %s, want %s", apperror.CodeOf(answer), apperror.CodeInternal)
	}
}

func TestScheduleLookupErrorAnswersAnUnknownScheduleWith404(t *testing.T) {
	err := scheduleLookupError(fmt.Errorf("get: %w", schedule.ErrScheduleNotFound))
	if apperror.CodeOf(err) != apperror.CodeScheduleNotFound || !errors.Is(apperror.CauseOf(err), schedule.ErrScheduleNotFound) {
		t.Fatalf("error = %v, want schedule.not_found caused by ErrScheduleNotFound", err)
	}
}

func TestScheduleHandlerPathParamsAreRequiredFields(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		name   string
		params map[string]string
		call   func(*ScheduleHandler, echo.Context) error
		field  string
	}{
		{"create bot_id", nil, (*ScheduleHandler).Create, "bot_id"},
		{"list bot_id", nil, (*ScheduleHandler).List, "bot_id"},
		{"get id", map[string]string{"bot_id": "b"}, (*ScheduleHandler).Get, "id"},
		{"update id", map[string]string{"bot_id": "b"}, (*ScheduleHandler).Update, "id"},
		{"delete id", map[string]string{"bot_id": "b"}, (*ScheduleHandler).Delete, "id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			c := testAuthContext(e, httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder(), userID)
			var names, values []string
			for k, v := range tc.params {
				names = append(names, k)
				values = append(values, v)
			}
			c.SetParamNames(names...)
			c.SetParamValues(values...)
			err := tc.call(&ScheduleHandler{}, c)
			if apperror.CodeOf(err) != apperror.CodeRequestFieldRequired || apperror.ArgsOf(err)["field"] != tc.field {
				t.Fatalf("error = %v, want %s for %q", err, apperror.CodeRequestFieldRequired, tc.field)
			}
		})
	}
}
