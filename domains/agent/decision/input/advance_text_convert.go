package input

import (
	"encoding/json"
	"fmt"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

func advanceTextResult(handled, invalid bool, req Request) (agentdomain.AdvanceTextResult, error) {
	wire, err := advanceTextRequestFrom(req)
	if err != nil {
		return agentdomain.AdvanceTextResult{}, err
	}
	return agentdomain.AdvanceTextResult{
		Handled: handled,
		Invalid: invalid,
		Request: wire,
	}, nil
}

func advanceTextRequestFrom(req Request) (agentdomain.AdvanceTextRequest, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return agentdomain.AdvanceTextRequest{}, fmt.Errorf("marshal advance text request: %w", err)
	}
	var wire agentdomain.AdvanceTextRequest
	if err := json.Unmarshal(data, &wire); err != nil {
		return agentdomain.AdvanceTextRequest{}, fmt.Errorf("unmarshal advance text request: %w", err)
	}
	return wire, nil
}
