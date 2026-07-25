package media

import (
	"errors"
	"fmt"
	"io"
)

// ReadAllWithLimit reads from reader and rejects payloads larger than maxBytes.
func ReadAllWithLimit(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("reader is required")
	}
	if maxBytes <= 0 {
		return nil, errors.New("max bytes must be greater than 0")
	}
	limited := &io.LimitedReader{
		R: reader,
		N: maxBytes + 1,
	}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: max %d bytes", ErrAssetTooLarge, maxBytes)
	}
	return data, nil
}
