package inbound

import (
	"encoding/json"
	"testing"
)

func TestAwaitsUserDecision(t *testing.T) {
	t.Parallel()

	cases := []struct {
		chunk string
		want  bool
	}{
		{`{"type":"tool_approval_request","approvalId":"a1"}`, true},
		{`{"type":"tool_approval_request","status":"pending"}`, true},
		{`{"type":"user_input_request","status":"pending"}`, true},
		{`{"type":"user_input_request","status":"submitted"}`, false},
		{`{"type":"tool_approval_request","status":"approved"}`, false},
		{`{"type":"tool_call_start","toolName":"read"}`, false},
		{`{"type":"text_delta","delta":"hi"}`, false},
		{`not json`, false},
	}
	for _, tc := range cases {
		if got := awaitsUserDecision(json.RawMessage(tc.chunk)); got != tc.want {
			t.Errorf("awaitsUserDecision(%s) = %v, want %v", tc.chunk, got, tc.want)
		}
	}
}
