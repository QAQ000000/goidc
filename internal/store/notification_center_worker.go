package store

import (
	"context"
	"regexp"
	"time"

	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
)

// notificationEligible checks current consent and ownership, not just the
// enqueue snapshot. Version checks prevent an opt-out/rebind cycle reviving
// messages created before the change. External adapters still resolve their
// compiled factory and credentials immediately before sending.
const notificationEligible = `EXISTS(SELECT 1 FROM notification_events e JOIN invoices i ON i.id=e.invoice_id JOIN users u ON u.id=e.user_id JOIN notification_routes r ON r.id=d.route_id JOIN notification_message_templates m ON m.id=r.template_id JOIN user_notification_preferences p ON p.user_id=e.user_id AND p.channel=d.channel WHERE e.id=d.event_id AND i.user_id=e.user_id AND i.status='paid' AND u.status='active' AND r.enabled AND r.version=d.route_version AND m.active AND p.enabled AND p.version=d.preference_version AND (d.channel='inbox' OR (d.channel='telegram' AND EXISTS(SELECT 1 FROM notification_targets t JOIN channel_instances c ON c.id=t.instance_id WHERE t.id=d.target_id AND t.user_id=e.user_id AND t.active AND t.version=d.target_version AND t.target=d.target AND t.instance_id=d.instance_id AND c.category='bot' AND c.implementation_id='telegram_bot' AND c.enabled AND r.instance_id=c.id))))`

func (s *Store) ClaimNotificationJob(ctx context.Context) (*notify.Job, error) {
	for n := 0; n < 32; n++ {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		job, err := s.claimNotificationJobTx(ctx, tx)
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		if job != nil {
			return job, nil
		}
	}
	return nil, pgx.ErrNoRows
}

func (s *Store) claimNotificationJobTx(ctx context.Context, tx pgx.Tx) (*notify.Job, error) {
	var job notify.Job
	var userID int64
	var attempts int
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT d.id,d.channel,coalesce(d.instance_id,''),d.target,d.subject,d.body,d.expires_at,e.user_id,d.attempts,`+notificationEligible+` FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE d.status='pending' AND d.channel IN ('telegram','inbox') AND d.next_attempt_at<=clock_timestamp() ORDER BY d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1`).Scan(&job.ID, &job.Channel, &job.InstanceID, &job.Target, &job.Subject, &job.Body, &job.ExpiresAt, &userID, &attempts, &eligible)
	if err != nil {
		return nil, err
	}
	if !notificationMessageValid(job.Channel, job.Subject, job.Body) {
		_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='failed',code='rendered_message_invalid',updated_at=now() WHERE id=$1`, job.ID)
		return nil, err
	}
	if !eligible || !job.ExpiresAt.After(time.Now()) || attempts >= 5 {
		_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='skipped',code='eligibility_changed',updated_at=now() WHERE id=$1`, job.ID)
		return nil, err
	}
	// Consent updates lock the user before preferences/targets; taking the
	// same locks here serializes claim against those changes.
	var id int64
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND status='active' FOR SHARE`, userID).Scan(&id); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT user_id FROM user_notification_preferences WHERE user_id=$1 AND channel=$2 FOR SHARE`, userID, job.Channel); err != nil {
		return nil, err
	}
	if job.Channel == "telegram" {
		if _, err = tx.Exec(ctx, `SELECT id FROM notification_targets WHERE id=(SELECT target_id FROM notification_deliveries WHERE id=$1) FOR SHARE`, job.ID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM channel_instances WHERE id=$1 FOR SHARE`, job.InstanceID); err != nil {
			return nil, err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT `+notificationEligible+` FROM notification_deliveries d WHERE d.id=$1`, job.ID).Scan(&eligible); err != nil {
		return nil, err
	}
	if !eligible {
		_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='skipped',code='eligibility_changed',updated_at=now() WHERE id=$1`, job.ID)
		return nil, err
	}
	job.LeaseToken, err = notificationToken()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='sending',attempts=attempts+1,lease_token=$2,lease_until=clock_timestamp()+interval '2 minutes',updated_at=now() WHERE id=$1`, job.ID, job.LeaseToken); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO notification_delivery_attempts(delivery_id,attempt,lease_token,status) VALUES($1,$2,$3,'sending')`, job.ID, attempts+1, job.LeaseToken); err != nil {
		return nil, err
	}
	if job.Channel == "inbox" {
		var messageID int64
		if err = tx.QueryRow(ctx, `INSERT INTO admin_inbox_messages(user_id,subject,body) VALUES($1,$2,$3) RETURNING id`, userID, job.Subject, job.Body).Scan(&messageID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='accepted',inbox_message_id=$2,code='inbox_stored',lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, job.ID, messageID); err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE notification_delivery_attempts SET status='accepted',code='inbox_stored',finished_at=now() WHERE lease_token=$1`, job.LeaseToken)
		return nil, err
	}
	return &job, nil
}

func (s *Store) NotificationJobActive(ctx context.Context, job notify.Job) (bool, error) {
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries d WHERE d.id=$1 AND d.lease_token=$2 AND d.status='sending' AND d.lease_until>clock_timestamp() AND d.expires_at>clock_timestamp() AND `+notificationEligible+`)`, job.ID, job.LeaseToken).Scan(&active)
	return active, err
}

