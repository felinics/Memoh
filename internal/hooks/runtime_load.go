package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	loadTimeout           = 1200 * time.Millisecond
	maxConfigBytes        = 1024 * 1024
	unavailableLoadNotice = "Hooks configuration could not be loaded for this turn. Hooks were skipped; do not claim their checks or actions ran."
)

type loadStateKey struct{}

type loadState struct {
	mu            sync.Mutex
	notice        string
	repaired      bool
	currentRepair bool
}

// WithLoadState collects service-owned diagnostics for one turn, without
// treating them as hook output or leaking configuration contents.
func WithLoadState(ctx context.Context) context.Context {
	if _, ok := ctx.Value(loadStateKey{}).(*loadState); ok {
		return ctx
	}
	return context.WithValue(ctx, loadStateKey{}, &loadState{})
}

func recordLoadNotice(ctx context.Context, loaded runtimeLoad) {
	notice := loaded.notice()
	if state, ok := ctx.Value(loadStateKey{}).(*loadState); ok {
		state.mu.Lock()
		state.notice = notice
		state.currentRepair = loaded.repaired
		state.repaired = state.repaired || loaded.repaired
		state.mu.Unlock()
	}
}

// LoadNotice returns the latest service-owned loading status for this turn.
func LoadNotice(ctx context.Context) string {
	state, ok := ctx.Value(loadStateKey{}).(*loadState)
	if !ok {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.repaired && !state.currentRepair {
		return strings.TrimSpace("Hooks JSON formatting was repaired earlier in this turn. " + state.notice)
	}
	return state.notice
}

type runtimeLoad struct {
	config         Config
	state          string
	skippedHooks   int
	skippedActions int
	repaired       bool
	saved          bool
}

func (r runtimeLoad) notice() string {
	var parts []string
	switch r.state {
	case "unavailable":
		parts = append(parts, unavailableLoadNotice)
	case "invalid":
		parts = append(parts, "Hooks configuration is invalid or ambiguous and could not be safely repaired. Hooks were skipped; the original file was preserved.")
	case "missing":
		parts = append(parts, "Hooks configuration file does not exist; no hooks were executed.")
	case "empty":
		parts = append(parts, "Hooks configuration is empty; no hooks were executed.")
	case "disabled":
		parts = append(parts, "Hooks are explicitly disabled.")
	}
	if r.skippedHooks > 0 || r.skippedActions > 0 {
		parts = append(parts, fmt.Sprintf("Hooks configuration is partially usable: %d invalid hooks and %d invalid actions were skipped. Valid hooks can run only while hooks are enabled.", r.skippedHooks, r.skippedActions))
	}
	if r.repaired {
		if r.saved {
			parts = append(parts, "Hooks JSON formatting was repaired and saved; the original file was backed up.")
		} else {
			parts = append(parts, "Hooks JSON formatting was repaired in memory but could not be saved. This turn uses the repaired configuration; this repair did not replace the on-disk file.")
		}
	}
	return strings.Join(parts, " ")
}

func emptyRuntimeLoad(state string) runtimeLoad {
	return runtimeLoad{config: Config{Version: 1, Enabled: boolPtr(false)}, state: state}
}

func (s *Service) logLoadError(err error) {
	if s != nil && s.logger != nil {
		s.logger.Warn("hooks configuration unavailable", slog.Any("error", err))
	}
}

// loadRuntime is deliberately tolerant. Load and ParseConfig remain strict
// for management APIs; valid execution policies still go through RunConfig.
func (s *Service) loadRuntime(ctx context.Context, botID string) runtimeLoad {
	if s == nil || s.provider == nil {
		return emptyRuntimeLoad("disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	client, err := s.provider.MCPClient(ctx, strings.TrimSpace(botID))
	if err != nil {
		s.logLoadError(err)
		return emptyRuntimeLoad("unavailable")
	}
	raw, err := readConfigBytes(ctx, client, DefaultConfigPath)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return emptyRuntimeLoad("missing")
		}
		s.logLoadError(err)
		return emptyRuntimeLoad("unavailable")
	}
	loaded, err := parseRuntimeConfig(raw)
	if err == nil {
		return loaded
	}
	repaired, repairErr := repairJSONFormatting(raw)
	if repairErr != nil {
		s.logLoadError(err)
		return emptyRuntimeLoad("invalid")
	}
	loaded, err = parseRuntimeConfig(repaired)
	if err != nil {
		s.logLoadError(err)
		return emptyRuntimeLoad("invalid")
	}
	loaded.repaired = true
	// Serialize this service's repair writers, then verify the source snapshot
	// again immediately before replacement. User edits detected during staging
	// are never overwritten. Keep only the most recent original backup.
	if s.repairMu.TryLock() {
		err = saveRepairedConfig(ctx, client, raw, repaired)
		s.repairMu.Unlock()
	} else {
		// Do not let queued repairs outlive the auxiliary loading budget.
		err = errors.New("another hooks repair is in progress")
	}
	loaded.saved = err == nil
	if err != nil {
		s.logLoadError(err)
	}
	return loaded
}

type repairFileClient interface {
	ReadRaw(context.Context, string) (io.ReadCloser, error)
	WriteFile(context.Context, string, []byte) error
	Rename(context.Context, string, string) error
	DeleteFile(context.Context, string, bool) error
}

func readConfigBytes(ctx context.Context, client repairFileClient, path string) ([]byte, error) {
	rc, err := client.ReadRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(io.LimitReader(rc, maxConfigBytes+1))
	if err == nil && len(raw) > maxConfigBytes {
		err = errors.New("hooks config exceeds size limit")
	}
	return raw, err
}

func saveRepairedConfig(ctx context.Context, client repairFileClient, original, repaired []byte) error {
	unchanged := func() error {
		current, err := readConfigBytes(ctx, client, DefaultConfigPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, original) {
			return errors.New("hooks config changed during repair")
		}
		return nil
	}
	if err := unchanged(); err != nil {
		return err
	}
	temp := DefaultConfigPath + ".repair-" + uuid.NewString()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = client.DeleteFile(cleanup, temp, false)
	}()
	if err := client.WriteFile(ctx, temp, repaired); err != nil {
		return err
	}
	backup := DefaultConfigPath + ".bak"
	if err := client.WriteFile(ctx, backup, original); err != nil {
		return err
	}
	if err := unchanged(); err != nil {
		return err
	}
	return client.Rename(ctx, temp, DefaultConfigPath)
}

