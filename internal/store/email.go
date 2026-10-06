package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type EmailJob struct {
	ID        int64
	Recipient string
	Subject   string
	Body      string
	Kind      string
	Attempts  int
}

func (s *Store) RecoverStaleEmailJobs(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE email_outbox SET status='pending',locked_at=NULL,available_at=now(),last_error='worker lease expired'
WHERE status='sending' AND locked_at<now()-interval '5 minutes'`)
	return err
}

func (s *Store) ClaimEmailJob(ctx context.Context) (*EmailJob, error) {
	var job EmailJob
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (
SELECT id FROM email_outbox WHERE status='pending' AND available_at<=now()
ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE email_outbox e SET status='sending',attempts=e.attempts+1,locked_at=now()
FROM candidate c WHERE e.id=c.id
RETURNING e.id,e.recipient,e.subject,e.body,e.kind,e.attempts`).Scan(&job.ID, &job.Recipient, &job.Subject, &job.Body, &job.Kind, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pgx.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) CompleteEmailJob(ctx context.Context, job EmailJob, sendErr error) error {
	if sendErr == nil {
		_, err := s.Pool.Exec(ctx, "UPDATE email_outbox SET status='sent',locked_at=NULL,last_error='',sent_at=now() WHERE id=$1 AND status='sending'", job.ID)
		return err
	}
	delay := time.Minute
	if job.Attempts >= 2 {
		delay = time.Duration(1<<min(job.Attempts-1, 6)) * time.Minute
	}
	if job.Attempts >= 5 {
		delay = 24 * time.Hour
	}
	_, err := s.Pool.Exec(ctx, "UPDATE email_outbox SET status='pending',locked_at=NULL,last_error=$2,available_at=now()+$3::interval WHERE id=$1 AND status='sending'", job.ID, truncateEmailError(sendErr), fmt.Sprintf("%d seconds", int(delay/time.Second)))
	return err
}

func truncateEmailError(err error) string {
	value := err.Error()
	if len(value) > 1000 {
		return value[:1000]
	}
	return value
}
