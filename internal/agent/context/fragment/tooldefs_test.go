package contextfrag

import (
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestToolDefAccountingForMeasuresSerializedDefinition(t *testing.T) {
	t.Parallel()

	tool := sdk.Tool{
		Name:        "send_message",
		Description: "Send a message to the current conversation.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
	}
	got := ToolDefAccountingFor("native", tool)

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	want := ToolDefAccounting{
		Provider:      "native",
		Name:          "send_message",
		Bytes:         len(data),
		TokenEstimate: TokensFromBytes(len(data)),
	}
	if got != want {
		t.Fatalf("ToolDefAccountingFor = %+v, want %+v", got, want)
	}
	if got.TokenEstimate == 0 {
		t.Fatal("tool definition estimate must be nonzero")
	}
}
