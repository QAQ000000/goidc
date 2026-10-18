package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/certification"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/jackc/pgx/v5"
)

type certificationJob struct {
	id, submissionID, profileID, userID, profileVersion int64
	instanceVersion                                     int64
	schemaVersion, queryAttempts                        int
	provider, instanceID, kind, requestID, subject      string
	lease, action, payloadAAD, snapshotAAD              string
	payload, snapshot                                   []byte
	config                                              module.Config
	descriptor                                          channel.Descriptor
}

// ProcessCertificationJob makes at most one external call. A persisted submit
// marker is never cleared: expiry, timeout and process death can only lead to
// queries or manual verification, never an automatic second submission.
func (s *Store) ProcessCertificationJob(ctx context.Context) (bool, error) {
	job, processed, err := s.claimCertificationJob(ctx)
	if err != nil || job == nil {
		return processed, err
	}
	provider, err := newCertificationProvider(job.provider, job.config)
	if err != nil {
		return true, s.certificationJobNeedsReview(ctx, *job, "provider_unavailable")
	}
	var payload map[string]any
	if job.action == "submit" {
		payload, err = openCertification(s.ProvisionEncryptionKey, job.payloadAAD, job.payload)
		if err != nil {
			return true, s.certificationJobNeedsReview(ctx, *job, "payload_unavailable")
		}
		// Recheck a dispatch that was claimed before a switch/instance change.
		var allowed bool
		err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM certification_provider_jobs j
 JOIN certification_submissions sub ON sub.id=j.submission_id
 JOIN certification_profiles p ON p.id=sub.profile_id
 JOIN channel_instances c ON c.id=sub.channel_instance_id
 JOIN certification_settings cfg ON cfg.id=1
 WHERE j.id=$1 AND j.lease_token=$2 AND j.status='submitting' AND j.lease_until>clock_timestamp()
 AND p.status='pending' AND p.version=sub.profile_version AND cfg.enabled AND c.enabled)`, job.id, job.lease).Scan(&allowed)
		if err != nil {
			return true, err
		}
		if !allowed {
			return true, s.certificationJobNeedsReview(ctx, *job, "dispatch_unavailable")
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	result, callErr := callCertificationProvider(callCtx, provider, *job, payload)
	cancel()
	// A provider deadline must not cancel the transaction that records its
	// unknown outcome. Application shutdown may still leave lease recovery.
	return true, s.finishCertificationJob(ctx, *job, result, callErr)
}

func newCertificationProvider(id string, cfg module.Config) (provider certification.Provider, err error) {
	defer func() {
		if recover() != nil {
			provider, err = nil, errors.New("certification provider unavailable")
		}
	}()
	return certification.New(id, cfg)
}

func callCertificationProvider(ctx context.Context, provider certification.Provider, job certificationJob, payload map[string]any) (result certification.Result, err error) {
	defer func() {
		if recover() != nil {
			result, err = certification.Result{}, errors.New("certification provider unavailable")
		}
	}()
	if job.action == "submit" {
		return provider.Submit(ctx, certification.Submission{RequestID: job.requestID, Type: job.kind, Payload: payload})
	}
	return provider.Query(ctx, certification.Query{RequestID: job.requestID, ProviderSubject: job.subject})
}

func (s *Store) claimCertificationJob(ctx context.Context) (*certificationJob, bool, error) {
	var candidate, profileID int64
	var instanceID, providerID string
	err := s.Pool.QueryRow(ctx, `SELECT j.id,sub.profile_id,sub.channel_instance_id,sub.provider_id FROM certification_provider_jobs j
 JOIN certification_submissions sub ON sub.id=j.submission_id
 WHERE ((j.status IN ('queued','waiting','uncertain') AND j.next_run_at<=clock_timestamp())
 OR (j.status IN ('submitting','querying') AND j.lease_until<=clock_timestamp())) ORDER BY j.next_run_at,j.id LIMIT 1`).Scan(&candidate, &profileID, &instanceID, &providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var out *certificationJob
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		// Keep the same settings -> channel -> profile -> job order as new
		// applications. Even shared locks can wait behind a queued writer.
		var masterEnabled, instanceEnabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM certification_settings WHERE id=1 FOR SHARE`).Scan(&masterEnabled); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT enabled FROM channel_instances WHERE id=$1 AND category='certification' AND implementation_id=$2 FOR SHARE`, instanceID, providerID).Scan(&instanceEnabled); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var profileVersion int64
		var profileStatus string
		if err := tx.QueryRow(ctx, `SELECT version,status FROM certification_profiles WHERE id=$1 FOR UPDATE`, profileID).Scan(&profileVersion, &profileStatus); err != nil {
			return err
		}
		var job certificationJob
		var state string
		var submitAttempts int
		var due bool
		err := tx.QueryRow(ctx, `SELECT j.id,j.status,j.submit_attempts,j.query_attempts,
 ((j.status IN ('queued','waiting','uncertain') AND j.next_run_at<=clock_timestamp()) OR (j.status IN ('submitting','querying') AND j.lease_until<=clock_timestamp())),
 sub.id,sub.profile_id,sub.user_id,sub.profile_version,sub.provider_id,sub.channel_instance_id,sub.channel_instance_version,sub.channel_schema_version,
 sub.certification_type,sub.request_id,sub.provider_subject,sub.payload_encrypted,sub.payload_aad,sub.config_snapshot_encrypted,sub.config_snapshot_aad
 FROM certification_provider_jobs j JOIN certification_submissions sub ON sub.id=j.submission_id WHERE j.id=$1 FOR UPDATE OF j`, candidate).Scan(
			&job.id, &state, &submitAttempts, &job.queryAttempts, &due, &job.submissionID, &job.profileID, &job.userID, &job.profileVersion, &job.provider, &job.instanceID, &job.instanceVersion, &job.schemaVersion,
			&job.kind, &job.requestID, &job.subject, &job.payload, &job.payloadAAD, &job.snapshot, &job.snapshotAAD)
		if err != nil {
			return err
		}
		if !due {
			return nil
		}
		if profileStatus != "pending" || profileVersion != job.profileVersion {
			_, err = tx.Exec(ctx, `UPDATE certification_provider_jobs SET status='cancelled',code='application_superseded',lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, job.id)
			return err
		}
		job.action = "query"
		if state == "queued" && submitAttempts == 0 {
			job.action = "submit"
			if !masterEnabled {
				return s.markCertificationNeedsReviewTx(ctx, tx, job, "certification_disabled")
			}
			if !instanceEnabled {
				return s.markCertificationNeedsReviewTx(ctx, tx, job, "instance_disabled")
			}
		}
		desc, exists := channel.Lookup(channel.Certification, job.provider)
		if !exists || !channel.HasFactory(channel.Certification, job.provider) || desc.SchemaVersion != job.schemaVersion {
			return s.markCertificationNeedsReviewTx(ctx, tx, job, "provider_unavailable")
		}
		job.descriptor = desc
		if job.action == "query" && (job.queryAttempts >= 5 || !certificationCanQuery(job, job.subject)) {
			return s.markCertificationNeedsReviewTx(ctx, tx, job, "query_unavailable")
		}
		cfg, err := s.openCertificationConfig(job.submissionID, job.instanceID, job.instanceVersion, job.snapshotAAD, job.snapshot)
		if err != nil {
			return s.markCertificationNeedsReviewTx(ctx, tx, job, "snapshot_unavailable")
		}
		flags := map[string]bool{}
		for _, key := range channel.SecretKeys(desc) {
			flags[key] = cfg.Secrets[key] != ""
		}
		if _, failures := channel.ValidateConfiguration(desc, cfg.Values, flags, true); len(failures) > 0 || len(channel.ValidateSecretValues(desc, cfg.Secrets)) > 0 {
			return s.markCertificationNeedsReviewTx(ctx, tx, job, "snapshot_invalid")
		}
		job.config = cfg
		rawToken := make([]byte, 24)
		if _, err = rand.Read(rawToken); err != nil {
			return err
		}
		job.lease = hex.EncodeToString(rawToken)
		state = "querying"
		if job.action == "submit" {
			state = "submitting"
		}
		_, err = tx.Exec(ctx, `UPDATE certification_provider_jobs SET status=$2,lease_token=$3,lease_until=clock_timestamp()+interval '1 minute',
 submit_attempts=submit_attempts+CASE WHEN $2='submitting' THEN 1 ELSE 0 END,
 query_attempts=query_attempts+CASE WHEN $2='querying' THEN 1 ELSE 0 END,
 submit_started_at=CASE WHEN $2='submitting' THEN clock_timestamp() ELSE submit_started_at END,updated_at=now() WHERE id=$1`, job.id, state, job.lease)
		if err != nil {
			return err
		}
		if job.action == "query" {
			job.queryAttempts++
		}
		if _, err = tx.Exec(ctx, `UPDATE certification_submissions SET status='processing' WHERE id=$1`, job.submissionID); err != nil {
			return err
		}
		out = &job
		return nil
	})
	return out, true, err
}

