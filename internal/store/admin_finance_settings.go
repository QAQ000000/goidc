package store

import (
	"context"
	"time"
)

// AdminFinanceSettingsSnapshot is the existing seven-field administrator read
// contract. Permission checks remain at the HTTP boundary; this read neither
// fills missing singleton rows nor changes their version.
type AdminFinanceSettingsSnapshot struct {
	TopupEnabled       bool      `json:"topup_enabled"`
	MinTopupCents      int64     `json:"min_topup_cents"`
	MaxTopupCents      int64     `json:"max_topup_cents"`
	MaxBalanceCents    int64     `json:"max_balance_cents"`
	RequireActiveOrder bool      `json:"require_active_order"`
	Version            int64     `json:"version"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (s *Store) AdminFinanceSettings(ctx context.Context) (AdminFinanceSettingsSnapshot, error) {
	var settings AdminFinanceSettingsSnapshot
	err := s.Pool.QueryRow(ctx, `SELECT topup_enabled,min_topup_cents,max_topup_cents,max_balance_cents,require_active_order,version,updated_at FROM finance_settings WHERE id=1`).Scan(
		&settings.TopupEnabled, &settings.MinTopupCents, &settings.MaxTopupCents,
		&settings.MaxBalanceCents, &settings.RequireActiveOrder, &settings.Version, &settings.UpdatedAt,
	)
	return settings, err
}
