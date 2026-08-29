package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

func TestWorkdirHTTPError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"name required":      {workdir.ErrNameRequired, http.StatusBadRequest},
		"path required":      {workdir.ErrPathRequired, http.StatusBadRequest},
		"invalid path":       {workdir.ErrInvalidPath, http.StatusBadRequest},
		"path missing":       {workdir.ErrPathNotFound, http.StatusBadRequest},
		"path not directory": {workdir.ErrPathNotDirectory, http.StatusBadRequest},
		"workdir not found":  {workdir.ErrWorkdirNotFound, http.StatusNotFound},
		"target not found":   {workspace.ErrWorkspaceTargetNotFound, http.StatusNotFound},
		"db not found":       {db.ErrNotFound, http.StatusNotFound},
		"duplicate path":     {workdir.ErrDuplicatePath, http.StatusConflict},
		"archived":           {workdir.ErrWorkdirArchived, http.StatusConflict},
		"runtime offline":    {workspace.ErrRemoteRuntimeOffline, http.StatusConflict},
		"unexpected failure": {errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			err := workdirHTTPError(nil, tc.err)
			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != tc.code {
				t.Fatalf("error = %v, want HTTP %d", err, tc.code)
			}
		})
	}
}
