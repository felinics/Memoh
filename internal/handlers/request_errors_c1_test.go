package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/display"
	"github.com/felinics/memoh/internal/errs"
	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/userruntime"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

type fieldCase struct {
	name  string
	err   error
	code  apperror.Code
	field string
}

func requireFieldCases(t *testing.T, answer func(error) error, cases []fieldCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := answer(tc.err)
			requireFieldError(t, got, tc.code, tc.field)
			if fault := errs.FaultOf(got); fault != apperror.FaultClient {
				t.Fatalf("fault = %q, want client", fault)
			}
		})
	}
}

func TestWorkdirErrorsNameTheirField(t *testing.T) {
	requireFieldCases(t, workdirHTTPError, []fieldCase{
		{"name required", workdir.ErrNameRequired, apperror.CodeRequestFieldRequired, "name"},
		{"path required", workdir.ErrPathRequired, apperror.CodeRequestFieldRequired, "path"},
		{"invalid path", fmt.Errorf("%w: x", workdir.ErrInvalidPath), apperror.CodeRequestFieldInvalid, "path"},
		{"path missing", workdir.ErrPathNotFound, apperror.CodeRequestFieldInvalid, "path"},
		{"path not directory", workdir.ErrPathNotDirectory, apperror.CodeRequestFieldInvalid, "path"},
	})
	requireFieldError(t, workdirDirectoriesHTTPError(workdir.ErrPathNotFound), apperror.CodeRequestFieldInvalid, "path")
}

func TestACLErrorsNameTheirField(t *testing.T) {
	h := &ACLHandler{}
	requireFieldCases(t, h.mapRuleError, []fieldCase{
		{"subject", acl.ErrInvalidRuleSubject, apperror.CodeRequestFieldInvalid, "channel_identity_id"},
		{"scope", acl.ErrInvalidSourceScope, apperror.CodeRequestFieldInvalid, "source_scope"},
		{"effect", acl.ErrInvalidEffect, apperror.CodeRequestFieldInvalid, "effect"},
	})
	if got := h.mapRuleError(errors.New("boom")); apperror.CodeOf(got) == apperror.CodeRequestFieldInvalid {
		t.Fatalf("unexpected failure answered as a field error: %v", got)
	}
}

func TestBotGrantErrorsNameTheirField(t *testing.T) {
	h := &BotUserAccessHandler{}
	requireFieldCases(t, h.mapGrantError, []fieldCase{
		{"owner user", bots.ErrOwnerUserNotFound, apperror.CodeRequestFieldInvalid, "user_id"},
		{"permission", bots.ErrInvalidPermission, apperror.CodeRequestFieldInvalid, "permissions"},
		{"subject", bots.ErrInvalidGrantSubject, apperror.CodeRequestFieldInvalid, "subject_type"},
		{"user required", bots.ErrGrantUserRequired, apperror.CodeRequestFieldRequired, "user_id"},
		{"owner conflict", bots.ErrGrantOwnerConflict, apperror.CodeRequestFieldInvalid, "user_id"},
	})
}

func TestCreateAndUpdateBotErrors(t *testing.T) {
	requireFieldError(t, createBotHTTPError(bots.ErrOwnerUserNotFound, false), apperror.CodeRequestFieldInvalid, "owner_id")
	requireFieldError(t, createBotHTTPError(acl.ErrUnknownPreset, true), apperror.CodeRequestFieldInvalid, "acl_preset")
	for _, cause := range []error{bots.ErrBotNameInvalid, bots.ErrBotNameReserved} {
		if got := apperror.CodeOf(createBotHTTPError(cause, true)); got != apperror.CodeBotNameInvalid {
			t.Fatalf("create code = %q, want %q", got, apperror.CodeBotNameInvalid)
		}
		if got := apperror.CodeOf(updateBotHTTPError(cause)); got != apperror.CodeBotNameInvalid {
			t.Fatalf("update code = %q, want %q", got, apperror.CodeBotNameInvalid)
		}
	}
}

func TestRemoveMemberRefusesSelf(t *testing.T) {
	userID := uuid.NewString()
	h := &UsersHandler{service: newTestAdminAccountService("admin")}
	c := fieldErrorContext(http.MethodDelete, "", userID, map[string]string{"id": userID})
	err := h.RemoveMember(c)
	if got := apperror.CodeOf(err); got != apperror.CodeUserCannotRemoveSelf {
		t.Fatalf("error = %v, want %s", err, apperror.CodeUserCannotRemoveSelf)
	}
}

func TestSettingsModelRefNamesItsField(t *testing.T) {
	for _, field := range []string{"chat_model_id", "compaction_model_id", "memory_llm_model_id", "image_model_id"} {
		t.Run(field, func(t *testing.T) {
			err := fmt.Errorf("update: %w", &settings.InvalidModelRefError{Field: field, Err: fmt.Errorf("%w: model not found: x", settings.ErrInvalidModelRef)})
			requireFieldError(t, settingsModelRefHTTPError(err), apperror.CodeRequestFieldInvalid, field)
		})
	}
	if got := settingsModelRefHTTPError(errors.New("boom")); got != nil {
		t.Fatalf("unrelated error answered as %v", got)
	}
}

