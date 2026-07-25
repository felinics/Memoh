package application

import (
	"context"
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/iam/account"
)

type titleModelBotReader struct {
	bot bot.Bot
}

func (q titleModelBotReader) GetForAccess(context.Context, string) (bot.Bot, error) {
	return q.bot, nil
}

type titleModelAccountStore struct {
	account.Store
	account account.Record
}

func (s titleModelAccountStore) GetByUserID(context.Context, string) (account.Record, error) {
	return s.account, nil
}

func TestFallbackSessionTitle(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "short first line passes through",
			input: "How do I parse JSON in Go?",
			want:  "How do I parse JSON in Go?",
		},
		{
			name:  "only the first line is used",
			input: "Summarize this file\n\nHere is the content:\nlots\nof\nlines",
			want:  "Summarize this file",
		},
		{
			name:  "long line is capped with an ellipsis",
			input: strings.Repeat("A", 120),
			want:  strings.Repeat("A", 50) + "…",
		},
		{
			name:  "truncation counts runes not bytes for CJK",
			input: strings.Repeat("あ", 80),
			want:  strings.Repeat("あ", 50) + "…",
		},
		{
			name:  "heading and inline code and link are stripped",
			input: "## Title\n`code` and [a link](https://x)",
			want:  "Title",
		},
		{
			name:  "emphasis markers are stripped",
			input: "**bold** and *italic* here",
			want:  "bold and italic here",
		},
		{
			name:  "inline code is unwrapped",
			input: "Check `fmt.Println` usage",
			want:  "Check fmt.Println usage",
		},
		{
			name:  "complete code fence yields nothing",
			input: "```js\nconst x = 1\n```",
			want:  "",
		},
		{
			name:  "whitespace-only yields nothing",
			input: "   \n  ",
			want:  "",
		},
		{
			name:  "empty yields nothing",
			input: "",
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fallbackSessionTitle(tc.input); got != tc.want {
				t.Fatalf("fallbackSessionTitle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestResolveTitleModelUsesBotOwnerProfile(t *testing.T) {
	const botID = "11111111-1111-1111-1111-111111111111"
	const ownerID = "22222222-2222-2222-2222-222222222222"
	const modelID = "33333333-3333-3333-3333-333333333333"
	accountService := account.NewService(nil, titleModelAccountStore{account: account.Record{
		ID: ownerID, TitleModelID: modelID,
	}})
	resolver := &Service{
		bots:     titleModelBotReader{bot: bot.Bot{ID: botID, OwnerUserID: ownerID}},
		accounts: accountService,
	}

	gotModelID, gotOwnerID, err := resolver.resolveTitleModel(context.Background(), botID)
	if err != nil {
		t.Fatalf("resolveTitleModel() error = %v", err)
	}
	if gotModelID != modelID || gotOwnerID != ownerID {
		t.Fatalf("resolveTitleModel() = (%q, %q), want (%q, %q)", gotModelID, gotOwnerID, modelID, ownerID)
	}
}
