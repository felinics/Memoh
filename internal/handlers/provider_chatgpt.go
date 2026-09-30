package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/chatgptplan"
	"github.com/felinics/memoh/internal/providerfail"
)

func (h *ProviderOAuthHandler) chatGPTAuthorizationError(c echo.Context, err error) error {
	mapped := providerfail.ChatGPT(c.Request().Context(), h.logger, err)
	if apperror.CodeOf(mapped) != "" {
		return mapped
	}
	return apperror.Wrap(apperror.CodeChatGPTUnavailable, err, nil)
}

// BeginChatGPT prepares a local authorization attempt.
//
// @Summary Prepare local Sign in with ChatGPT
// @Tags providers-oauth
// @ID beginChatGPTAuthorization
// @Param id path string true "Provider ID"
// @Param request body chatgptplan.BeginRequest true "Local callback"
// @Success 200 {object} chatgptplan.BeginResponse
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /providers/{id}/chatgpt/authorize [post]
//
//nolint:godot // Swag annotations are metadata.
func (h *ProviderOAuthHandler) BeginChatGPT(c echo.Context) error {
	user, err := auth.UserIDFromContext(c)
	if err != nil {
		return apperror.New(apperror.CodeChatGPTOwnerRequired, nil)
	}
	var req chatgptplan.BeginRequest
	if c.Bind(&req) != nil {
		return apperror.New(apperror.CodeChatGPTAuthorizationInvalid, nil)
	}
	result, err := h.service.BeginChatGPTAuthorization(c.Request().Context(), c.Param("id"), user, req)
	if err != nil {
		return h.chatGPTAuthorizationError(c, err)
	}
	// Authorization URLs can contain an ID token hint. Never cache this response.
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, result)
}

// RetainChatGPTRegistration saves an issued client without activating credentials.
//
// @Summary Retain the client issued to a pending ChatGPT authorization
// @Tags providers-oauth
// @ID retainChatGPTRegistration
// @Param id path string true "Provider ID"
// @Param request body chatgptplan.RegistrationRequest true "Validated local callback"
// @Success 204 "Registration retained"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /providers/{id}/chatgpt/registration [post]
//
//nolint:godot // Swag annotations are metadata.
func (h *ProviderOAuthHandler) RetainChatGPTRegistration(c echo.Context) error {
	user, err := auth.UserIDFromContext(c)
	if err != nil {
		return apperror.New(apperror.CodeChatGPTOwnerRequired, nil)
	}
	var req chatgptplan.RegistrationRequest
	if c.Bind(&req) != nil {
		return apperror.New(apperror.CodeChatGPTAuthorizationInvalid, nil)
	}
	if err := h.service.RetainChatGPTRegistration(c.Request().Context(), c.Param("id"), user, req); err != nil {
		return h.chatGPTAuthorizationError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// CompleteChatGPT imports verified credentials.
//
// @Summary Import a locally exchanged ChatGPT grant
// @Tags providers-oauth
// @ID completeChatGPTAuthorization
// @Param id path string true "Provider ID"
// @Param request body chatgptplan.CompleteRequest true "Locally exchanged grant"
// @Success 200 {object} chatgptplan.Status
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /providers/{id}/chatgpt/complete [post]
//
//nolint:godot // Swag annotations are metadata.
func (h *ProviderOAuthHandler) CompleteChatGPT(c echo.Context) error {
	user, err := auth.UserIDFromContext(c)
	if err != nil {
		return apperror.New(apperror.CodeChatGPTOwnerRequired, nil)
	}
	var req chatgptplan.CompleteRequest
	if c.Bind(&req) != nil {
		return apperror.New(apperror.CodeChatGPTAuthorizationInvalid, nil)
	}
	result, err := h.service.CompleteChatGPTAuthorization(c.Request().Context(), c.Param("id"), user, req)
	if err != nil {
		return h.chatGPTAuthorizationError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, result)
}

// ChatGPTStatus reports the redacted authorization state.
//
// @Summary Get redacted ChatGPT connection state
// @Tags providers-oauth
// @ID getChatGPTAuthorizationStatus
// @Param id path string true "Provider ID"
// @Success 200 {object} chatgptplan.Status
// @Failure 400 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /providers/{id}/chatgpt/status [get]
//
//nolint:godot // Swag annotations are metadata.
func (h *ProviderOAuthHandler) ChatGPTStatus(c echo.Context) error {
	result, err := h.service.ChatGPTAuthorizationStatus(c.Request().Context(), c.Param("id"))
	if err != nil {
		return h.chatGPTAuthorizationError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, result)
}

// RevokeChatGPT disconnects an authorized account.
//
// @Summary Disconnect ChatGPT, preserving registration for sign-in
// @Tags providers-oauth
// @ID revokeChatGPTAuthorization
// @Param id path string true "Provider ID"
// @Success 204 "Disconnected"
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} apperror.Problem
// @Failure 503 {object} apperror.Problem
// @Router /providers/{id}/chatgpt/token [delete]
//
//nolint:godot // Swag annotations are metadata.
func (h *ProviderOAuthHandler) RevokeChatGPT(c echo.Context) error {
	user, err := auth.UserIDFromContext(c)
	if err != nil {
		return apperror.New(apperror.CodeChatGPTOwnerRequired, nil)
	}
	if err = h.service.RevokeChatGPTAuthorization(c.Request().Context(), c.Param("id"), user); err != nil {
		return h.chatGPTAuthorizationError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
