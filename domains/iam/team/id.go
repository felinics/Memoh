// Package team holds the default team identity used by self-hosted Memoh.
package team

// DefaultTeamID is the stable, published identity of the singleton team used
// by self-hosted (single-team) installs. It is a fixed constant — NEVER
// generated per install — so that migrations, fixtures, and application code can
// reference the same value across environments.
//
// It is seeded by migration 0112_team_core and referenced as the backfill
// value when propagating team_id onto existing business rows.
const DefaultTeamID = "00000000-0000-0000-0000-000000000001"