func certificationCanQuery(job certificationJob, subject string) bool {
	return certificationCapability(job.descriptor, "query_by_request_id") || subject != "" && certificationCapability(job.descriptor, "query")
}

func (s *Store) markCertificationNeedsReviewTx(ctx context.Context, tx pgx.Tx, job certificationJob, code string) error {
	if _, err := tx.Exec(ctx, `UPDATE certification_provider_jobs SET status='needs_review',code=$2,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, job.id, code); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE certification_submissions SET status='failed' WHERE id=$1`, job.submissionID); err != nil {
		return err
	}
	return Audit(ctx, tx, 0, "certification.provider_needs_review", "certification_submission", job.submissionID, map[string]any{"code": code})
}

func (s *Store) certificationJobNeedsReview(ctx context.Context, job certificationJob, code string) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var version int64
		var status string
		if err := tx.QueryRow(ctx, `SELECT version,status FROM certification_profiles WHERE id=$1 FOR UPDATE`, job.profileID).Scan(&version, &status); err != nil {
			return err
		}
		var active bool
		var lockedID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM certification_provider_jobs WHERE id=$1 FOR UPDATE`, job.id).Scan(&lockedID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(lease_token=$2 AND status IN ('submitting','querying') AND lease_until>clock_timestamp(),false) FROM certification_provider_jobs WHERE id=$1`, job.id, job.lease).Scan(&active); err != nil {
			return err
		}
		if !active || version != job.profileVersion || status != "pending" {
			return nil
		}
		return s.markCertificationNeedsReviewTx(ctx, tx, job, code)
	})
}

