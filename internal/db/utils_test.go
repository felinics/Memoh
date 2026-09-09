package db

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
)

func TestDSN(t *testing.T) {
	cfg := config.PostgresConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "memoh",
		Password: "testpw1",
		Database: "memoh",
		SSLMode:  "disable",
	}
	// Build want dynamically to avoid gosec G101 false positive on literal URLs containing passwords.
	want := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s", cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database, cfg.SSLMode)
	if got := DSN(cfg); got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}

func TestDSNEncodesSpecialCharacters(t *testing.T) {
	specialValue := "pa" + "@ss word/#'\"\\"
	cfg := config.PostgresConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "memoh@example",
		Password: specialValue,
		Database: "memoh",
		SSLMode:  "disable",
	}

	parsed, err := url.Parse(DSN(cfg))
	if err != nil {
		t.Fatalf("DSN() produced invalid URL: %v", err)
	}
	if parsed.User.Username() != cfg.User {
		t.Errorf("DSN() username = %q, want %q", parsed.User.Username(), cfg.User)
	}
	password, ok := parsed.User.Password()
	if !ok {
		t.Fatal("DSN() did not include a password")
	}
	if password != cfg.Password {
		t.Errorf("DSN() password = %q, want %q", password, cfg.Password)
	}
	if parsed.Hostname() != cfg.Host {
		t.Errorf("DSN() hostname = %q, want %q", parsed.Hostname(), cfg.Host)
	}
	if parsed.Port() != strconv.Itoa(cfg.Port) {
		t.Errorf("DSN() port = %q, want %d", parsed.Port(), cfg.Port)
	}
	if parsed.Path != "/"+cfg.Database {
		t.Errorf("DSN() path = %q, want /%s", parsed.Path, cfg.Database)
	}
	if parsed.Query().Get("sslmode") != cfg.SSLMode {
		t.Errorf("DSN() sslmode = %q, want %q", parsed.Query().Get("sslmode"), cfg.SSLMode)
	}
}

func TestParseUUID(t *testing.T) {
	validUUID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	tests := []struct {
		name    string
		id      string
		wantErr bool
		want    pgtype.UUID
	}{
		{
			name:    "valid",
			id:      "550e8400-e29b-41d4-a716-446655440000",
			wantErr: false,
			want:    pgtype.UUID{Bytes: validUUID, Valid: true},
		},
		{
			name:    "valid with whitespace",
			id:      "  550e8400-e29b-41d4-a716-446655440000  ",
			wantErr: false,
			want:    pgtype.UUID{Bytes: validUUID, Valid: true},
		},
		{
			name:    "invalid format",
			id:      "not-a-uuid",
			wantErr: true,
		},
		{
			name:    "empty",
			id:      "",
			wantErr: true,
		},
		{
			name:    "partial",
			id:      "550e8400-e29b",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUUID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseUUID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && (got.Valid != tt.want.Valid || got.Bytes != tt.want.Bytes) {
				t.Errorf("ParseUUID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTimeFromPg(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		value pgtype.Timestamptz
		want  time.Time
	}{
		{"valid", pgtype.Timestamptz{Time: now, Valid: true}, now},
		{"invalid", pgtype.Timestamptz{}, time.Time{}},
		{"valid zero", pgtype.Timestamptz{Time: time.Time{}, Valid: true}, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TimeFromPg(tt.value)
			if !got.Equal(tt.want) {
				t.Errorf("TimeFromPg() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTextToString(t *testing.T) {
	tests := []struct {
		name  string
		value pgtype.Text
		want  string
	}{
		{"valid", pgtype.Text{String: "hello", Valid: true}, "hello"},
		{"invalid", pgtype.Text{}, ""},
		{"valid empty", pgtype.Text{String: "", Valid: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TextToString(tt.value); got != tt.want {
				t.Errorf("TextToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("some error"), false},
		{"unique violation", &pgconn.PgError{Code: "23505"}, true},
		{"other pg error", &pgconn.PgError{Code: "23503"}, false},
		{"wrapped unique violation", fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505"}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUniqueViolation(tt.err); got != tt.want {
				t.Errorf("IsUniqueViolation() = %v, want %v", got, tt.want)
			}
		})
	}
}
