package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
)

func TestTerminalOutputUsesPromptToolOutputLimit(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("terminal output ", 300) + "\nTAIL"
	manager := &terminalManager{
		limit: ToolOutputLimit{MaxBytes: 512, MaxLines: 80},
		terminals: map[string]*terminal{
			"term-1": {
				output: large,
				done:   make(chan struct{}),
			},
		},
	}

	resp, err := manager.TerminalOutput(context.Background(), acp.TerminalOutputRequest{TerminalId: "term-1"})
	if err != nil {
		t.Fatalf("TerminalOutput returned error: %v", err)
	}
	if !resp.Truncated {
		t.Fatal("TerminalOutput Truncated = false, want true")
	}
	if len(resp.Output) >= len(large) {
		t.Fatalf("terminal output was not limited")
	}
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(resp.Output, want) {
			t.Fatalf("terminal output missing %q:\n%s", want, resp.Output)
		}
	}
}

func TestTerminalAppendOutputPreservesHeadTail(t *testing.T) {
	t.Parallel()

	term := &terminal{
		outputLimit: ToolOutputLimit{MaxBytes: 512, MaxLines: 80},
		done:        make(chan struct{}),
	}
	term.appendOutput("HEAD\n")
	term.appendOutput(strings.Repeat("terminal output ", 300))
	term.appendOutput("\nTAIL")

	output, truncated, _ := term.snapshot()
	if !truncated {
		t.Fatal("terminal truncated = false, want true")
	}
	if len(output) > 512 {
		t.Fatalf("terminal output bytes = %d, want <= 512", len(output))
	}
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(output, want) {
			t.Fatalf("terminal output missing %q:\n%s", want, output)
		}
	}
}

func TestTerminalAppendOutputHonorsSmallByteLimit(t *testing.T) {
	t.Parallel()

	term := &terminal{
		outputLimit: ToolOutputLimit{MaxBytes: 192, MaxLines: 80},
		done:        make(chan struct{}),
	}
	term.appendOutput("HEAD\n")
	term.appendOutput(strings.Repeat("terminal output ", 300))
	term.appendOutput("\nTAIL")

	output, truncated, _ := term.snapshot()
	if !truncated {
		t.Fatal("terminal truncated = false, want true")
	}
	if len(output) > 192 {
		t.Fatalf("terminal output bytes = %d, want <= 192", len(output))
	}
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(output, want) {
			t.Fatalf("terminal output missing %q:\n%s", want, output)
		}
	}
}

func TestTerminalAppendOutputHonorsTinyByteLimits(t *testing.T) {
	t.Parallel()

	for _, maxBytes := range []int{1, 8, 32} {
		t.Run(fmt.Sprintf("%d bytes", maxBytes), func(t *testing.T) {
			t.Parallel()

			term := &terminal{
				outputLimit: ToolOutputLimit{MaxBytes: maxBytes, MaxLines: 80},
				done:        make(chan struct{}),
			}
			term.appendOutput("HEAD\n")
			term.appendOutput(strings.Repeat("terminal output ", 300))
			term.appendOutput("\nTAIL")

			output, truncated, _ := term.snapshot()
			if !truncated {
				t.Fatal("terminal truncated = false, want true")
			}
			if len(output) > maxBytes {
				t.Fatalf("terminal output bytes = %d, want <= %d", len(output), maxBytes)
			}
			if !utf8.ValidString(output) {
				t.Fatalf("terminal output is not valid UTF-8: %q", output)
			}
		})
	}
}

func TestCreateTerminalHonorsOutputByteLimitRequest(t *testing.T) {
	t.Parallel()

	for _, maxBytes := range []int{1, 8, 192} {
		t.Run(fmt.Sprintf("%d bytes", maxBytes), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			manager := newTerminalManager(context.Background(), newTestBridgeClient(t, root), "/data", "/data", 5, nil, true, nil, nil)
			term, err := manager.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
				Command:         "printf",
				Args:            []string{"HEAD" + strings.Repeat("x", 5000) + "TAIL"},
				OutputByteLimit: &maxBytes,
			}, nil, terminalRuntimeScope{})
			if err != nil {
				t.Fatalf("CreateTerminal returned error: %v", err)
			}
			if _, err := manager.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
				t.Fatalf("WaitForTerminalExit returned error: %v", err)
			}
			resp, err := manager.TerminalOutput(context.Background(), acp.TerminalOutputRequest{TerminalId: term.TerminalId})
			if err != nil {
				t.Fatalf("TerminalOutput returned error: %v", err)
			}
			if !resp.Truncated {
				t.Fatal("TerminalOutput Truncated = false, want true")
			}
			if len(resp.Output) > maxBytes {
				t.Fatalf("terminal output bytes = %d, want <= %d", len(resp.Output), maxBytes)
			}
			if !utf8.ValidString(resp.Output) {
				t.Fatalf("terminal output is not valid UTF-8: %q", resp.Output)
			}
			if maxBytes >= 192 {
				for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
					if !strings.Contains(resp.Output, want) {
						t.Fatalf("terminal output missing %q:\n%s", want, resp.Output)
					}
				}
			}
		})
	}
}

func TestCreateTerminalStopsWhenOwningRunIsCanceled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runCtx, cancelRun := context.WithCancel(context.Background())
	manager := newTerminalManager(context.Background(), newTestBridgeClient(t, root), "/data", "/data", 30, nil, true, nil, nil)
	term, err := manager.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "sleep",
		Args:    []string{"30"},
	}, nil, terminalRuntimeScope{runContext: runCtx})
	if err != nil {
		cancelRun()
		t.Fatalf("CreateTerminal() error = %v", err)
	}
	cancelRun()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	if _, err := manager.WaitForTerminalExit(waitCtx, acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
		t.Fatalf("terminal did not stop with its owning run: %v", err)
	}
}

func TestTerminalEndEventUsesPromptToolOutputLimit(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("terminal output ", 300) + "\nTAIL"
	limit := ToolOutputLimit{MaxBytes: 512, MaxLines: 80}
	collector := newEventCollector(limit)
	emitter := &toolEventEmitter{}
	emitter.setPromptState(collector, nil, limit)
	manager := &terminalManager{
		limit:  limit,
		events: emitter,
	}
	term := &terminal{
		id:     "terminal-1",
		input:  map[string]any{"command": "test"},
		output: large,
		done:   make(chan struct{}),
	}

	manager.emitTerminalEnd(term)
	result := collector.result()
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	output, ok := result.Events[0].Result.(map[string]any)
	if !ok {
		t.Fatalf("event result = %#v, want map", result.Events[0].Result)
	}
	stdout, ok := output["stdout"].(string)
	if !ok {
		t.Fatalf("stdout = %#v, want string", output["stdout"])
	}
	if len(stdout) >= len(large) || !strings.Contains(stdout, "[memoh pruned]") {
		t.Fatalf("terminal end stdout was not limited:\n%s", stdout)
	}
	if truncated, _ := output["truncated"].(bool); !truncated {
		t.Fatalf("terminal end truncated = %#v, want true", output["truncated"])
	}
}
