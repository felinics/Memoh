package hooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestRuntimeConfigIsolatesInvalidHooksAndActions(t *testing.T) {
	raw := []byte(`{"hooks":[
  {"event":"unknown","actions":[{"type":"tool","tool":"never"}]},
  {"event":"UserMessageReceived","matcher":"[","actions":[{"type":"tool","tool":"never"}]},
  {"event":"UserMessageReceived","actions":[{"type":"tool"},{"type":"tool","tool":"allowed","on_error":"typo"},{"type":"tool","tool":"valid"}]}
 ]}`)
	loaded, err := parseRuntimeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeToolRunner{fn: func(context.Context, string, map[string]any) (any, error) {
		return map[string]any{"decision": "allow"}, nil
	}}
	result, err := NewService(nil, nil).RunConfig(t.Context(), loaded.config, Request{Event: EventUserMessageReceived}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.skippedHooks != 2 || loaded.skippedActions != 2 || result.ActionsRun != 1 || runner.calls[0].name != "valid" {
		t.Fatalf("load=%+v result=%+v calls=%+v", loaded, result, runner.calls)
	}
	if _, err := ParseConfig(raw); err == nil {
		t.Fatal("management parser accepted invalid config")
	}
}

func TestRuntimeConfigRefusesAmbiguousDocuments(t *testing.T) {
	for _, raw := range []string{`{"enabled":true,"enabled":false}`, `{"version":99}`, `{"hooks":"bad"}`, `null`, `{"hooks":[{"event":"UserMessageReceived","actions":[{"type":"command","command":"one","command":"two"}]}]}`, `{"enabled":"false"}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseRuntimeConfig([]byte(raw)); err == nil {
				t.Fatal("ambiguous document accepted")
			}
		})
	}
}

func TestRuntimeRepairPreservesCommandStringsAndPolicies(t *testing.T) {
	raw := []byte("{ // outside comment\n\"enabled\":true,\"hooks\":[{\"event\":\"UserMessageReceived\",\"actions\":[{\"type\":\"command\",\"command\":\"curl https://example.org/a/*b*/ --data \\\"x,}\\\"\",\"on_error\":\"block\",},],},],/* end */}")
	repaired, err := repairJSONFormatting(raw)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := parseRuntimeConfig(repaired)
	if err != nil {
		t.Fatal(err)
	}
	action := loaded.config.Hooks[0].Actions[0]
	if action.Command != `curl https://example.org/a/*b*/ --data "x,}"` || action.OnError != OnErrorBlock {
		t.Fatalf("action altered: %+v", action)
	}
	for _, broken := range []string{`{"hooks":[`, `{"enabled": tru}`, `{"enabled":"unfinished}`, `{"enabled":false /*`, `{"enabled":false} garbage`} {
		if _, err := repairJSONFormatting([]byte(broken)); err == nil {
			t.Fatalf("unsafe repair accepted: %s", broken)
		}
	}
}

func TestRunSkipsBrokenConfigButPreservesValidDeny(t *testing.T) {
	for _, raw := range []string{`{"hooks": [`, `{"hooks":[{"event":"invalid"}]}`} {
		server := &hookBridgeTestServer{files: map[string][]byte{DefaultConfigPath: []byte(raw)}}
		service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})
		ctx := WithLoadState(t.Context())
		res, err := service.Run(ctx, Request{BotID: "b", Event: EventUserMessageReceived}, nil)
		if err != nil || res.Decision != DecisionAllow || LoadNotice(ctx) == "" {
			t.Fatalf("result=%+v error=%v notice=%q", res, err, LoadNotice(ctx))
		}
		if !bytes.Equal(server.files[DefaultConfigPath], []byte(raw)) {
			t.Fatal("broken original overwritten")
		}
	}
	server := &hookBridgeTestServer{files: map[string][]byte{DefaultConfigPath: []byte(`{"hooks":[{"event":"UserMessageReceived","actions":[{"type":"tool","tool":"deny"}]}]}`)}}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})
	runner := &fakeToolRunner{fn: func(context.Context, string, map[string]any) (any, error) {
		return map[string]any{"decision": "deny", "reason": "policy"}, nil
	}}
	if _, err := service.Run(t.Context(), Request{BotID: "b", Event: EventUserMessageReceived}, runner); !errors.Is(err, ErrDenied) {
		t.Fatalf("valid deny bypassed: %v", err)
	}
}

