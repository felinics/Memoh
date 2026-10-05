package claudecode

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func feedLines(t *testing.T, r *turnRunner, lines ...string) {
	t.Helper()
	for _, line := range lines {
		feed(t, r, line)
	}
}

func feedFixture(t *testing.T, r *turnRunner, path string) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // fixed testdata fixture path
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(nil, maxLineBytes)
	for scanner.Scan() {
		feed(t, r, scanner.Text())
	}
}

// The fixtures are Claude Code CLI 2.1.288 stream-json output for one native
// turn of two API requests, recorded against a local fake Anthropic endpoint.
// The CLI keys modelUsage by the requested model while each assistant message
// carries the dated model the API answered with.
func TestClaudeRecordedTurnUsageAndContext(t *testing.T) {
	wantUsage := sdk.Usage{
		InputTokens: 3080, OutputTokens: 35, TotalTokens: 3115, CachedInputTokens: 2500,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 40, CacheReadTokens: 2500, CacheWriteTokens: 540},
	}
	for _, tc := range []struct {
		fixture string
		window  int
	}{
		{"testdata/usage_two_requests.ndjson", 200000},
		{"testdata/usage_model_alias.ndjson", 200000},
		{"testdata/usage_model_1m.ndjson", 1000000},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			r := newTestRunner(&recordingSink{})
			feedFixture(t, r, tc.fixture)
			result, err := r.buildResult("")
			r.close()
			if err != nil {
				t.Fatal(err)
			}
			if result.Usage == nil || *result.Usage != wantUsage {
				t.Errorf("usage = %+v, want %+v", result.Usage, wantUsage)
			}
			want := external.ContextUsage{UsedTokens: 30 + 1500 + 40 + 15, WindowTokens: tc.window, Source: contextSourceRequest}
			if result.Context == nil || *result.Context != want {
				t.Errorf("context = %+v, want %+v", result.Context, want)
			}
		})
	}
}

const (
	claudeInit          = `{"type":"system","subtype":"init","session_id":"s","model":"claude-opus-5-5","claude_code_version":"2.1.269"}`
	claudeStartM1       = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"message_start","message":{"id":"m1","model":"claude-opus-5-5-20261001","usage":{"input_tokens":5,"cache_read_input_tokens":90000,"cache_creation_input_tokens":5000,"output_tokens":1}}}}`
	claudeAssistantM1   = `{"type":"assistant","parent_tool_use_id":null,"message":{"id":"m1","model":"claude-opus-5-5-20261001","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":5,"cache_read_input_tokens":90000,"cache_creation_input_tokens":5000,"output_tokens":1}}}`
	claudeDeltaM1       = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":40}}}`
	claudeResultR1      = `{"type":"result","subtype":"success","uuid":"r1","is_error":false,"result":"a","usage":{"input_tokens":5,"cache_read_input_tokens":90000,"cache_creation_input_tokens":5000,"output_tokens":40},"modelUsage":{"claude-opus-5-5":{"contextWindow":1000000}}}`
	claudeStartM2Cached = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"message_start","message":{"id":"m2","model":"claude-opus-5-5-20261001","usage":{"input_tokens":0,"cache_read_input_tokens":95300,"cache_creation_input_tokens":0,"output_tokens":1}}}}`
	claudeDeltaM2       = `{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":30}}}`
	claudeSubagent      = `{"type":"assistant","parent_tool_use_id":"toolu_task","message":{"id":"m3","model":"claude-haiku-5","content":[{"type":"text","text":"sub"}],"usage":{"input_tokens":3,"cache_read_input_tokens":400000,"cache_creation_input_tokens":0,"output_tokens":9}}}`
	claudeSubagentStart = `{"type":"stream_event","parent_tool_use_id":"toolu_task","event":{"type":"message_start","message":{"id":"m3","model":"claude-haiku-5","usage":{"input_tokens":3,"cache_read_input_tokens":400000,"cache_creation_input_tokens":0,"output_tokens":1}}}}`
	claudeAPIError      = `{"type":"assistant","error":"rate_limit","message":{"id":"m4","model":"<synthetic>","content":[{"type":"text","text":"API Error"}],"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}`
	claudeResultR2      = `{"type":"result","subtype":"success","uuid":"r2","is_error":false,"result":"b","usage":{"input_tokens":3,"cache_read_input_tokens":495300,"cache_creation_input_tokens":0,"output_tokens":39},"modelUsage":{"claude-opus-5-5":{"contextWindow":1000000},"claude-haiku-5":{"contextWindow":200000}}}`
)