func certificationResultStatus(result certification.Result, callErr error) (string, string) {
	if callErr != nil {
		return "failed", "provider_outcome_unknown"
	}
	if len(result.ProviderSubject) > 512 || strings.ContainsAny(result.ProviderSubject, "\x00\r\n") {
		return "failed", "provider_invalid_response"
	}
	switch result.Status {
	case "processing", "approved", "rejected", "failed":
		return result.Status, ""
	default:
		return "failed", "provider_invalid_response"
	}
}

func (s *Store) finishCertificationJob(ctx context.Context, job certificationJob, result certification.Result, callErr error) error {
	status, code := certificationResultStatus(result, callErr)
	// Provider messages may contain identity documents or credentials. Their
	// only persisted form is ciphertext; logs, audit and public reasons use codes.
	if len(result.Message) > 16384 {
		result.Message = result.Message[:16384]
	}
	sealed, aad, err := sealCertification(s.ProvisionEncryptionKey, fmt.Sprintf("result:%d:%s", job.submissionID, job.lease), result)
	if err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var version int64
		var profileStatus string
		if err := tx.QueryRow(ctx, `SELECT version,status FROM certification_profiles WHERE id=$1 FOR UPDATE`, job.profileID).Scan(&version, &profileStatus); err != nil {
			return err
		}
		var active bool
		var lockedID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM certification_provider_jobs WHERE id=$1 FOR UPDATE`, job.id).Scan(&lockedID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(lease_token=$2 AND status IN ('submitting','querying') AND lease_until>clock_timestamp(),false) FROM certification_provider_jobs WHERE id=$1`, job.id, job.lease).Scan(&active); err != nil {
			return err
		}
		ignore := ""
		if version != job.profileVersion || profileStatus != "pending" {
			ignore = "application_superseded"
		} else if !active {
			ignore = "lease_lost"
		}
		subject := result.ProviderSubject
		if len(subject) > 512 || strings.ContainsAny(subject, "\x00\r\n") {
			subject = ""
		}
		if ignore == "" && job.subject != "" && subject != "" && subject != job.subject {
			code = "provider_subject_mismatch"
		}
		tag, err := tx.Exec(ctx, `INSERT INTO certification_provider_results(submission_id,provider_id,status,provider_subject,result_encrypted,result_aad,error_message,applied,ignore_reason,job_lease_token)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(submission_id,job_lease_token) WHERE job_lease_token<>'' DO NOTHING`, job.submissionID, job.provider, status, subject, sealed, aad, code, ignore == "" && code == "", ignore, job.lease)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if ignore != "" {
			return Audit(ctx, tx, 0, "certification.provider_result_ignored", "certification_submission", job.submissionID, map[string]any{"code": ignore})
		}
		if code == "provider_subject_mismatch" {
			return s.markCertificationNeedsReviewTx(ctx, tx, job, code)
		}
		if code != "" {
			if job.queryAttempts >= 5 || !certificationCanQuery(job, job.subject) {
				return s.markCertificationNeedsReviewTx(ctx, tx, job, "query_unavailable")
			}
			return s.waitCertificationQueryTx(ctx, tx, job, "uncertain", code)
		}
		if subject == "" {
			subject = job.subject
		}
		if status == "failed" {
			return s.markCertificationNeedsReviewTx(ctx, tx, job, "provider_failed")
		}
		if status == "processing" {
			if _, err := tx.Exec(ctx, `UPDATE certification_submissions SET status='processing',provider_subject=$2 WHERE id=$1`, job.submissionID, subject); err != nil {
				return err
			}
			if job.queryAttempts >= 5 || !certificationCanQuery(job, subject) {
				return s.markCertificationNeedsReviewTx(ctx, tx, job, "query_unavailable")
			}
			return s.waitCertificationQueryTx(ctx, tx, job, "waiting", "provider_processing")
		}
		reason := ""
		if status == "rejected" {
			reason = "实名认证未通过，请核对资料或联系支持"
		}
		if _, err := tx.Exec(ctx, `UPDATE certification_profiles SET status=$2,provider_subject=$3,rejection_reason=$4,version=version+1,updated_at=now() WHERE id=$1`, job.profileID, status, subject, reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE certification_submissions SET status=$2,provider_subject=$3,reviewed_at=now(),reviewed_by=NULL,review_note='' WHERE id=$1`, job.submissionID, status, subject); err != nil {
			return err
		}
		if status == "approved" {
			if err := s.applyCertifiedNameTx(ctx, tx, 0, job.userID, job.profileID, &job.submissionID, job.payload, &job.payloadAAD); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE certification_provider_jobs SET status='done',code=$2,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, job.id, "provider_"+status); err != nil {
			return err
		}
		return Audit(ctx, tx, 0, "certification.provider_completed", "certification_submission", job.submissionID, map[string]any{"decision": status, "provider_id": job.provider})
	})
}

func (s *Store) waitCertificationQueryTx(ctx context.Context, tx pgx.Tx, job certificationJob, state, code string) error {
	_, err := tx.Exec(ctx, `UPDATE certification_provider_jobs SET status=$2,code=$3,next_run_at=clock_timestamp()+interval '30 seconds',lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, job.id, state, code)
	return err
}
