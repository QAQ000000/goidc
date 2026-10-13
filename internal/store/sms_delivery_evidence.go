package store

import (
	"context"
	"fmt"
)

// SMSDeliveryEvidence contains the untouched HTTP response body (up to 64 KiB).
// Outcome is a fixed diagnostic label, never a gateway message or Go error.
type SMSDeliveryEvidence struct {
	Provider   string
	HTTPStatus int
	Outcome    string
	Response   []byte
	Truncated  bool
}

func smsEvidenceDomain(job SMSJob) string {
	return fmt.Sprintf("response:%d:%d:%s", job.ID, job.Attempts, job.LockedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"))
}

// SaveSMSDeliveryEvidence persists evidence before completing a delivery. The
// lease timestamp distinguishes manual retries that reset the attempt count.
// Raw bodies must not be passed to logs, audit events or ordinary admin DTOs.
func (s *Store) SaveSMSDeliveryEvidence(ctx context.Context, job SMSJob, evidence SMSDeliveryEvidence) error {
	if len(evidence.Response) > 64<<10 || job.ID <= 0 || job.Attempts <= 0 || job.LockedAt.IsZero() {
		return fmt.Errorf("invalid SMS delivery evidence")
	}
	switch evidence.Outcome {
	case "accepted", "configuration_rejected", "transport_error", "read_error", "response_too_large", "http_rejected", "invalid_json", "business_rejected", "provider_failed":
	default:
		return fmt.Errorf("invalid SMS evidence outcome")
	}
	sealed, err := smsCrypt(s.ProvisionEncryptionKey, evidence.Response, smsEvidenceDomain(job), true)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO sms_delivery_evidence(outbox_id,attempts,locked_at,provider,http_status,outcome,response_encrypted,response_truncated) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, job.ID, job.Attempts, job.LockedAt, evidence.Provider, evidence.HTTPStatus, evidence.Outcome, sealed, evidence.Truncated)
	return err
}
