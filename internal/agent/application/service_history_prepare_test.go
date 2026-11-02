package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// expiringHistoryMessageService returns its rows and then ends the request,
// as a schedule's execution budget running out during the load would.
type expiringHistoryMessageService struct {
	*recordingMessageService
	rows   []messagepkg.Message
	expire context.CancelCauseFunc
}

var errTestBudgetExpired = errors.New("test execution budget expired")

func (s *expiringHistoryMessageService) ListHistoryWithinBytes(context.Context, string, messagepkg.HistoryWindow) (messagepkg.HistoryLoad, error) {
	s.expire(errTestBudgetExpired)
	return messagepkg.HistoryLoad{Messages: append([]messagepkg.Message(nil), s.rows...)}, nil
}

func TestPrepareHistoryContextStopsWhenTheRequestEnds(t *testing.T) {
	t.Parallel()

	ctx, expire := context.WithCancelCause(t.Context())
	defer expire(nil)
	messages := &expiringHistoryMessageService{recordingMessageService: &recordingMessageService{}, rows: []messagepkg.Message{persistedHistoryMessage(t, "row-1", "user", "hello")}, expire: expire}
	svc := &Service{messageService: messages, checkpoints: emptyHistoryCheckpoints()}

	_, err := prepareHistoryFor(ctx, svc, ChatRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID}, 100000)
	if !errors.Is(err, errTestBudgetExpired) || !strings.HasPrefix(err.Error(), "decode history rows: ") {
		t.Fatalf("error = %v, want the request's cause named after the step it interrupted", err)
	}
}

// prepareHistoryFor prepares req's history as a chat turn scopes it.
func prepareHistoryFor(ctx context.Context, svc *Service, req ChatRequest, budget int) (preparedHistoryContext, error) {
	return svc.prepareHistoryContext(ctx, req, chatHistoryScope(req), budget)
}
