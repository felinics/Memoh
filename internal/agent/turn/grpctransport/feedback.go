package grpctransport

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	acpfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

// feedbackStatusPrefix marks a gRPC status message that carries a JSON
// *acpfeedback.Error. Typed errors lose their identity across the process
// boundary; without this envelope the channel process can never render the
// localized ACP guidance (agent not configured, login expired, …) and users
// would see a bare "internal turn operation failed" instead.
const feedbackStatusPrefix = "memoh-acp-feedback:"

// feedbackFromError extracts a transportable feedback error. The sentinel
// table mirrors acpFeedbackFromError in internal/channel/inbound: session
// sentinels compared via errors.Is cannot survive serialization, so they
// are converted to feedback errors before crossing the wire.
func feedbackFromError(err error) *acpfeedback.Error {
	var feedback *acpfeedback.Error
	if errors.As(err, &feedback) {
		return feedback
	}
	switch {
	case errors.Is(err, sessionpkg.ErrACPAgentIDRequired):
		return acpfeedback.New(acpfeedback.CodeAgentNotConfigured, "missing_agent_id", http.StatusBadRequest, "chat.acp.agentNotConfigured", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPUnknownAgent):
		return acpfeedback.New(acpfeedback.CodeAgentNotFound, "unknown_agent", http.StatusBadRequest, "chat.acp.agentNotFound", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPAgentNotEnabled):
		return acpfeedback.New(acpfeedback.CodeAgentNotEnabled, "agent_not_enabled", http.StatusForbidden, "chat.acp.agentNotEnabled", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPAgentNotConfigured):
		return acpfeedback.New(acpfeedback.CodeAgentNotConfigured, "agent_not_configured", http.StatusBadRequest, "chat.acp.agentNotConfigured", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPRuntimeOwnerMissing):
		return acpfeedback.New(acpfeedback.CodeRuntimeOwnerMissing, "missing_runtime_owner", http.StatusForbidden, "chat.acp.runtimeOwnerMissing", err.Error(), nil)
	default:
		return nil
	}
}

// encodeFeedback packs a feedback error into a status message.
func encodeFeedback(feedback *acpfeedback.Error) (string, bool) {
	data, err := json.Marshal(feedback)
	if err != nil {
		return "", false
	}
	return feedbackStatusPrefix + string(data), true
}

// decodeFeedback recovers a feedback error from a status message, returning
// nil when the message does not carry the envelope.
func decodeFeedback(message string) *acpfeedback.Error {
	rest, ok := strings.CutPrefix(message, feedbackStatusPrefix)
	if !ok {
		return nil
	}
	var feedback acpfeedback.Error
	if err := json.Unmarshal([]byte(rest), &feedback); err != nil {
		return nil
	}
	if strings.TrimSpace(feedback.Code) == "" {
		return nil
	}
	return &feedback
}