func TestClaudeContextFollowsLatestTopLevelRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  *external.ContextUsage
		usage *sdk.Usage
	}{
		{
			// A fully cached request has zero raw input and is still a request.
			name: "cache-only request after steering, subagent and API error",
			lines: []string{
				claudeInit, claudeStartM1, claudeAssistantM1, claudeDeltaM1, claudeResultR1,
				claudeInit, claudeStartM2Cached, claudeSubagentStart, claudeSubagent, claudeAPIError, claudeDeltaM2, claudeResultR2,
				claudeResultR2,
			},
			want: &external.ContextUsage{UsedTokens: 95300 + 30, WindowTokens: 1000000, Source: contextSourceRequest},
			usage: &sdk.Usage{
				InputTokens: 95005 + 495303, OutputTokens: 79, TotalTokens: 95005 + 495303 + 79, CachedInputTokens: 585300,
				InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 8, CacheReadTokens: 585300, CacheWriteTokens: 5000},
			},
		},
		{
			name:  "latest request without its final delta",
			lines: []string{claudeInit, claudeStartM1, claudeAssistantM1, claudeDeltaM1, claudeResultR1, claudeInit, claudeStartM2Cached},
		},
		{
			name: "compaction after the latest request",
			lines: []string{
				claudeInit, claudeStartM1, claudeAssistantM1, claudeDeltaM1,
				`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":95045}}`,
				claudeResultR1,
			},
		},
		{
			name: "no request",
			lines: []string{
				claudeInit,
				`{"type":"result","subtype":"success","uuid":"r1","is_error":false,"result":"Total cost: $0.00","usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0},"modelUsage":{}}`,
			},
		},
		{
			name: "window unknown without a matching model",
			lines: []string{
				claudeInit, claudeStartM1, claudeAssistantM1, claudeDeltaM1,
				`{"type":"result","subtype":"success","uuid":"r1","is_error":false,"result":"a","usage":{"input_tokens":5,"cache_read_input_tokens":90000,"cache_creation_input_tokens":5000,"output_tokens":40},"modelUsage":{"claude-opus-5-5[1m]":{"contextWindow":1000000}}}`,
			},
			want: &external.ContextUsage{UsedTokens: 95045, Source: contextSourceRequest},
		},
		{
			name: "window unknown after a model fallback",
			lines: []string{
				claudeInit, claudeStartM1, claudeAssistantM1, claudeDeltaM1,
				strings.ReplaceAll(claudeStartM2Cached, "claude-opus-5-5-20261001", "claude-sonnet-5-20261001"),
				claudeDeltaM2,
				`{"type":"result","subtype":"success","uuid":"r1","is_error":false,"result":"a","usage":{"input_tokens":5,"cache_read_input_tokens":185300,"cache_creation_input_tokens":5000,"output_tokens":70},"modelUsage":{"claude-opus-5-5":{"contextWindow":1000000},"claude-sonnet-5":{"contextWindow":200000}}}`,
			},
			want: &external.ContextUsage{UsedTokens: 95330, Source: contextSourceRequest},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRunner(&recordingSink{})
			defer r.close()
			feedLines(t, r, tc.lines...)
			result, err := r.buildResult("s")
			if err != nil {
				t.Fatal(err)
			}
			if (result.Context == nil) != (tc.want == nil) || (tc.want != nil && *result.Context != *tc.want) {
				t.Errorf("context = %+v, want %+v", result.Context, tc.want)
			}
			if tc.usage != nil && (result.Usage == nil || *result.Usage != *tc.usage) {
				t.Errorf("usage = %+v, want %+v", result.Usage, tc.usage)
			}
		})
	}
}
