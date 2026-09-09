package handlers

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/audio/adapter"
)

func transcriptionHTTPError(err error) error {
	code := apperror.CodeTranscriptionFailed
	switch {
	case errors.Is(err, adapter.ErrInvalidInput):
		code = apperror.CodeTranscriptionRequestInvalid
	case errors.Is(err, adapter.ErrAudioTooLarge):
		code = apperror.CodeTranscriptionAudioTooLarge
	case errors.Is(err, adapter.ErrRequestRejected):
		code = apperror.CodeTranscriptionRequestRejected
	case errors.Is(err, adapter.ErrRateLimited):
		code = apperror.CodeTranscriptionRateLimited
	case errors.Is(err, adapter.ErrUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = apperror.CodeTranscriptionUnavailable
	}
	return apperror.Wrap(code, err, nil)
}
