package store

import (
	"context"
	"fmt"
	"time"
)

func outboxRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Duration(1<<(attempts-1)) * time.Minute
}

// Table names are private constants supplied by the email/SMS entrypoints.
// Recovering a crashed delivery consumes its attempt and uses the same delay
// and maximum as a normal failure. Expiration always wins over dead-lettering.
func (s *Store) recoverOutboxJobs(ctx context.Context, table string) error {
	body := "body_encrypted=''::bytea"
	if table == "email_outbox" {
		body += ",body='[expired]'"
	}
	_, err := s.Pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET status='expired',%s,
 dead_letter_at=NULL,locked_at=NULL
 WHERE status IN ('pending','sending','dead_letter') AND
 (expires_at<=now() OR EXISTS(SELECT 1 FROM verification_challenges v
 WHERE v.id=%s.verification_id AND (v.consumed_at IS NOT NULL OR v.expires_at<=now())))`, table, body, table))
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET
 status=CASE WHEN attempts>=max_attempts THEN 'dead_letter' ELSE 'pending' END,
 dead_letter_at=CASE WHEN attempts>=max_attempts THEN now() ELSE NULL END,
 locked_at=NULL,available_at=CASE WHEN attempts>=max_attempts THEN now()
   ELSE now()+power(2,LEAST(GREATEST(attempts,1),6)-1)*interval '1 minute' END,
 last_error='worker lease expired'
 WHERE (status='sending' AND locked_at<now()-interval '5 minutes')
    OR (status='pending' AND attempts>=max_attempts)`, table))
	return err
}

// locked_at fences completions across manual retries that reset attempts to
// zero; attempts alone could let an old worker complete a newly claimed job.
func (s *Store) failOutboxJob(ctx context.Context, table string, id int64, attempts int, lockedAt time.Time, message string) error {
	expired := fmt.Sprintf(`(expires_at<=now() OR EXISTS(SELECT 1 FROM verification_challenges v
 WHERE v.id=%s.verification_id AND (v.consumed_at IS NOT NULL OR v.expires_at<=now())))`, table)
	body := fmt.Sprintf("body_encrypted=CASE WHEN %s THEN ''::bytea ELSE body_encrypted END", expired)
	if table == "email_outbox" {
		body += fmt.Sprintf(",body=CASE WHEN %s THEN '[expired]' ELSE body END", expired)
	}
	_, err := s.Pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET
 status=CASE WHEN %s THEN 'expired' WHEN attempts>=max_attempts THEN 'dead_letter' ELSE 'pending' END,
 %s,
 dead_letter_at=CASE WHEN %s THEN NULL WHEN attempts>=max_attempts THEN now() ELSE NULL END,
 locked_at=NULL,last_error=$2,available_at=CASE WHEN %s OR attempts>=max_attempts THEN now() ELSE now()+$3::interval END
 WHERE id=$1 AND status='sending' AND attempts=$4 AND locked_at=$5`, table, expired, body, expired, expired),
		id, message, fmt.Sprintf("%d seconds", int(outboxRetryDelay(attempts)/time.Second)), attempts, lockedAt)
	return err
}
