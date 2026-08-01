package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/channel"
)

type fakeRateStore struct {
	bots           []botRate
	setBot         botRate
	resetBot       botRate
	keywordBot     botRate
	setRate        float64
	selector       string
	keyword        string
	keywordChanged bool
	err            error
}

func (s *fakeRateStore) List(context.Context) ([]botRate, error) {
	return s.bots, s.err
}

func (s *fakeRateStore) Set(_ context.Context, selector string, rate float64) (botRate, error) {
	s.selector = selector
	s.setRate = rate
	return s.setBot, s.err
}

func (s *fakeRateStore) Reset(_ context.Context, selector string) (botRate, error) {
	s.selector = selector
	return s.resetBot, s.err
}

func (s *fakeRateStore) AddKeyword(_ context.Context, selector, keyword string) (botRate, bool, error) {
	s.selector = selector
	s.keyword = keyword
	return s.keywordBot, s.keywordChanged, s.err
}

func (s *fakeRateStore) RemoveKeyword(_ context.Context, selector, keyword string) (botRate, error) {
	s.selector = selector
	s.keyword = keyword
	return s.keywordBot, s.err
}

func TestParseConfig(t *testing.T) {
	t.Run("set percentage", func(t *testing.T) {
		cfg, err := parseConfig([]string{"--sudo=false", "set", "alice", "12.5%"}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parseConfig() error = %v", err)
		}
		if cfg.action != "set" || cfg.selector != "alice" || cfg.rate != 0.125 || cfg.useSudo {
			t.Fatalf("parseConfig() = %+v", cfg)
		}
	})

	t.Run("no command lists", func(t *testing.T) {
		cfg, err := parseConfig(nil, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parseConfig() error = %v", err)
		}
		if cfg.action != "list" {
			t.Fatalf("action = %q, want list", cfg.action)
		}
	})

	t.Run("reject invalid percentage", func(t *testing.T) {
		for _, value := range []string{"101", "NaN", "-1"} {
			if _, err := parseConfig([]string{"set", "alice", value}, &bytes.Buffer{}); err == nil {
				t.Fatalf("parseConfig() unexpectedly accepted %q", value)
			}
		}
	})

	t.Run("keyword add", func(t *testing.T) {
		cfg, err := parseConfig([]string{"keyword-add", "alice", "小雪"}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parseConfig() error = %v", err)
		}
		if cfg.action != "keyword-add" || cfg.selector != "alice" || cfg.keyword != "小雪" {
			t.Fatalf("parseConfig() = %+v", cfg)
		}
	})
}

func TestExecuteSet(t *testing.T) {
	rate := 0.4
	store := &fakeRateStore{
		setBot: botRate{
			ID:             "bot-id",
			Name:           "alice",
			ConfiguredRate: &rate,
		},
	}
	var stdout, stderr bytes.Buffer
	exitCode := execute(
		context.Background(),
		config{action: "set", selector: "alice", rate: rate},
		store,
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("execute() = %d, stderr = %s", exitCode, stderr.String())
	}
	if store.selector != "alice" || store.setRate != rate {
		t.Fatalf("Set() selector/rate = %q/%v", store.selector, store.setRate)
	}
	if !strings.Contains(stdout.String(), "40%") || !strings.Contains(stdout.String(), "立即生效") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestExecuteListUsesDefaultRate(t *testing.T) {
	store := &fakeRateStore{bots: []botRate{{
		ID:          "bot-id",
		Name:        "alice",
		DisplayName: "Alice",
	}}}
	var stdout, stderr bytes.Buffer
	exitCode := execute(
		context.Background(),
		config{action: "list"},
		store,
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("execute() = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "25%") || !strings.Contains(stdout.String(), "默认") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestExecuteKeywordAdd(t *testing.T) {
	store := &fakeRateStore{keywordBot: botRate{
		ID:   "bot-id",
		Name: "alice",
	}, keywordChanged: true}
	var stdout, stderr bytes.Buffer
	exitCode := execute(
		context.Background(),
		config{action: "keyword-add", selector: "alice", keyword: "小雪"},
		store,
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("execute() = %d, stderr = %s", exitCode, stderr.String())
	}
	if store.selector != "alice" || store.keyword != "小雪" {
		t.Fatalf("AddKeyword() selector/keyword = %q/%q", store.selector, store.keyword)
	}
	if !strings.Contains(stdout.String(), "小雪") || !strings.Contains(stdout.String(), "立即生效") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestExecuteKeywordAddReportsExistingKeyword(t *testing.T) {
	store := &fakeRateStore{keywordBot: botRate{ID: "bot-id", Name: "alice"}}
	var stdout, stderr bytes.Buffer
	exitCode := execute(context.Background(), config{action: "keyword-add", selector: "alice", keyword: "小雪"}, store, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("execute() = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "已存在") || !strings.Contains(stdout.String(), "未做变更") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestExecuteReportsStoreError(t *testing.T) {
	store := &fakeRateStore{err: errors.New("database unavailable")}
	var stdout, stderr bytes.Buffer
	exitCode := execute(
		context.Background(),
		config{action: "list"},
		store,
		&stdout,
		&stderr,
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "database unavailable") {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestEffectiveRateRejectsInvalidStoredValue(t *testing.T) {
	invalid := 1.5
	bot := botRate{ConfiguredRate: &invalid}
	if got := bot.effectiveRate(); got != channel.DefaultTelegramDiscussPassiveSampleRate {
		t.Fatalf("effectiveRate() = %v", got)
	}
}
