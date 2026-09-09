package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	a11yCLIPath        = "/opt/memoh/toolkit/display/bin/a11y-cli"
	a11yExecTimeoutSec = 15
)

type a11yPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type a11ySnapshotItem struct {
	Ref     string     `json:"ref"`
	Role    string     `json:"role"`
	Name    string     `json:"name"`
	Center  *a11yPoint `json:"center,omitempty"`
	CenterX int        `json:"center_x,omitempty"`
	CenterY int        `json:"center_y,omitempty"`
}

type a11ySnapshotOutput struct {
	Items   []a11ySnapshotItem `json:"items"`
	Lines   string             `json:"lines"`
	RefPath string             `json:"refs_path"`
}

type a11yActionOutput struct {
	OK       bool       `json:"ok"`
	Ref      string     `json:"ref"`
	Action   string     `json:"action"`
	Fallback *a11yPoint `json:"fallback,omitempty"`
	Error    string     `json:"error,omitempty"`
}

func execA11y(ctx context.Context, client *bridge.Client, args ...string) ([]byte, error) {
	if client == nil {
		return nil, errors.New("workspace bridge client is not configured")
	}
	cmd := fmt.Sprintf("DISPLAY=:99 %s %s", shellQuote(a11yCLIPath), shellQuoteArgs(args))
	result, err := client.Exec(ctx, cmd, "/", a11yExecTimeoutSec)
	if err != nil {
		return nil, err
	}
	stdout := strings.TrimSpace(result.Stdout)
	if result.ExitCode != 0 {
		stderr := strings.TrimSpace(result.Stderr)
		if stderr == "" {
			stderr = stdout
		}
		if stderr == "" {
			stderr = fmt.Sprintf("a11y-cli exited with code %d", result.ExitCode)
		}
		return nil, errors.New(stderr)
	}
	if stdout == "" {
		return nil, errors.New("a11y-cli returned empty output")
	}
	return []byte(stdout), nil
}

func computerA11ySnapshot(ctx context.Context, client *bridge.Client) (*a11ySnapshotOutput, error) {
	raw, err := execA11y(ctx, client, "snapshot")
	if err != nil {
		return nil, err
	}
	var out a11ySnapshotOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse snapshot output: %w", err)
	}
	for i := range out.Items {
		if out.Items[i].Center != nil {
			out.Items[i].CenterX = out.Items[i].Center.X
			out.Items[i].CenterY = out.Items[i].Center.Y
		}
	}
	return &out, nil
}

func computerA11yClick(ctx context.Context, client *bridge.Client, ref string) (*a11yActionOutput, error) {
	return runA11yAction(ctx, client, "click", ref, "")
}

func computerA11yEdit(ctx context.Context, client *bridge.Client, ref, text string, replace bool) (*a11yActionOutput, error) {
	action := "type"
	if replace {
		action = "fill"
	}
	return runA11yAction(ctx, client, action, ref, text)
}

func runA11yAction(ctx context.Context, client *bridge.Client, action, ref, text string) (*a11yActionOutput, error) {
	args := []string{action, "--ref", ref}
	if text != "" {
		args = append(args, "--text", text)
	}
	raw, err := execA11y(ctx, client, args...)
	if err != nil {
		return nil, err
	}
	var out a11yActionOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse a11y-cli %s output: %w", action, err)
	}
	return &out, nil
}

func lookupComputerRef(ctx context.Context, containers bridge.Provider, botID, ref string) (*a11ySnapshotItem, error) {
	if containers == nil {
		return nil, errors.New("workspace runtime provider is not configured")
	}
	client, err := containers.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	snapshot, err := computerA11ySnapshot(ctx, client)
	if err != nil {
		return nil, err
	}
	normalized := normalizeBrowserRef(ref)
	for i := range snapshot.Items {
		if normalizeBrowserRef(snapshot.Items[i].Ref) == normalized {
			return &snapshot.Items[i], nil
		}
	}
	return nil, nil
}

func shellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func shellQuoteArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}
