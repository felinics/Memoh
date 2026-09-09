package db

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
)

// DSN builds a PostgreSQL connection string from config.
func DSN(cfg config.PostgresConfig) string {
	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   cfg.Database,
	}
	query := dsn.Query()
	query.Set("sslmode", cfg.SSLMode)
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

// ParseUUID converts a string UUID to pgtype.UUID.
func ParseUUID(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID: %w", err)
	}
	var pgID pgtype.UUID
	pgID.Valid = true
	copy(pgID.Bytes[:], parsed[:])
	return pgID, nil
}

// ParseUUIDOrEmpty converts a string UUID to pgtype.UUID, returning an invalid UUID if the string is empty or unparsable.
func ParseUUIDOrEmpty(id string) pgtype.UUID {
	id = strings.TrimSpace(id)
	if id == "" {
		return pgtype.UUID{}
	}
	pgID, err := ParseUUID(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgID
}

// TimeFromPg converts a pgtype.Timestamptz to time.Time.
func TimeFromPg(value pgtype.Timestamptz) time.Time {
	if value.Valid {
		return value.Time
	}
	return time.Time{}
}

// TextToString returns the string value of pgtype.Text, or "" when invalid.
func TextToString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// IsUniqueViolation reports whether err is a PostgreSQL UNIQUE constraint violation.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
