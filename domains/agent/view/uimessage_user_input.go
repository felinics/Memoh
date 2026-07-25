package view

import (
	"strings"

	userinput "github.com/memohai/memoh/domains/agent/decision/input"
)

func uiUserInputFromPayload(userInputID string, shortID int, status string, payload any, canRespond bool) *UIUserInput {
	userInputID = strings.TrimSpace(userInputID)
	if userInputID == "" {
		return nil
	}
	if status = strings.TrimSpace(status); status == "" {
		status = userinput.StatusPending
	}
	return &UIUserInput{
		UserInputID: userInputID,
		ShortID:     shortID,
		Status:      status,
		Questions:   userinput.PayloadFromStored(payload).Questions,
		CanRespond:  canRespond,
	}
}
