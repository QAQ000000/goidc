package store

// Predicates mirror the workers' claim candidates. They count due work rather
// than all historical pending rows; future retries have their own column.
// A due notification may still need an eligibility skip, which is real work
// for its loop. Legacy verification/deadline exclusions match legacy claims.
func workerQueueQueries() []string {
	emailEligible := `attempts<max_attempts AND (expires_at IS NULL OR expires_at>now()) AND (verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=email_outbox.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) AND ` + legacyDeadlineEligibleSQL("email_outbox", "email")
	smsEligible := `attempts<max_attempts AND expires_at>now() AND (verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=sms_outbox.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) AND ` + legacyDeadlineEligibleSQL("sms_outbox", "sms")
	return []string{
		`SELECT 'provision',count(*) FILTER(WHERE status='pending' AND available_at<=now()),count(*) FILTER(WHERE status='pending' AND available_at>now()),min(available_at) FILTER(WHERE status='pending' AND available_at<=now()),count(*) FILTER(WHERE status='running'),count(*) FILTER(WHERE status='running' AND locked_at<now()-interval '5 minutes'),count(*) FILTER(WHERE status='failed' AND completed_at>=now()-interval '1 hour'),count(*) FILTER(WHERE status='waiting_verification' AND updated_at>=now()-interval '1 hour'),0::bigint FROM provision_jobs`,
		legacyWorkerQueueQuery("email", "email_outbox", emailEligible),
		legacyWorkerQueueQuery("sms", "sms_outbox", smsEligible),
		`SELECT 'notification_center',count(*) FILTER(WHERE status='pending' AND next_attempt_at<=now()),count(*) FILTER(WHERE status='pending' AND next_attempt_at>now()),min(next_attempt_at) FILTER(WHERE status='pending' AND next_attempt_at<=now()),count(*) FILTER(WHERE status='sending'),count(*) FILTER(WHERE status='sending' AND lease_until<=now()),count(*) FILTER(WHERE status='failed' AND updated_at>=now()-interval '1 hour'),count(*) FILTER(WHERE status='unknown' AND updated_at>=now()-interval '1 hour'),0::bigint FROM notification_deliveries WHERE channel IN ('telegram','inbox','web_push')`,
		`SELECT 'certification',count(*) FILTER(WHERE status IN ('queued','waiting','uncertain') AND next_run_at<=now()),count(*) FILTER(WHERE status IN ('queued','waiting','uncertain') AND next_run_at>now()),min(next_run_at) FILTER(WHERE status IN ('queued','waiting','uncertain') AND next_run_at<=now()),count(*) FILTER(WHERE status IN ('submitting','querying')),count(*) FILTER(WHERE status IN ('submitting','querying') AND lease_until<=now()),count(*) FILTER(WHERE status='needs_review' AND updated_at>=now()-interval '1 hour'),count(*) FILTER(WHERE status='uncertain' AND updated_at>=now()-interval '1 hour'),0::bigint FROM certification_provider_jobs`,
		`SELECT 'payment_checks',0::bigint,0::bigint,NULL::timestamptz,count(*) FILTER(WHERE status='checking'),count(*) FILTER(WHERE status='checking' AND lease_until<=now()),count(*) FILTER(WHERE status='error' AND completed_at>=now()-interval '1 hour'),0::bigint,0::bigint FROM payment_reconciliation_checks`,
		`SELECT 'file_cleanup',count(*) FILTER(WHERE state IN ('staging','ready') AND gc_after<=now()),count(*) FILTER(WHERE state IN ('staging','ready') AND gc_after>now()),min(gc_after) FILTER(WHERE state IN ('staging','ready') AND gc_after<=now()),count(*) FILTER(WHERE state='deleting'),count(*) FILTER(WHERE state='deleting' AND delete_lease_until<=now()),count(*) FILTER(WHERE last_error_code<>'' AND updated_at>=now()-interval '1 hour'),count(*) FILTER(WHERE state='uncertain' AND updated_at>=now()-interval '1 hour'),0::bigint FROM file_storage_objects`,
	}
}

func legacyWorkerQueueQuery(name, table, eligible string) string {
	return `SELECT '` + name + `',count(*) FILTER(WHERE status='pending' AND available_at<=now() AND ` + eligible + `),count(*) FILTER(WHERE status='pending' AND available_at>now() AND ` + eligible + `),min(available_at) FILTER(WHERE status='pending' AND available_at<=now() AND ` + eligible + `),count(*) FILTER(WHERE status='sending'),count(*) FILTER(WHERE status='sending' AND locked_at<now()-interval '5 minutes'),0::bigint,0::bigint,count(*) FILTER(WHERE status='dead_letter' AND dead_letter_at>=now()-interval '1 hour') FROM ` + table
}
