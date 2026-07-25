package provider

import (
	"net/http"

	sdk "github.com/memohai/twilight-ai/sdk"

	memohcopilot "github.com/memohai/memoh/domains/model/internal/provider/copilot"
)

// NewCopilotSDKProvider builds a Twilight GitHub Copilot provider from a
// resolved Copilot API token.
func NewCopilotSDKProvider(copilotToken string, baseClient *http.Client) sdk.Provider {
	return memohcopilot.NewProvider(copilotToken, baseClient)
}

// NewCopilotSDKModel builds a Twilight GitHub Copilot chat model.
func NewCopilotSDKModel(copilotToken, modelID string, baseClient *http.Client) *sdk.Model {
	return memohcopilot.NewModel(copilotToken, modelID, baseClient)
}
