package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

type certificationSelection struct {
	instance ChannelInstance
	config   module.Config
}

// ValidateCertificationSelectionTx checks an explicit routing selection without
// constructing a provider or contacting it. Only manual has an omitted-ID alias.
func (s *Store) ValidateCertificationSelectionTx(ctx context.Context, tx pgx.Tx, providerID, instanceID string) error {
	_, err := s.certificationInstanceTx(ctx, tx, providerID, instanceID)
	return err
}

func (s *Store) certificationInstanceTx(ctx context.Context, tx pgx.Tx, providerID, instanceID string) (certificationSelection, error) {
	providerID, instanceID = strings.TrimSpace(providerID), strings.TrimSpace(instanceID)
	if providerID == "manual" && instanceID == "" {
		instanceID = "manual-default"
	}
	if providerID == "" || instanceID == "" {
		return certificationSelection{}, conflict("certification_instance_required", "请选择实名认证渠道实例")
	}
	rec, err := scanChannelRecord(tx.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1 FOR SHARE", instanceID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return certificationSelection{}, conflict("certification_instance_unavailable", "实名认证渠道实例不可用")
		}
		return certificationSelection{}, err
	}
	if rec.item.Category != string(channel.Certification) || rec.item.ImplementationID != providerID {
		return certificationSelection{}, conflict("certification_instance_mismatch", "认证实现与渠道实例不匹配")
	}
	cfg, _, err := s.runtimeChannelRecord(rec, channel.Certification)
	if err != nil {
		return certificationSelection{}, err
	}
	desc, _ := channel.Lookup(channel.Certification, providerID)
	if providerID != "manual" && !certificationCapability(desc, "submit") {
		return certificationSelection{}, conflict("certification_submit_unavailable", "该认证模块不支持提交申请")
	}
	return certificationSelection{instance: rec.item, config: cfg}, nil
}

func certificationCapability(desc channel.Descriptor, capability string) bool {
	for _, item := range desc.Capabilities {
		if item == capability {
			return true
		}
	}
	return false
}

func (s *Store) certificationSelectionTx(ctx context.Context, tx pgx.Tx) (certificationSelection, error) {
	var enabled bool
	var provider, instance string
	err := tx.QueryRow(ctx, `SELECT enabled,default_provider_id,default_instance_id FROM certification_settings WHERE id=1 FOR SHARE`).Scan(&enabled, &provider, &instance)
	if err == pgx.ErrNoRows || err == nil && !enabled {
		return certificationSelection{}, conflict("certification_disabled", "实名认证已关闭")
	}
	if err != nil {
		return certificationSelection{}, err
	}
	return s.certificationInstanceTx(ctx, tx, provider, instance)
}

func certificationSnapshotAAD(submissionID int64, instanceID string, version int64) string {
	return fmt.Sprintf("zjmfgo:certification-config:v1:%d:%s:%d", submissionID, instanceID, version)
}

// The encrypted snapshot contains only serializable factory inputs. It cannot
// silently fall back to the current instance credentials during later queries.
func (s *Store) sealCertificationConfig(submissionID int64, selection certificationSelection) ([]byte, string, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return nil, "", conflict("certification_unconfigured", "认证加密尚未配置")
	}
	raw, err := json.Marshal(selection.config)
	if err != nil {
		return nil, "", err
	}
	aad := certificationSnapshotAAD(submissionID, selection.instance.ID, selection.instance.Version)
	sealed, err := secure.Seal(s.ProvisionEncryptionKey, []byte(aad), raw)
	return sealed, aad, err
}

func (s *Store) openCertificationConfig(submissionID int64, instance string, version int64, aad string, sealed []byte) (module.Config, error) {
	var cfg module.Config
	if len(s.ProvisionEncryptionKey) != 32 || aad != certificationSnapshotAAD(submissionID, instance, version) || len(sealed) == 0 {
		return cfg, conflict("certification_snapshot_unavailable", "原认证配置不可用，请人工核验")
	}
	raw, err := secure.Open(s.ProvisionEncryptionKey, []byte(aad), sealed)
	if err != nil || json.Unmarshal(raw, &cfg) != nil {
		return module.Config{}, conflict("certification_snapshot_unavailable", "原认证配置不可用，请人工核验")
	}
	return cfg, nil
}

func (s *Store) createCertificationSubmissionTx(ctx context.Context, tx pgx.Tx, userID, profileID, version int64, kind string, payload map[string]any, selection certificationSelection) (map[string]any, error) {
	var submissionID int64
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('certification_submissions','id'))`).Scan(&submissionID); err != nil {
		return nil, err
	}
	sealed, aad, err := sealCertification(s.ProvisionEncryptionKey, fmt.Sprint(profileID), payload)
	if err != nil {
		return nil, conflict("certification_unconfigured", "认证加密尚未配置")
	}
	snapshot, snapshotAAD, err := s.sealCertificationConfig(submissionID, selection)
	if err != nil {
		return nil, err
	}
	requestID := fmt.Sprintf("certification-%d", submissionID)
	_, err = tx.Exec(ctx, `INSERT INTO certification_submissions(id,profile_id,user_id,certification_type,provider_id,status,payload_encrypted,payload_aad,channel_instance_id,channel_instance_version,channel_schema_version,profile_version,request_id,config_snapshot_encrypted,config_snapshot_aad)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,'pending',$6,$7,$8,$9,$10,$11,$12,$13,$14)`, submissionID, profileID, userID, kind, selection.instance.ImplementationID, sealed, aad, selection.instance.ID, selection.instance.Version, selection.instance.SchemaVersion, version, requestID, snapshot, snapshotAAD)
	if err != nil {
		return nil, err
	}
	// Profile locks serialize both a new application and an administrator's
	// decision against worker results. Clearing the lease fences in-flight work.
	if err = cancelCertificationJobsTx(ctx, tx, profileID, "application_superseded"); err != nil {
		return nil, err
	}
	state := "needs_review"
	jobStatus, jobCode := "needs_review", "manual_review"
	if selection.instance.ImplementationID != "manual" {
		state = "queued"
		jobStatus, jobCode = "queued", ""
	}
	if _, err = tx.Exec(ctx, `INSERT INTO certification_provider_jobs(submission_id,status,code) VALUES($1,$2,$3)`, submissionID, jobStatus, jobCode); err != nil {
		return nil, err
	}
	return map[string]any{"id": submissionID, "profile_id": profileID, "status": "pending", "version": version, "processing_state": state}, nil
}

func cancelCertificationJobsTx(ctx context.Context, tx pgx.Tx, profileID int64, code string) error {
	_, err := tx.Exec(ctx, `UPDATE certification_provider_jobs j SET status='cancelled',code=$2,lease_token=NULL,lease_until=NULL,updated_at=now()
 FROM certification_submissions sub WHERE j.submission_id=sub.id AND sub.profile_id=$1
 AND j.status NOT IN ('done','cancelled')`, profileID, code)
	return err
}

func certificationProcessingState(status, provider, jobStatus string) string {
	if status == "approved" || status == "rejected" {
		return "finished"
	}
	if status == "unverified" {
		return "finished"
	}
	if provider == "manual" || jobStatus == "needs_review" || jobStatus == "cancelled" || jobStatus == "" {
		return "needs_review"
	}
	if jobStatus == "queued" {
		return "queued"
	}
	return "processing"
}
