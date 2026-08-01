package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNormalizeChatID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: " -1004402809405 ", want: "-1004402809405", ok: true},
		{input: "00042", want: "42", ok: true},
		{input: "", ok: false},
		{input: "0", ok: false},
		{input: "@group", ok: false},
	}
	for _, tt := range tests {
		got, err := normalizeChatID(tt.input)
		if (err == nil) != tt.ok {
			t.Fatalf("normalizeChatID(%q) error = %v, want ok=%v", tt.input, err, tt.ok)
		}
		if got != tt.want {
			t.Fatalf("normalizeChatID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeMessageIDs(t *testing.T) {
	t.Parallel()

	got, err := normalizeMessageIDs([]string{" 4689,4688 ", "4688", "0002"})
	if err != nil {
		t.Fatalf("normalizeMessageIDs() error = %v", err)
	}
	want := []string{"2", "4688", "4689"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalizeMessageIDs() = %v, want %v", got, want)
	}

	for _, invalid := range [][]string{nil, {"0"}, {"-1"}, {"abc"}} {
		if _, err := normalizeMessageIDs(invalid); err == nil {
			t.Fatalf("normalizeMessageIDs(%v) unexpectedly succeeded", invalid)
		}
	}
}

func TestBuildPreviewSQLUsesExactRouteAndMessageIDs(t *testing.T) {
	t.Parallel()

	sql := buildPreviewSQL("-1004402809405", []string{"4688", "4690"})
	for _, fragment := range []string{
		"route.channel_type = 'telegram'",
		"route.external_conversation_id = '-1004402809405'",
		"('4688'), ('4690')",
		"history.source_message_id = requested.message_id",
		"event.external_message_id = requested.message_id",
		"memory.source_message_ids ? history.id::text",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("preview SQL missing %q", fragment)
		}
	}
}

func TestBuildApplySQLScopesEveryDeleteThroughTempTargets(t *testing.T) {
	t.Parallel()

	sql := buildApplySQL("-1004402809405", []string{"4688"})
	for _, fragment := range []string{
		"CREATE TEMP TABLE memoh_tg_target_sessions",
		"route.external_conversation_id = '-1004402809405'",
		"CREATE TEMP TABLE memoh_tg_locked_sessions",
		"UPDATE bot_sessions session",
		"DELETE FROM bot_history_message_compacts compact",
		"USING memoh_tg_target_compactions target",
		"DELETE FROM bot_history_messages history",
		"USING memoh_tg_target_history target",
		"DELETE FROM bot_session_events event",
		"USING memoh_tg_target_events target",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("apply SQL missing %q", fragment)
		}
	}
}

func TestValidateApplyPreview(t *testing.T) {
	t.Parallel()

	cfg := config{chatID: "-100", messageIDs: []string{"42"}}
	base := preview{
		RouteCount: 1,
		Matches: []matchPreview{{
			BotName:      "bot",
			MessageID:    "42",
			HistoryCount: 1,
			EventCount:   1,
		}},
	}
	if err := validateApplyPreview(cfg, base); err != nil {
		t.Fatalf("validateApplyPreview(base) error = %v", err)
	}

	withReply := base
	withReply.Matches = append([]matchPreview(nil), base.Matches...)
	withReply.Matches[0].EventReplies = 1
	if err := validateApplyPreview(cfg, withReply); err == nil {
		t.Fatal("validateApplyPreview(withReply) unexpectedly succeeded")
	}
	cfg.allowReferences = true
	if err := validateApplyPreview(cfg, withReply); err != nil {
		t.Fatalf("validateApplyPreview(withReply, allow) error = %v", err)
	}

	withMemory := base
	withMemory.Matches = append([]matchPreview(nil), base.Matches...)
	withMemory.Matches[0].MemoryNodeCount = 1
	if err := validateApplyPreview(cfg, withMemory); err == nil {
		t.Fatal("validateApplyPreview(withMemory) unexpectedly succeeded")
	}
}

func TestPreviewJSONShape(t *testing.T) {
	t.Parallel()

	raw := `{
		"route_count": 1,
		"routes": [{"bot_name": "bot", "route_id": "route"}],
		"matches": [{
			"bot_name": "bot",
			"route_id": "route",
			"session_id": "session",
			"message_id": "42",
			"sender_name": "user",
			"received_at_ms": 1000,
			"history_count": 1,
			"event_count": 1,
			"history_reply_count": 0,
			"event_reply_count": 0,
			"compaction_count": 0,
			"memory_node_count": 0
		}],
		"missing": []
	}`
	var got preview
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.RouteCount != 1 || len(got.Matches) != 1 || got.Matches[0].MessageID != "42" {
		t.Fatalf("decoded preview = %#v", got)
	}
}

func TestParseConfigDefaultsToDryRunAndRestart(t *testing.T) {
	t.Parallel()

	cfg, err := parseConfig(
		[]string{"--chat-id", "-100", "--message-id", "42"},
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.apply {
		t.Fatal("apply defaulted to true")
	}
	if !cfg.restart || !cfg.useSudo {
		t.Fatalf("unexpected defaults: restart=%v sudo=%v", cfg.restart, cfg.useSudo)
	}
	if cfg.timeout != 10*time.Minute {
		t.Fatalf("timeout = %v, want 10m", cfg.timeout)
	}
}

func TestDockerOutputKeepsStderrOutOfJSON(t *testing.T) {
	t.Parallel()

	var warnings bytes.Buffer
	runner := &commandRunner{
		stderr: &warnings,
		dockerCommandFn: func(ctx context.Context, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' '{"route_count":1}'; printf '%s' 'NOTICE: maintenance' >&2`)
		},
	}
	output, err := runner.dockerOutput(context.Background(), "exec", "postgres")
	if err != nil {
		t.Fatalf("dockerOutput() error = %v", err)
	}
	if got := string(output); got != `{"route_count":1}` {
		t.Fatalf("stdout = %q, want clean JSON", got)
	}
	if !strings.Contains(warnings.String(), "NOTICE: maintenance") {
		t.Fatalf("stderr = %q, want psql notice", warnings.String())
	}
}

func TestHelpExitsSuccessfully(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	if code := run([]string{"--help"}, strings.NewReader(""), io.Discard, &stderr); code != 0 {
		t.Fatalf("run(--help) = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "memoh-telegram-sync") {
		t.Fatalf("help output missing command name: %s", stderr.String())
	}
}