func parseRuntimeConfig(raw []byte) (runtimeLoad, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return emptyRuntimeLoad("empty"), nil
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return runtimeLoad{}, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return runtimeLoad{}, err
	}
	if root == nil {
		return runtimeLoad{}, errors.New("hooks config must be an object")
	}
	hooksRaw := root["hooks"]
	delete(root, "hooks")
	header, err := json.Marshal(root)
	if err != nil {
		return runtimeLoad{}, err
	}
	cfg, err := ParseConfig(header)
	if err != nil {
		return runtimeLoad{}, err
	}
	if _, err := parseTimeout(cfg.Defaults.Timeout); err != nil {
		return runtimeLoad{}, err
	}
	if !validOnError(cfg.Defaults.OnError) {
		return runtimeLoad{}, errors.New("invalid default on_error")
	}
	loaded := runtimeLoad{config: cfg}
	if !cfg.enabled() {
		loaded.state = "disabled"
	}
	var entries []json.RawMessage
	if len(hooksRaw) > 0 {
		if err := json.Unmarshal(hooksRaw, &entries); err != nil {
			return runtimeLoad{}, err
		}
	}
	for _, entry := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil || fields == nil {
			loaded.skippedHooks++
			continue
		}
		actionsRaw := fields["actions"]
		delete(fields, "actions")
		hookHeader, marshalErr := json.Marshal(fields)
		if marshalErr != nil {
			loaded.skippedHooks++
			continue
		}
		var h Hook
		if err := json.Unmarshal(hookHeader, &h); err != nil {
			loaded.skippedHooks++
			continue
		}
		one := cfg
		one.Hooks = []Hook{h}
		one.applyDefaults()
		if err := one.validate(); err != nil {
			loaded.skippedHooks++
			continue
		}
		h = one.Hooks[0]
		var actions []json.RawMessage
		if len(actionsRaw) > 0 {
			if err := json.Unmarshal(actionsRaw, &actions); err != nil {
				loaded.skippedHooks++
				continue
			}
		}
		for _, actionRaw := range actions {
			var action HookAction
			if err := json.Unmarshal(actionRaw, &action); err != nil {
				loaded.skippedActions++
				continue
			}
			one.Hooks = []Hook{{Name: h.Name, Event: h.Event, Actions: []HookAction{action}}}
			one.applyDefaults()
			if err := one.validate(); err != nil {
				loaded.skippedActions++
				continue
			}
			h.Actions = append(h.Actions, one.Hooks[0].Actions[0])
		}
		if len(h.Actions) > 0 {
			h.source = hookSource{Kind: sourceKindUser}
			loaded.config.Hooks = append(loaded.config.Hooks, h)
		}
	}
	return loaded, nil
}

func validOnError(value string) bool {
	return value == OnErrorIgnore || value == OnErrorFail || value == OnErrorBlock
}

// Reject ambiguity even when encoding/json would silently use the last key.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		keys := map[string]bool{}
		for decoder.More() {
			if delim == '{' {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate JSON key")
				}
				keys[name] = true
			}
			if err := value(); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	return nil
}

// Only comments outside strings and trailing commas are removed. Structural
// and string damage is never guessed, and all repaired input is parsed again.
func repairJSONFormatting(raw []byte) ([]byte, error) {
	out := append([]byte(nil), raw...)
	inString, escaped := false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c != '/' || i+1 >= len(out) {
			continue
		}
		switch out[i+1] {
		case '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		case '*':
			end := bytes.Index(out[i+2:], []byte("*/"))
			if end < 0 {
				return nil, errors.New("unterminated JSON comment")
			}
			end += i + 4
			for i < end {
				if out[i] != '\n' && out[i] != '\r' {
					out[i] = ' '
				}
				i++
			}
			i--
		}
	}
	if inString {
		return nil, errors.New("unterminated JSON string")
	}
	inString, escaped = false, false
	for i, c := range out {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && strings.ContainsRune(" \t\r\n", rune(out[j])) {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				out[i] = ' '
			}
		}
	}
	if bytes.Equal(out, raw) || !json.Valid(out) {
		return nil, errors.New("JSON cannot be safely repaired")
	}
	return out, nil
}
