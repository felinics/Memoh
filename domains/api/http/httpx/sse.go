package httpx

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// PrepareSSE makes the response ready to stream: it sets the event-stream
// headers and lifts the server WriteTimeout, which would otherwise cut the
// stream off while the client is still reading. Every SSE handler must call it
// before its first write.
func PrepareSSE(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set(echo.HeaderCacheControl, "no-cache")
	c.Response().Header().Set(echo.HeaderConnection, "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")
	return ClearWriteDeadline(c)
}

func WriteSSEJSON(writer io.Writer, flusher http.Flusher, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return WriteSSEData(writer, flusher, string(data))
}

func WriteSSEData(writer io.Writer, flusher http.Flusher, payload string) error {
	// SSE frames are line-oriented; fold CR/LF to avoid frame injection.
	safePayload := strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(payload)
	if _, err := io.WriteString(writer, "data: "); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, safePayload); err != nil { //nolint:gosec // G705: SSE body is plain text and CR/LF are escaped above
		return err
	}
	if _, err := io.WriteString(writer, "\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
