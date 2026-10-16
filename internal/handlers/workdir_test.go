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
		code int
	}{
		"duplicate path":     {workdir.ErrDuplicatePath, http.StatusConflict},
		"archived":           {workdir.ErrWorkdirArchived, http.StatusConflict},
		"runtime offline":    {workspace.ErrRemoteRuntimeOffline, http.StatusConflict},
		"unexpected failure": {errors.New("boom"), 0},
	} {
		t.Run(name, func(t *testing.T) {
			err := workdirHTTPError(tc.err)
			var httpErr *echo.HTTPError
			if tc.code == 0 {
				// An unexpected failure is returned with its cause for the
				// boundary to answer as internal and record.
				if errors.As(err, &httpErr) || !errors.Is(err, tc.err) {
					t.Fatalf("error = %v, want the cause without an HTTP status", err)
				}
				return
			}
			if !errors.As(err, &httpErr) || httpErr.Code != tc.code {
				t.Fatalf("error = %v, want HTTP %d", err, tc.code)
			}
		})
	}
}

func TestWorkdirDirectoriesHTTPError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"runtime offline":    {workspace.ErrRemoteRuntimeOffline, http.StatusServiceUnavailable},
		"bridge unavailable": {fmt.Errorf("list: %w", bridge.ErrUnavailable), http.StatusServiceUnavailable},
		"path forbidden":     {workdir.ErrPathForbidden, http.StatusForbidden},
		"runtime revoked":    {workspace.ErrRemoteRuntimeRevoked, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			err := workdirDirectoriesHTTPError(tc.err)
			var httpErr *echo.HTTPError
			if errors.As(err, &httpErr) {
				if httpErr.Code != tc.code {
					t.Fatalf("error = %v, want HTTP %d", err, tc.code)
				}
				return
			}
			if apperror.CodeOf(err) != apperror.CodeWorkspaceUnreachable || tc.code != http.StatusServiceUnavailable {
				t.Fatalf("error = %v, want HTTP %d", err, tc.code)
			}
		})
	}
}
