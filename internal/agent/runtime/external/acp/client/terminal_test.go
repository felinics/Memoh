package client

import (
	"context"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestCreateTerminalStopsWhenOwningRunIsCanceled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runCtx, cancelRun := context.WithCancel(context.Background())
	manager := newTerminalManager(context.Background(), newTestBridgeClient(t, root), "/data", "/data", 30, nil, nil, nil)
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

func TestCreateTerminalRunsACommandLineSentWithoutArgs(t *testing.T) {
	t.Parallel()

	manager := newTerminalManager(context.Background(), newTestBridgeClient(t, t.TempDir()), "/data", "/data", 30, nil, nil, nil)
	term, err := manager.CreateTerminal(context.Background(), acp.CreateTerminalRequest{
		Command: "echo one && echo two",
	}, nil, terminalRuntimeScope{})
	if err != nil {
		t.Fatalf("CreateTerminal() error = %v", err)
	}
	if _, err := manager.WaitForTerminalExit(context.Background(), acp.WaitForTerminalExitRequest{TerminalId: term.TerminalId}); err != nil {
		t.Fatalf("WaitForTerminalExit() error = %v", err)
	}
	out, err := manager.TerminalOutput(context.Background(), acp.TerminalOutputRequest{TerminalId: term.TerminalId})
	if err != nil || out.Output != "one\ntwo\n" {
		t.Fatalf("TerminalOutput() = %q, %v; want the line run by the shell", out.Output, err)
	}
}
