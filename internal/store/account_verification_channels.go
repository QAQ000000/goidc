package store

import "context"

func (s *Store) AccountVerificationChannelEnabled(ctx context.Context, channel string) (bool, error) {
	var enabled bool
	err := s.Pool.QueryRow(ctx, "SELECT CASE WHEN $1='sms' THEN verification_sms_enabled ELSE verification_email_enabled END FROM auth_settings WHERE id=1", channel).Scan(&enabled)
	return enabled, err
}
