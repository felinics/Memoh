package command

import (
	"context"
	"strings"
	"testing"
)

func TestHelpUsesCommandUILocale(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)

	res, err := h.ExecuteResult(context.Background(), ExecuteInput{Text: "/help", Locale: "zh"})
	if err != nil {
		t.Fatalf("ExecuteResult /help: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	for _, want := range []string{"可用命令", "/model — 切换对话模型", "/settings — 查看和修改设置"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("zh global help missing %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "Available commands") || strings.Contains(res.Text, "Manage bot models") {
		t.Fatalf("zh global help leaked English help chrome:\n%s", res.Text)
	}
}

func TestHelpUsesCommandTarget(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)

	res, err := h.ExecuteResult(context.Background(), ExecuteInput{
		Text:          "/help@snowluocat_bot",
		CommandTarget: "snowluocat_bot",
		Locale:        "en",
	})
	if err != nil {
		t.Fatalf("ExecuteResult addressed /help: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	for _, want := range []string{
		"/help — show this help",
		"/link — Link this account",
		"for example `/help@snowluocat_bot`",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("addressed help missing %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "group's actions") || strings.Contains(res.Text, "<group>") {
		t.Fatalf("addressed help leaked internal group terminology:\n%s", res.Text)
	}
	if count := strings.Count(res.Text, "snowluocat_bot"); count != 1 {
		t.Fatalf("addressed help repeats bot username %d times, want once:\n%s", count, res.Text)
	}
}

func TestGroupAndActionHelpUseCommandUILocale(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)

	group, err := h.ExecuteResult(context.Background(), ExecuteInput{Text: "/help model", Locale: "zh"})
	if err != nil {
		t.Fatalf("ExecuteResult /help model: %v", err)
	}
	if group == nil || group.Interactive == nil || group.Interactive.Choices == nil {
		t.Fatalf("expected interactive group help, got %+v", group)
	}
	// Action tokens stay literal (a command token must never be translated:
	// showing "列出" but rejecting a typed "列出" is exactly the bug this guards).
	// Only the trailing summary is localized.
	for _, want := range []string{"**/model** — 切换对话模型", "`/model list` — 列出可用对话模型", "`/model set-heartbeat` — 设置心跳模型", "选择操作："} {
		if !strings.Contains(group.Interactive.Choices.Title, want) {
			t.Fatalf("zh group help missing %q:\n%s", want, group.Interactive.Choices.Title)
		}
	}
	if group.Interactive.Choices.Columns != 1 {
		t.Fatalf("zh group help buttons should be one per row, got columns=%d", group.Interactive.Choices.Columns)
	}
	labels := map[string]bool{}
	for _, choice := range group.Interactive.Choices.Choices {
		labels[choice.Label] = true
		if strings.Contains(choice.Label, "🔒") || strings.Contains(choice.Label, "所有者") {
			t.Fatalf("zh group help leaked permission label: %#v", group.Interactive.Choices.Choices)
		}
	}
	// Buttons carry the literal canonical token so the visible action matches
	// what re-dispatches and what a user would type.
	if !labels["list"] || !labels["set-heartbeat"] {
		t.Fatalf("group help buttons should carry literal action tokens, got %#v", group.Interactive.Choices.Choices)
	}

	action, err := h.ExecuteResult(context.Background(), ExecuteInput{Text: "/help model current", Locale: "zh"})
	if err != nil {
		t.Fatalf("ExecuteResult /help model current: %v", err)
	}
	for _, want := range []string{"说明： 查看当前对话模型和心跳模型", "用法：", "查看同组操作"} {
		if action == nil || !strings.Contains(action.Text, want) {
			t.Fatalf("zh action help missing %q:\n%v", want, action)
		}
	}
}