var notificationSafeCode = regexp.MustCompile(`^[a-z0-9_]{0,64}$`)

func (s *Store) CompleteNotificationJob(ctx context.Context, job notify.Job, result notify.Result) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var attempts int
	var expires time.Time
	if err = tx.QueryRow(ctx, `SELECT attempts,expires_at FROM notification_deliveries WHERE id=$1 AND lease_token=$2 AND status='sending' AND lease_until>clock_timestamp() FOR UPDATE`, job.ID, job.LeaseToken).Scan(&attempts, &expires); err == pgx.ErrNoRows {
		return conflict("notification_lease_lost", "通知处理租约已失效")
	}
	if err != nil {
		return err
	}
	status, attemptStatus, code := result.Outcome, result.Outcome, result.Code
	if !notificationSafeCode.MatchString(code) {
		code = "provider_error"
	}
	next := time.Now()
	switch status {
	case "accepted", "failed", "unknown":
	case "rate_limited":
		status, attemptStatus = "failed", "failed"
		code = "rate_limited"
		if result.RetryAfter > 0 && attempts < 5 {
			next = next.Add(max(time.Second, result.RetryAfter))
			if next.Before(expires) {
				status = "pending"
			}
		}
	default:
		status, attemptStatus, code = "unknown", "unknown", "invalid_provider_outcome"
	}
	messageID := result.ProviderMessageID
	if len(messageID) > 128 {
		messageID = ""
	}
	tag, err := tx.Exec(ctx, `UPDATE notification_deliveries SET status=$3,code=$4,provider_message_id=$5,next_attempt_at=$6,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2 AND status='sending' AND lease_until>clock_timestamp()`, job.ID, job.LeaseToken, status, code, messageID, next)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return conflict("notification_lease_lost", "通知处理租约已失效")
	}
	if _, err = tx.Exec(ctx, `UPDATE notification_delivery_attempts SET status=$2,code=$3,provider_message_id=$4,finished_at=now() WHERE lease_token=$1 AND status='sending'`, job.LeaseToken, attemptStatus, code, messageID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecoverStaleNotificationJobs(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `WITH stale AS(UPDATE notification_deliveries SET status='unknown',code='lease_expired',lease_token=NULL,lease_until=NULL,updated_at=now() WHERE status='sending' AND lease_until<=clock_timestamp() RETURNING id) UPDATE notification_delivery_attempts a SET status='unknown',code='lease_expired',finished_at=now() FROM stale s WHERE a.delivery_id=s.id AND a.status='sending'`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='skipped',code='expired',updated_at=now() WHERE status='pending' AND channel IN ('telegram','inbox') AND expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
