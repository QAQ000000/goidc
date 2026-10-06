package store

import (
	"context"
	"encoding/json"
	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
	"time"
)

type storageCredentialKey struct{}
type storageCredential struct {
	Hash   string
	APIKey bool
}

// WithStorageCredential binds an HTTP request to its authenticated credential.
// The credential is checked and locked inside the enqueue transaction, after
// the supplier preflight; revocation therefore cannot slip through that read.
func WithStorageCredential(ctx context.Context, hash string, apiKey bool) context.Context {
	return context.WithValue(ctx, storageCredentialKey{}, storageCredential{hash, apiKey})
}

func lockStorageCredential(ctx context.Context, tx pgx.Tx, actorID int64) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok { // Internal store callers authorize their actor separately.
		return nil
	}
	query := `SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`
	if credential.APIKey {
		query = `SELECT user_id FROM api_keys WHERE secret_hash=$1 AND user_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()) FOR SHARE`
	}
	var id int64
	if err := tx.QueryRow(ctx, query, credential.Hash, actorID).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return conflict("unauthorized", "登录已失效，请重新登录")
		}
		return err
	}
	return nil
}

// LockFileCredential keeps revocation serialized with private file transactions.
func LockFileCredential(ctx context.Context, tx pgx.Tx, actorID int64) error {
	return lockStorageCredential(ctx, tx, actorID)
}

func (s *Store) StorageServiceAction(ctx context.Context, actorID, serviceID int64, kind, operation, pointID, key string, ack bool, admin bool) (ServiceActionResult, error) {
	action := kind + "_" + operation
	if !provision.IsStorageAction(action) || !ack || (operation == "create" && pointID != "") || (operation != "create" && !provision.ValidResourceID(pointID)) {
		return ServiceActionResult{}, conflict("invalid_storage_request", "请选择有效操作与还原点并确认风险")
	}
	input := &provision.StorageRequest{Kind: kind, Operation: operation, PointID: pointID}
	return s.enqueueServiceAction(ctx, actorID, serviceID, action, key, admin, "", 0, input, nil)
}

func pendingStorageRequest(ctx context.Context, tx pgx.Tx, job ProvisionJob) (provision.StorageRequest, error) {
	var raw []byte
	var in provision.StorageRequest
	if err := tx.QueryRow(ctx, "SELECT request_payload FROM provision_jobs WHERE id=$1 AND service_id=$2", job.ID, job.ServiceID).Scan(&raw); err != nil {
		return in, err
	}
	if json.Unmarshal(raw, &in) != nil || in.Kind+"_"+in.Operation != job.Action || !provision.IsStorageAction(job.Action) || !provision.ValidResourceID(in.UpstreamID) || in.ProfileID == "" || in.Limit < 1 || in.Limit > 100000000 || (in.Operation == "create" && (in.PointID != "" || in.Point != nil)) || (in.Operation != "create" && (!provision.ValidResourceID(in.PointID) || in.Point == nil || in.Point.ID != in.PointID || in.Point.Name == "" || in.Point.CreatedAt.Year() < 2000 || in.Point.CreatedAt.Year() > 2200)) {
		return in, conflict("invalid_storage_request", "快照或备份任务无效")
	}
	return in, nil
}

type StorageListing struct {
	Kind      string                   `json:"kind"`
	Limit     int                      `json:"limit"`
	Items     []provision.StoragePoint `json:"items"`
	FetchedAt time.Time                `json:"fetched_at"`
	binding   monitoringBinding
}

func (s *Store) ServiceStorage(ctx context.Context, actorID, serviceID int64, kind string, admin bool) (StorageListing, error) {
	out := StorageListing{Kind: kind, Items: []provision.StoragePoint{}}
	if kind != "snapshot" && kind != "backup" {
		return out, conflict("invalid_storage_kind", "请选择快照或备份")
	}
	binding, err := s.monitoringBinding(ctx, actorID, serviceID, admin)
	if err != nil {
		return out, err
	}
	var raw []byte
	if err = s.Pool.QueryRow(ctx, "SELECT delivery_snapshot FROM services WHERE id=$1", serviceID).Scan(&raw); err != nil {
		return out, err
	}
	var delivery provision.Delivery
	if json.Unmarshal(raw, &delivery) != nil || delivery.Validate() != nil || delivery.ProfileID != binding.Profile {
		return out, conflict("invalid_storage_binding", "服务交付配置无效")
	}
	out.Limit = delivery.Resources.Snapshots
	if kind == "backup" {
		out.Limit = delivery.Resources.Backups
	}
	if out.Limit > 0 {
		callCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		out.Items, err = s.Provisioners[binding.Profile].ListStorage(callCtx, kind, binding.Upstream)
		cancel()
		if err != nil {
			return StorageListing{}, conflict("supplier_storage_failed", "还原点暂不可用，请稍后重试")
		}
	}
	current, err := s.monitoringBinding(ctx, actorID, serviceID, admin)
	if err != nil {
		return StorageListing{}, err
	}
	var after []byte
	if err = s.Pool.QueryRow(ctx, "SELECT delivery_snapshot FROM services WHERE id=$1", serviceID).Scan(&after); err != nil {
		return StorageListing{}, err
	}
	if current != binding || string(raw) != string(after) {
		return StorageListing{}, conflict("storage_resource_changed", "服务资源已变更，请刷新")
	}
	out.FetchedAt = time.Now().UTC()
	out.binding = binding
	return out, nil
}

func (s *Store) applyStorageVerification(ctx context.Context, tx pgx.Tx, job ProvisionJob, current string, in DeliveryVerification) error {
	if !allowedServiceAction(current, job.Action) {
		return conflict("invalid_delivery_state", "服务状态不允许完成该操作")
	}
	request, err := pendingStorageRequest(ctx, tx, job)
	if err != nil {
		return err
	}
	var sealed []byte
	var upstream, profile string
	if err = tx.QueryRow(ctx, "SELECT upstream_id,coalesce(delivery_snapshot->>'profile_id',''),credentials_encrypted FROM services WHERE id=$1", job.ServiceID).Scan(&upstream, &profile, &sealed); err != nil {
		return err
	}
	if upstream != request.UpstreamID || profile != request.ProfileID {
		return conflict("storage_resource_changed", "任务资源绑定已变更")
	}
	if !provision.IsStorageRestore(job.Action) {
		return nil
	}
	if len(s.ProvisionEncryptionKey) != 32 {
		return conflict("credentials_unavailable", "交付密钥未配置")
	}
	old, err := provision.Unseal(s.ProvisionEncryptionKey, job.ServiceID, sealed)
	if err != nil {
		return conflict("credentials_unavailable", "原服务凭据不可用")
	}
	old.Password = in.Password
	sealed, err = provision.Seal(s.ProvisionEncryptionKey, job.ServiceID, old)
	if err != nil {
		return err
	}
	system, _ := json.Marshal(map[string]string{"name": in.OSName, "login": in.Username})
	_, err = tx.Exec(ctx, `UPDATE services SET username=$2,installed_os=$3,credentials_encrypted=$4,resource_info=resource_info||jsonb_build_object('power','processing'),last_error='',version=version+1,updated_at=now() WHERE id=$1`, job.ServiceID, in.Username, system, sealed)
	return err
}
