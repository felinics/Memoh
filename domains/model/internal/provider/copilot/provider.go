package copilot

import (
	"net/http"
	"strings"

	githubcopilot "github.com/memohai/twilight-ai/provider/github/copilot"
	sdk "github.com/memohai/twilight-ai/sdk"
)

func NewProvider(copilotToken string, baseClient *http.Client) sdk.Provider {
	options := []githubcopilot.Option{
		githubcopilot.WithGitHubToken(strings.TrimSpace(copilotToken)),
		githubcopilot.WithBaseURL(DefaultAPIBaseURL),
		githubcopilot.WithHTTPClient(NewHTTPClient(baseClient)),
	}
	return githubcopilot.New(options...)
}

func NewModel(copilotToken, modelID string, baseClient *http.Client) *sdk.Model {
	options := []githubcopilot.Option{
		githubcopilot.WithGitHubToken(strings.TrimSpace(copilotToken)),
		githubcopilot.WithBaseURL(DefaultAPIBaseURL),
		githubcopilot.WithHTTPClient(NewHTTPClient(baseClient)),
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = githubcopilot.AutoModel
	}
	return githubcopilot.New(options...).ChatModel(modelID)
}
