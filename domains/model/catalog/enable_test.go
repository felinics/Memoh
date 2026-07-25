package catalog_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/memohai/memoh/domains/model/catalog"
)

func TestAddRequest_EnableIsOptional(t *testing.T) {
	t.Run("absent key leaves Enable nil", func(t *testing.T) {
		var req catalog.AddRequest
		body := `{"model_id":"gpt-4","provider_id":"11111111-1111-1111-1111-111111111111","type":"chat"}`
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		assert.Nil(t, req.Enable, "missing enable key should produce nil pointer (server defaults to true)")
	})

	t.Run("explicit false sets pointer", func(t *testing.T) {
		var req catalog.AddRequest
		body := `{"model_id":"gpt-4","provider_id":"11111111-1111-1111-1111-111111111111","type":"chat","enable":false}`
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		require.NotNil(t, req.Enable)
		assert.False(t, *req.Enable)
	})

	t.Run("explicit true sets pointer", func(t *testing.T) {
		var req catalog.AddRequest
		body := `{"model_id":"gpt-4","provider_id":"11111111-1111-1111-1111-111111111111","type":"chat","enable":true}`
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		require.NotNil(t, req.Enable)
		assert.True(t, *req.Enable)
	})
}

func TestUpdateRequest_EnableIsOptional(t *testing.T) {
	t.Run("absent key leaves Enable nil (preserve current)", func(t *testing.T) {
		var req catalog.UpdateRequest
		body := `{"model_id":"gpt-4","provider_id":"11111111-1111-1111-1111-111111111111","type":"chat"}`
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		assert.Nil(t, req.Enable, "missing enable key on update means preserve current state")
	})

	t.Run("explicit false toggles off", func(t *testing.T) {
		var req catalog.UpdateRequest
		body := `{"model_id":"gpt-4","provider_id":"11111111-1111-1111-1111-111111111111","type":"chat","enable":false}`
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		require.NotNil(t, req.Enable)
		assert.False(t, *req.Enable)
	})
}

func TestResolveEnable(t *testing.T) {
	falseVal := false
	trueVal := true

	t.Run("nil override preserves current", func(t *testing.T) {
		assert.True(t, catalog.ResolveEnable(nil, true), "nil + true → true")
		assert.False(t, catalog.ResolveEnable(nil, false), "nil + false → false")
	})

	t.Run("non-nil override replaces current", func(t *testing.T) {
		assert.False(t, catalog.ResolveEnable(&falseVal, true), "false override beats current=true")
		assert.True(t, catalog.ResolveEnable(&trueVal, false), "true override beats current=false")
	})
}
