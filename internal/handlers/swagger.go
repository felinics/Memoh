package handlers

// @title Memoh API
// @version 1.0.0

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/swaggo/swag"

	// Register the generated swagger document for swag.ReadDoc.
	_ "github.com/felinics/memoh/spec"
)

//go:generate go tool swag init -g swagger.go -o ../../spec --parseDependency --parseInternal

var (
	swaggerSpec []byte
	swaggerOnce sync.Once
	swaggerErr  error
)

type SwaggerHandler struct {
	logger *slog.Logger
}

func NewSwaggerHandler(log *slog.Logger) *SwaggerHandler {
	return &SwaggerHandler{logger: log.With(slog.String("handler", "swagger"))}
}

func (h *SwaggerHandler) Register(e *echo.Echo) {
	e.GET("api/swagger.json", h.Spec)
	e.GET("api/docs", h.UI)
	e.GET("api/docs/", h.UI)
}

func (*SwaggerHandler) Spec(c echo.Context) error {
	swaggerOnce.Do(func() {
		var doc string
		doc, swaggerErr = swag.ReadDoc()
		swaggerSpec = []byte(doc)
	})
	if swaggerErr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, swaggerErr.Error())
	}
	return c.Blob(http.StatusOK, "application/json", swaggerSpec)
}

func (*SwaggerHandler) UI(c echo.Context) error {
	return c.HTML(http.StatusOK, swaggerUIHTML)
}

const swaggerUIHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width,initial-scale=1" />
    <title>memoh-go Swagger UI</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script>
      window.onload = () => {
        window.ui = SwaggerUIBundle({
          url: '/api/swagger.json',
          dom_id: '#swagger-ui'
        });
      };
    </script>
  </body>
</html>`
