package grpctransport

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

// feedbackStatusPrefix marks a gRPC status message that carries a JSON
// *agentfeedback.Error. Typed errors lose their identity across the process
// boundary; without this envelope the channel process can never render the
// localized ACP guidance (agent not configured, login expired, …) and users
// would see a bare "internal turn operation failed" instead.
const feedbackStatusPrefix = "memoh-acp-feedback:"

// feedbackFromError extracts a transportable feedback error. The sentinel
// table mirrors acpFeedbackFromError in internal/channel/inbound: session
// sentinels compared via errors.Is cannot survive serialization, so they
// are converted to feedback errors before crossing the wire.
func feedbackFromError(err error) *agentfeedback.Error {
	var feedback *agentfeedback.Error
	if errors.As(err, &feedback) {
		return feedback
	}
	switch {
	case errors.Is(err, sessionpkg.ErrACPAgentIDRequired):
		return agentfeedback.New(agentfeedback.CodeAgentNotConfigured, "missing_agent_id", http.StatusBadRequest, "chat.externalAgent.agentNotConfigured", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPUnknownAgent):
		return agentfeedback.New(agentfeedback.CodeAgentNotFound, "unknown_agent", http.StatusBadRequest, "chat.externalAgent.agentNotFound", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPAgentNotEnabled):
		return agentfeedback.New(agentfeedback.CodeAgentNotEnabled, "agent_not_enabled", http.StatusForbidden, "chat.externalAgent.agentNotEnabled", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPAgentNotConfigured):
		return agentfeedback.New(agentfeedback.CodeAgentNotConfigured, "agent_not_configured", http.StatusBadRequest, "chat.externalAgent.agentNotConfigured", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPRuntimeOwnerMissing):
		return agentfeedback.New(agentfeedback.CodeRuntimeOwnerMissing, "missing_runtime_owner", http.StatusForbidden, "chat.externalAgent.runtimeOwnerMissing", err.Error(), nil)
	default:
		return nil
	}
}

// encodeFeedback packs a feedback error into a status message.
func encodeFeedback(feedback *agentfeedback.Error) (string, bool) {
	data, err := json.Marshal(feedback)
	if err != nil {
		return "", false
	}
	return feedbackStatusPrefix + string(data), true
}

// decodeFeedback recovers a feedback error from a status message, returning
// nil when the message does not carry the envelope.
func decodeFeedback(message string) *agentfeedback.Error {
	rest, ok := strings.CutPrefix(message, feedbackStatusPrefix)
	if !ok {
		return nil
	}
	var feedback agentfeedback.Error
	if err := json.Unmarshal([]byte(rest), &feedback); err != nil {
		return nil
	}
	if strings.TrimSpace(feedback.Code) == "" {
		return nil
	}
	return &feedback
}
