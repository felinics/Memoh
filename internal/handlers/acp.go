package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/agentcredential"
)

type ACPHandler struct {
	credentials *agentcredential.Service
}

func NewACPHandler(credentials *agentcredential.Service) *ACPHandler {
	return &ACPHandler{credentials: credentials}
}

func (h *ACPHandler) Register(e *echo.Echo) {
	e.GET("/acp/profiles", h.ListProfiles)
}

// ListProfiles godoc
// @Summary List ACP profiles
// @Description List safe ACP profile metadata used by the frontend to render agent configuration UI
// @Tags acp
// @Success 200 {object} acpprofile.ProfilesResponse
// @Router /acp/profiles [get].
func (h *ACPHandler) ListProfiles(c echo.Context) error {
	return c.JSON(http.StatusOK, acpprofile.ProfilesResponse{
		Items:                     acpprofile.List(),
		CredentialStoreConfigured: h.credentials.Configured(),
	})
}
