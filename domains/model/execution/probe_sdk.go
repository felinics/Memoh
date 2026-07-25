package execution

import (
	"context"
	"net/http"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
)

// ProbeSDK is the production implementation of domains/model/provider.ProbeSDK.
type ProbeSDK struct{}

func (ProbeSDK) NewProvider(baseURL, apiKey, codexAccountID string, clientType modeldomain.ClientType, timeout time.Duration, httpClient *http.Client) sdk.Provider {
	return NewSDKProvider(baseURL, apiKey, codexAccountID, clientType, timeout, httpClient)
}

func (ProbeSDK) InferEmbeddingDimensions(ctx context.Context, clientType, baseURL, apiKey, modelID string, timeout time.Duration, httpClient *http.Client) (int, error) {
	return InferEmbeddingDimensions(ctx, clientType, baseURL, apiKey, modelID, timeout, httpClient)
}