func TestRunMissingConfigDoesNotRequireWrite(t *testing.T) {
	server := &hookBridgeTestServer{files: map[string][]byte{}}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})
	ctx := WithLoadState(t.Context())
	res, err := service.Run(ctx, Request{BotID: "b", Event: EventBeforeModelCall}, nil)
	if err != nil || res.Decision != DecisionAllow || !strings.Contains(LoadNotice(ctx), "does not exist") {
		t.Fatalf("res=%+v err=%v notice=%q", res, err, LoadNotice(ctx))
	}
	if len(server.files) != 0 {
		t.Fatal("runtime read unexpectedly wrote missing config")
	}
}

type repairMemoryClient struct {
	files       map[string][]byte
	failPath    string
	editOnWrite bool
}

func (c *repairMemoryClient) ReadRaw(_ context.Context, p string) (io.ReadCloser, error) {
	b, ok := c.files[p]
	if !ok {
		return nil, bridge.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (c *repairMemoryClient) WriteFile(_ context.Context, p string, b []byte) error {
	if p == c.failPath {
		return errors.New("write denied")
	}
	c.files[p] = bytes.Clone(b)
	if c.editOnWrite && p != DefaultConfigPath {
		c.files[DefaultConfigPath] = []byte(`{"enabled":false}`)
	}
	return nil
}

func (c *repairMemoryClient) Rename(_ context.Context, a, b string) error {
	c.files[b] = c.files[a]
	delete(c.files, a)
	return nil
}

func (c *repairMemoryClient) DeleteFile(_ context.Context, p string, _ bool) error {
	delete(c.files, p)
	return nil
}

func TestRepairSavePreservesOriginalOnFailureOrConcurrentEdit(t *testing.T) {
	original := []byte(`{"hooks":[],}`)
	repaired := []byte(`{"hooks":[] }`)
	for _, scenario := range []string{"saved", "backup-failed", "concurrent-edit"} {
		t.Run(scenario, func(t *testing.T) {
			client := &repairMemoryClient{files: map[string][]byte{DefaultConfigPath: bytes.Clone(original)}}
			if scenario == "backup-failed" {
				client.failPath = DefaultConfigPath + ".bak"
			}
			if scenario == "concurrent-edit" {
				client.editOnWrite = true
			}
			err := saveRepairedConfig(t.Context(), client, original, repaired)
			switch scenario {
			case "saved":
				if err != nil || !bytes.Equal(client.files[DefaultConfigPath], repaired) || !bytes.Equal(client.files[DefaultConfigPath+".bak"], original) {
					t.Fatalf("err=%v files=%v", err, client.files)
				}
			case "backup-failed":
				if err == nil || !bytes.Equal(client.files[DefaultConfigPath], original) {
					t.Fatal("original lost on failed backup")
				}
			case "concurrent-edit":
				if err == nil || string(client.files[DefaultConfigPath]) != `{"enabled":false}` {
					t.Fatal("concurrent user edit overwritten")
				}
			}
			for p := range client.files {
				if strings.Contains(p, ".repair-") {
					t.Fatal("staged repair leaked")
				}
			}
		})
	}
}

// Keep the test transport at the same behavior boundary as runtime loading.
func TestRunUsesRepairInMemoryWhenPersistenceUnavailable(t *testing.T) {
	raw := []byte(`{"hooks":[{"event":"BeforeModelCall","actions":[{"type":"tool","tool":"allowed",}],}],}`)
	server := &hookBridgeTestServer{files: map[string][]byte{DefaultConfigPath: raw}}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})
	ctx := WithLoadState(t.Context())
	runner := &fakeToolRunner{}
	res, err := service.Run(ctx, Request{BotID: "b", Event: EventBeforeModelCall}, runner)
	if err != nil || res.ActionsRun != 1 || !strings.Contains(LoadNotice(ctx), "could not be saved") {
		t.Fatalf("res=%+v err=%v notice=%s", res, err, LoadNotice(ctx))
	}
	if !bytes.Equal(server.files[DefaultConfigPath], raw) {
		t.Fatal("failed rename overwrote original")
	}
}

func TestConcurrentRepairDoesNotBlockConversation(t *testing.T) {
	raw := []byte(`{"hooks":[{"event":"BeforeModelCall","actions":[{"type":"tool","tool":"allowed",}],}],}`)
	server := &hookBridgeTestServer{files: map[string][]byte{DefaultConfigPath: raw}}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})
	service.repairMu.Lock()
	defer service.repairMu.Unlock()
	ctx := WithLoadState(t.Context())
	res, err := service.Run(ctx, Request{BotID: "b", Event: EventBeforeModelCall}, &fakeToolRunner{})
	if err != nil || res.ActionsRun != 1 || !strings.Contains(LoadNotice(ctx), "could not be saved") || !bytes.Equal(server.files[DefaultConfigPath], raw) {
		t.Fatalf("res=%+v err=%v notice=%s", res, err, LoadNotice(ctx))
	}
}
