package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/project"
	"github.com/memohai/memoh/internal/workspace"
)

func TestProjectHTTPError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"name required":      {project.ErrNameRequired, http.StatusBadRequest},
		"path required":      {project.ErrPathRequired, http.StatusBadRequest},
		"invalid path":       {project.ErrInvalidPath, http.StatusBadRequest},
		"path missing":       {project.ErrPathNotFound, http.StatusBadRequest},
		"path not directory": {project.ErrPathNotDirectory, http.StatusBadRequest},
		"project not found":  {project.ErrProjectNotFound, http.StatusNotFound},
		"target not found":   {workspace.ErrWorkspaceTargetNotFound, http.StatusNotFound},
		"db not found":       {db.ErrNotFound, http.StatusNotFound},
		"duplicate path":     {project.ErrDuplicatePath, http.StatusConflict},
		"archived":           {project.ErrProjectArchived, http.StatusConflict},
		"runtime offline":    {workspace.ErrRemoteRuntimeOffline, http.StatusConflict},
		"unexpected failure": {errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			err := projectHTTPError(nil, tc.err)
			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != tc.code {
				t.Fatalf("error = %v, want HTTP %d", err, tc.code)
			}
		})
	}
}
