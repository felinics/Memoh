package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestWorkdirNotFoundAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code apperror.Code
	}{
		"workdir not found": {workdir.ErrWorkdirNotFound, apperror.CodeWorkdirNotFound},
		"db not found":      {db.ErrNotFound, apperror.CodeWorkdirNotFound},
		"target not found":  {workspace.ErrWorkspaceTargetNotFound, apperror.CodeWorkspaceTargetNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			for _, translate := range []func(error) error{workdirHTTPError, workdirDirectoriesHTTPError} {
				err := translate(tc.err)
				if apperror.CodeOf(err) != tc.code || !errors.Is(apperror.CauseOf(err), tc.err) {
					t.Fatalf("error = %v, want %s caused by %v", err, tc.code, tc.err)
				}
				if def, _ := apperror.Lookup(tc.code); def.HTTPStatus != http.StatusNotFound {
					t.Fatalf("%s status = %d, want 404", tc.code, def.HTTPStatus)
				}
			}
		})
	}
}

func TestWorkdirHTTPError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code apperror.Code
	}{
		"duplicate path":     {workdir.ErrDuplicatePath, apperror.CodeWorkdirPathAlreadyExists},
		"archived":           {workdir.ErrWorkdirArchived, apperror.CodeWorkdirArchived},
		"runtime offline":    {workspace.ErrRemoteRuntimeOffline, apperror.CodeWorkspaceTargetOffline},
		"runtime revoked":    {workspace.ErrRemoteRuntimeRevoked, apperror.CodeWorkspaceTargetRevoked},
		"owner mismatch":     {workspace.ErrRemoteRuntimeOwnerMismatch, apperror.CodeWorkspaceTargetOwnerMismatch},
		"client too old":     {workspace.ErrRemoteRuntimeClientUpdateNeeded, apperror.CodeWorkspaceTargetClientUpdateRequired},
		"unexpected failure": {errors.New("boom"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := workdirHTTPError(tc.err)
			var httpErr *echo.HTTPError
			if tc.code == "" {
				// An unexpected failure is returned with its cause for the
				// boundary to answer as internal and record.
				if errors.As(err, &httpErr) || !errors.Is(err, tc.err) {
					t.Fatalf("error = %v, want the cause without an HTTP status", err)
				}
				return
			}
			if apperror.CodeOf(err) != tc.code || !errors.Is(apperror.CauseOf(err), tc.err) {
				t.Fatalf("error = %v, want %s caused by %v", err, tc.code, tc.err)
			}
		})
	}
}

func TestWorkdirDirectoriesHTTPError(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		wantStatus int
		wantCode   apperror.Code
	}{
		"runtime offline":    {workspace.ErrRemoteRuntimeOffline, http.StatusServiceUnavailable, apperror.CodeWorkspaceUnreachable},
		"bridge unavailable": {fmt.Errorf("list: %w", bridge.ErrUnavailable), http.StatusServiceUnavailable, apperror.CodeWorkspaceUnreachable},
		"path forbidden":     {workdir.ErrPathForbidden, http.StatusForbidden, ""},
		"runtime revoked":    {workspace.ErrRemoteRuntimeRevoked, http.StatusConflict, apperror.CodeWorkspaceTargetRevoked},
	} {
		t.Run(name, func(t *testing.T) {
			err := workdirDirectoriesHTTPError(tc.err)
			if tc.wantCode == "" {
				var httpErr *echo.HTTPError
				if !errors.As(err, &httpErr) || httpErr.Code != tc.wantStatus {
					t.Fatalf("error = %v, want HTTP %d", err, tc.wantStatus)
				}
				return
			}
			if apperror.CodeOf(err) != tc.wantCode || !errors.Is(apperror.CauseOf(err), tc.err) {
				t.Fatalf("error = %v, want %s caused by %v", err, tc.wantCode, tc.err)
			}
			if def, _ := apperror.Lookup(tc.wantCode); def.HTTPStatus != tc.wantStatus {
				t.Fatalf("%s status = %d, want %d", tc.wantCode, def.HTTPStatus, tc.wantStatus)
			}
		})
	}
}
