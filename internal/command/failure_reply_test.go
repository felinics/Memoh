package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

// newFailingCommandHandler registers /probe run, whose handler returns err,
// and records the handler's log lines in the returned buffer.
func newFailingCommandHandler(err error) (*Handler, *bytes.Buffer) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewHandler(log, &fakeRoleResolver{role: "owner"}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	group := newCommandGroup("probe", "probe")
	group.Register(SubCommand{
		Name:    "run",
		Handler: func(CommandContext) (string, error) { return "", err },
	})
	h.registry.RegisterGroup(group)
	return h, &buf
}

func executeProbe(t *testing.T, h *Handler) string {
	t.Helper()
	res, err := h.ExecuteResult(context.Background(), ExecuteInput{BotID: "bot-1", ChannelIdentityID: "ci-1", Text: "/probe run", Locale: "en"})
	if err != nil {
		t.Fatalf("ExecuteResult: %v", err)
	}
	return res.Text
}

// An internal error is answered with the generic copy for the command, its
// text stays out of the reply, and the cause is recorded as an event.
func TestExecuteResultHidesInternalErrorText(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{
		errors.New("upstream returned SECRET-TOKEN"),
		errors.New(`schedule "x" not found: SECRET-TOKEN`),
	} {
		h, logs := newFailingCommandHandler(fmt.Errorf("probe: %w", cause))
		got := executeProbe(t, h)
		if want := "⚠️ Couldn't complete `/probe` right now. Try again in a moment."; got != want {
			t.Errorf("reply = %q, want %q", got, want)
		}
		if !strings.Contains(logs.String(), `"msg":"command failed"`) || !strings.Contains(logs.String(), "SECRET-TOKEN") {
			t.Errorf("cause not recorded: %s", logs.String())
		}
	}
}

// A public error is answered with its code's channel copy and its args.
// A client fault is not this process failing, so nothing is recorded.
func TestExecuteResultRendersPublicErrorByCode(t *testing.T) {
	t.Parallel()
	public := apperror.Wrap(apperror.CodeSettingsReasoningEffortInvalid, errors.New("SECRET-TOKEN"), map[string]string{"effort": "xhigh"})
	h, logs := newFailingCommandHandler(fmt.Errorf("update settings: %w", public))
	got := executeProbe(t, h)
	if want := `Reasoning level "xhigh" is not supported by the selected chat model.`; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	if strings.Contains(logs.String(), "command failed") {
		t.Errorf("client fault recorded: %s", logs.String())
	}
}

// A replyError is the command's answer: its copy, with the list pointer the
// copy carries, and no record.
func TestExecuteResultRendersReplyError(t *testing.T) {
	t.Parallel()
	h, logs := newFailingCommandHandler(newReplyError("cmd.schedule.notFound", map[string]any{"name": `"daily"`, "command": CmdRef("schedule list")}))
	if got, want := executeProbe(t, h), "No schedule named \"daily\". See schedules with `/schedule list`."; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	if strings.Contains(logs.String(), "command failed") {
		t.Errorf("reply error recorded: %s", logs.String())
	}
}

// /model set reports a missing model service with its own copy, not with the
// text of an error.
func TestModelSetWithoutModelServiceRepliesWithCopy(t *testing.T) {
	t.Parallel()
	h := newTestHandler(&fakeRoleResolver{role: "owner"})
	res, err := h.ExecuteResult(context.Background(), ExecuteInput{BotID: "bot-1", ChannelIdentityID: "ci-1", Text: "/model set gpt", Locale: "zh"})
	if err != nil {
		t.Fatalf("ExecuteResult: %v", err)
	}
	var reply *replyError
	_, findErr := h.findModelForSelection(CommandContext{Ctx: context.Background()}, []string{"gpt"})
	if !errors.As(findErr, &reply) || reply.key != "cmd.model.serviceUnavailable" {
		t.Fatalf("findModelForSelection error = %v, want replyError cmd.model.serviceUnavailable", findErr)
	}
	if want := "模型服务暂不可用。"; res.Text != want {
		t.Errorf("reply = %q, want %q", res.Text, want)
	}
}
