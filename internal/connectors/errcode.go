package connectors

import (
	"errors"
	"net/http"

	connectsdk "github.com/felinics/connect-it/sdk/go"

	"github.com/felinics/memoh/internal/apperror"
)

// CodeOf maps a connector failure to its catalog code, or "" when err is not
// one this package or the upstream SDK reports. HTTP responses and persisted
// failure reasons share it so the same upstream status reads the same
// everywhere: 4xx statuses describe the request, anything else means the
// upstream could not serve it.
func CodeOf(err error) apperror.Code {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return apperror.CodeConnectorRequestInvalid
	case errors.Is(err, ErrNotConfigured):
		return apperror.CodeConnectorNotConfigured
	}
	var apiErr *connectsdk.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return apperror.CodeConnectorRequestRejected
		case http.StatusNotFound:
			return apperror.CodeConnectorNotFound
		case http.StatusConflict:
			return apperror.CodeConnectorConflict
		}
		return apperror.CodeConnectorUpstreamUnavailable
	}
	if errors.Is(err, ErrUpstreamUnavailable) {
		return apperror.CodeConnectorUpstreamUnavailable
	}
	return ""
}
