package bot

import (
	"testing"

	"github.com/labstack/echo/v4"
)

func TestUsersHandlerDoesNotRegisterAdminPasswordReset(t *testing.T) {
	e := echo.New()
	(&UsersHandler{}).Register(e)

	routes := make(map[string]bool)
	for _, route := range e.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	if !routes["PUT /users/me/password"] {
		t.Fatal("self-service password update route is not registered")
	}
	if routes["PUT /users/:id/password"] {
		t.Fatal("team-admin password reset route must not be registered")
	}
}
