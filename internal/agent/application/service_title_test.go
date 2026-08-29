package application

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type titleModelQueries struct {
	dbstore.Queries
	bot sqlc.GetBotByIDRow
}

func (q titleModelQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

type titleModelAccountStore struct {
	dbstore.AccountStore
	account dbstore.AccountRecord
}

func (s titleModelAccountStore) GetByUserID(context.Context, string) (dbstore.AccountRecord, error) {
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
	botID, err := db.ParseUUID("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := db.ParseUUID("22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	const modelID = "33333333-3333-3333-3333-333333333333"
	accountService := accounts.NewService(nil, titleModelAccountStore{account: dbstore.AccountRecord{
		ID: ownerID.String(), TitleModelID: modelID,
	}})
	resolver := &Service{
		queries:        titleModelQueries{bot: sqlc.GetBotByIDRow{ID: botID, OwnerUserID: ownerID}},
		accountService: accountService,
	}

	gotModelID, gotOwnerID, err := resolver.resolveTitleModel(context.Background(), botID.String())
	if err != nil {
		t.Fatalf("resolveTitleModel() error = %v", err)
	}
	if gotModelID != modelID || gotOwnerID != ownerID.String() {
		t.Fatalf("resolveTitleModel() = (%q, %q), want (%q, %q)", gotModelID, gotOwnerID, modelID, ownerID.String())
	}
}

func TestBoundTitleInput(t *testing.T) {
	longASCII := strings.Repeat("a", 20000)
	longCJK := strings.Repeat("对", 20000)

	tests := []struct {
		name          string
		msg           string
		contextWindow int
		check         func(t *testing.T, got, msg string)
	}{
		{
			name:          "empty stays empty",
			msg:           "",
			contextWindow: 8192,
			check: func(t *testing.T, got, _ string) {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
			},
		},
		{
			name:          "within budget passes through whole",
			msg:           "为什么 Kubernetes 的滚动更新这么慢",
			contextWindow: 8192,
			check: func(t *testing.T, got, msg string) {
				if got != msg {
					t.Fatalf("expected passthrough, got %q", got)
				}
			},
		},
		{
			name:          "unknown window still passes small messages whole",
			msg:           "hello world",
			contextWindow: 0,
			check: func(t *testing.T, got, msg string) {
				if got != msg {
					t.Fatalf("expected passthrough, got %q", got)
				}
			},
		},
		{
			name:          "over budget keeps head and tail with elision",
			msg:           longASCII,
			contextWindow: 8192, // budget = 8192-2048-512 = 5632 runes
			check: func(t *testing.T, got, msg string) {
				runes := []rune(msg)
				head := string(runes[:5632*2/3])
				tail := string(runes[len(runes)-5632/3:])
				if !strings.HasPrefix(got, head) || !strings.HasSuffix(got, tail) {
					t.Fatal("expected head and tail of the original message to be kept")
				}
				if !strings.Contains(got, "\n…\n") {
					t.Fatal("expected elision marker between head and tail")
				}
			},
		},
		{
			name:          "budget counts runes not bytes",
			msg:           longCJK,
			contextWindow: 8192,
			check: func(t *testing.T, got, _ string) {
				if !strings.Contains(got, "\n…\n") {
					t.Fatal("expected CJK message of 20000 runes to be sampled")
				}
			},
		},
		{
			name:          "window smaller than output reservation keeps the floor sample",
			msg:           longASCII,
			contextWindow: 1024,
			check: func(t *testing.T, got, msg string) {
				runes := []rune(msg)
				head := string(runes[:titleInputFloorRunes*2/3])
				if !strings.HasPrefix(got, head) {
					t.Fatal("expected the floor-sized head sample")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, boundTitleInput(tt.msg, tt.contextWindow), tt.msg)
		})
	}
}
