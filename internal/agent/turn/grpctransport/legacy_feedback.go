package grpctransport

import (
	"encoding/json"
	"strings"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/rpc"
)

// legacyFeedbackPrefix marks the FailedPrecondition status message a server
// from before the error envelope used for External Agent feedback: the prefix
// followed by a JSON object with the code and its args.
//
// It is decoded until no deployed server sends it.
const legacyFeedbackPrefix = "memoh-acp-feedback:"

// decodeLegacyFeedback restores the apperror of a legacy feedback status, or
// returns nil when the message is not one or its code is not in the catalog.
func decodeLegacyFeedback(received error, message string) error {
	rest, ok := strings.CutPrefix(message, legacyFeedbackPrefix)
	if !ok {
		return nil
	}
	var feedback struct {
		Code string            `json:"code"`
		Args map[string]string `json:"args"`
	}
	if err := json.Unmarshal([]byte(rest), &feedback); err != nil {
		return nil
	}
	code := apperror.Code(strings.TrimSpace(feedback.Code))
	if _, ok := apperror.Lookup(code); !ok {
		return nil
	}
	return rpc.Restored(apperror.New(code, feedback.Args), received)
}