func TestNetworkErrors(t *testing.T) {
	got := networkHTTPError(fmt.Errorf("%w for bot b", netctl.ErrProviderNotConfigured), "status")
	if apperror.CodeOf(got) != apperror.CodeNetworkProviderNotConfigured {
		t.Fatalf("error = %v, want %s", got, apperror.CodeNetworkProviderNotConfigured)
	}
	requireFieldError(t, networkHTTPError(fmt.Errorf("%w %q", netctl.ErrUnsupportedAction, "x"), "action"), apperror.CodeRequestFieldInvalid, "action_id")
	internal := networkHTTPError(errors.New("provider down"), "status")
	var httpErr *echo.HTTPError
	if errors.As(internal, &httpErr) || apperror.CodeOf(internal) == apperror.CodeNetworkProviderNotConfigured {
		t.Fatalf("provider failure answered as a user error: %v", internal)
	}
	if fault := errs.FaultOf(internal); fault != apperror.FaultServer {
		t.Fatalf("fault = %q, want server", fault)
	}
}

func TestDisplayOfferErrors(t *testing.T) {
	requireFieldCases(t, displayOfferHTTPError, []fieldCase{
		{"missing sdp", display.ErrOfferRequired, apperror.CodeRequestFieldRequired, "sdp"},
		{"type", fmt.Errorf("%w %q", display.ErrOfferTypeUnsupported, "answer"), apperror.CodeRequestFieldInvalid, "type"},
		{"bad sdp", fmt.Errorf("%w: x", display.ErrOfferInvalid), apperror.CodeRequestFieldInvalid, "sdp"},
		{"no codec", fmt.Errorf("%w: x", display.ErrCodecUnsupported), apperror.CodeRequestFieldInvalid, "sdp"},
	})
	if got := apperror.CodeOf(displayOfferHTTPError(display.ErrDisplayDisabled)); got != apperror.CodeWorkspaceDisplayDisabled {
		t.Fatalf("disabled code = %q", got)
	}
	var httpErr *echo.HTTPError
	if err := displayOfferHTTPError(display.ErrEncoderUnavailable); !errors.As(err, &httpErr) || httpErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("encoder error = %v, want 503", err)
	}
	internal := displayOfferHTTPError(errors.New("pion exploded"))
	if errors.As(internal, &httpErr) || errs.FaultOf(internal) != apperror.FaultServer {
		t.Fatalf("internal failure = %v, want an internal fault", internal)
	}
}

func TestToolApprovalModeErrorsNameTheirField(t *testing.T) {
	valid := settings.ToolApprovalAllow
	for field, modes := range map[string]workspace.WorkspaceTargetToolApproval{
		"read":  {Read: "bogus", Write: valid, Exec: valid},
		"write": {Read: valid, Write: "bogus", Exec: valid},
		"exec":  {Read: valid, Write: valid, Exec: "bogus"},
	} {
		t.Run(field, func(t *testing.T) {
			_, err := workspace.ApplyWorkspaceToolApprovalModes(settings.ToolApprovalConfig{}, modes)
			requireFieldError(t, workspaceTargetHTTPError(err), apperror.CodeRequestFieldInvalid, field)
		})
	}
}

type validationOnlyRuntimeStore struct{ dbstore.UserRuntimeStore }

func TestUserRuntimeErrorsNameTheirField(t *testing.T) {
	h := &UserRuntimeHandler{service: userruntime.NewService(validationOnlyRuntimeStore{}, nil)}
	userID := uuid.NewString()

	c := fieldErrorContext(http.MethodDelete, "", userID, map[string]string{"id": "not-a-uuid"})
	requireFieldError(t, h.Delete(c), apperror.CodeRequestFieldInvalid, "id")

	c = fieldErrorContext(http.MethodPost, `{"name":"bad\u0000name"}`, userID, nil)
	requireFieldError(t, h.Create(c), apperror.CodeRequestFieldInvalid, "name")

	// An invalid input that no request field explains is not the caller's.
	if err := runtimeHTTPError(userruntime.ErrInvalidInput); apperror.CodeOf(err) == apperror.CodeRequestFieldInvalid {
		t.Fatalf("answered as a field error: %v", err)
	}
}

func TestSendChannelMessageErrors(t *testing.T) {
	requireFieldError(t, sendChannelMessageHTTPError(channel.ErrSendTargetRequired, true), apperror.CodeRequestFieldRequired, "target")
	if got := apperror.CodeOf(sendChannelMessageHTTPError(channel.ErrChannelBindingRequired, true)); got != apperror.CodeChannelBindingRequired {
		t.Fatalf("binding code = %q", got)
	}
	if err := sendChannelMessageHTTPError(channel.ErrChannelConfigNotFound, true); apperror.CodeOf(err) != apperror.CodeChannelConfigNotFound {
		t.Fatalf("missing config = %v, want channel.config_not_found", err)
	}
	// A route-supplied target is never the caller's field.
	fromRoute := sendChannelMessageHTTPError(channel.ErrSendTargetRequired, false)
	if apperror.CodeOf(fromRoute) == apperror.CodeRequestFieldRequired {
		t.Fatalf("route target answered as a request field: %v", fromRoute)
	}
	delivery := sendChannelMessageHTTPError(errors.New("telegram: chat not found"), true)
	var httpErr *echo.HTTPError
	if errors.As(delivery, &httpErr) || errs.FaultOf(delivery) != apperror.FaultDependency {
		t.Fatalf("delivery failure = %v, want a dependency fault", delivery)
	}
}
