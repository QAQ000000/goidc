package store

import "testing"

// historicalAccountSnapshotRows compares every persisted pre-split account
// field across an upgrade. Account credentials move into administrators with
// the same ID; the new generated discriminator is not a historical field.
// Keep this separate from the public profile view, which omits credentials.
func historicalAccountSnapshotRows(t *testing.T, db *Store) string {
	t.Helper()
	var split bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT to_regclass('administrators') IS NOT NULL`).Scan(&split); err != nil {
		t.Fatal(err)
	}
	rows := `SELECT to_jsonb(u)-'principal_kind' AS profile FROM users u`
	if split {
		rows += ` UNION ALL SELECT to_jsonb(a)-'principal_kind' FROM administrators a`
	}
	return `(SELECT jsonb_agg(profile ORDER BY (profile->>'id')::bigint) FROM (` + rows + `) account_snapshot)`
}
