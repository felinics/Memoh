package httpx

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
)

// Binder is echo's DefaultBinder, except that a JSON value of the wrong type
// is answered as an invalid field named by its key. Both HTTP shells install
// it, so a handler returns whatever Bind returns. Any other binding failure,
// such as malformed JSON, stays echo's 400 and is answered without a field.
type Binder struct {
	echo.DefaultBinder
}

// Bind binds the request into i.
func (b *Binder) Bind(i any, c echo.Context) error {
	err := b.DefaultBinder.Bind(i, c)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return apperror.FieldInvalid(typeErr.Field, err)
	}
	return err
}

// RequiredParam returns the path parameter name, trimmed. An empty one is
// answered as a missing field called name.
func RequiredParam(c echo.Context, name string) (string, error) {
	return required(c.Param(name), name)
}

// RequiredQuery returns the query parameter name, trimmed. An empty one is
// answered as a missing field called name.
func RequiredQuery(c echo.Context, name string) (string, error) {
	return required(c.QueryParam(name), name)
}

func required(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", apperror.FieldRequired(name)
	}
	return value, nil
}
