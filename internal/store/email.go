package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type EmailJob struct {
	ID                int64
	CorrelationID     string
	Recipient         string
	Subject           string
	Body              string
	Kind              string
	Attempts          int
	VerificationID    string
	ChannelInstanceID string
	LockedAt          time.Time
}

func (s *Store) RecoverStaleEmailJobs(ctx context.Context) error {
	return s.recoverOutboxJobs(ctx, "email_outbox")
}

func (s *Store) ClaimEmailJob(ctx context.Context) (*EmailJob, error) {
	if err := s.expireLegacyDeadlineJobs(ctx, "email_outbox"); err != nil {
		return nil, err
	}
	var job EmailJob
	var sealed []byte
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (
SELECT id FROM email_outbox WHERE status='pending' AND attempts<max_attempts AND available_at<=now() AND (expires_at IS NULL OR expires_at>now()) AND (verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=email_outbox.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) AND `+legacyDeadlineEligibleSQL("email_outbox", "email")+`
ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE email_outbox e SET status='sending',attempts=e.attempts+1,locked_at=now()
FROM candidate c WHERE e.id=c.id
RETURNING e.id,e.recipient,e.subject,e.body,e.kind,e.attempts,e.body_encrypted,coalesce(e.verification_id,''),coalesce(e.channel_instance_id,''),e.locked_at,coalesce(e.correlation_id,'')`).Scan(&job.ID, &job.Recipient, &job.Subject, &job.Body, &job.Kind, &job.Attempts, &sealed, &job.VerificationID, &job.ChannelInstanceID, &job.LockedAt, &job.CorrelationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pgx.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	if len(sealed) > 0 {
		raw, decodeErr := s.VerificationPayload(job.VerificationID, sealed, false)
		if decodeErr != nil {
			_ = s.CompleteEmailJob(ctx, job, fmt.Errorf("email payload unavailable"))
			return nil, decodeErr
		}
		job.Body = string(raw)
	}
	return &job, nil
}

func (s *Store) EmailJobActive(ctx context.Context, job EmailJob) (bool, error) {
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM email_outbox e WHERE e.id=$1 AND e.status='sending' AND e.attempts=$2 AND e.locked_at=$3 AND (e.expires_at IS NULL OR e.expires_at>now()) AND (e.verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=e.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) AND `+legacyDeadlineEligibleSQL("e", "email")+`)`, job.ID, job.Attempts, job.LockedAt).Scan(&active)
	return active, err
}

func (s *Store) CompleteEmailJob(ctx context.Context, job EmailJob, sendErr error) error {
	if sendErr == nil {
		_, err := s.Pool.Exec(ctx, "UPDATE email_outbox SET status='sent',body=CASE WHEN verification_id IS NOT NULL THEN '[delivered]' ELSE body END,body_encrypted=''::bytea,locked_at=NULL,last_error='',sent_at=now() WHERE id=$1 AND status='sending' AND attempts=$2 AND locked_at=$3", job.ID, job.Attempts, job.LockedAt)
		return err
	}
	return s.failOutboxJob(ctx, "email_outbox", job.ID, job.Attempts, job.LockedAt, truncateEmailError(sendErr))
}

func truncateEmailError(err error) string {
	value := err.Error()
	if len(value) > 1000 {
		return value[:1000]
	}
	return value
}
