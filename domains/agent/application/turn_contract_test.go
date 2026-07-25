package application

import "testing"

// TestParseKindCoversChannelChunkTypes pins parseKind against every chunk
// type the channel inbound mapper consumes today (fixtures from
// internal/channel/inbound/channel_test.go). If a new chunk type appears
// on the wire, add it here and to the channel mapper together.
func TestParseKindCoversChannelChunkTypes(t *testing.T) {
	fixtures := map[string]string{
		"text_delta":            `{"type":"text_delta","delta":"hello"}`,
		"text_start":            `{"type":"text_start"}`,
		"text_end":              `{"type":"text_end"}`,
		"reasoning_delta":       `{"type":"reasoning_delta","delta":"thinking"}`,
		"reasoning_start":       `{"type":"reasoning_start"}`,
		"reasoning_end":         `{"type":"reasoning_end"}`,
		"tool_call_start":       `{"type":"tool_call_start","toolName":"search_web","toolCallId":"tc_1"}`,
		"tool_call_end":         `{"type":"tool_call_end","toolName":"search_web","toolCallId":"tc_1"}`,
		"attachment_delta":      `{"type":"attachment_delta","attachments":[]}`,
		"error":                 `{"type":"error","error":"boom"}`,
		"agent_start":           `{"type":"agent_start"}`,
		"agent_end":             `{"type":"agent_end"}`,
		"agent_abort":           `{"type":"agent_abort"}`,
		"processing_started":    `{"type":"processing_started"}`,
		"processing_completed":  `{"type":"processing_completed"}`,
		"processing_failed":     `{"type":"processing_failed","error":"failed"}`,
		"user_input_request":    `{"type":"user_input_request","toolCallId":"tc_2"}`,
		"tool_approval_request": `{"type":"tool_approval_request","toolCallId":"tc_3","toolName":"exec"}`,
		"reaction_delta":        `{"type":"reaction_delta","reactions":[{"emoji":"👍"}]}`,
		"speech_delta":          `{"type":"speech_delta","speeches":[{"text":"hi"}]}`,
	}
	for want, chunk := range fixtures {
		if got := parseKind([]byte(chunk)); got != want {
			t.Errorf("parseKind(%s) = %q, want %q", chunk, got, want)
		}
	}
	if got := parseKind([]byte(``)); got != "" {
		t.Errorf("parseKind(empty) = %q, want empty", got)
	}
	if got := parseKind([]byte(`not-json`)); got != "" {
		t.Errorf("parseKind(garbage) = %q, want empty", got)
	}
}
