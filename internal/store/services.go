package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

type ProvisionJob struct {
	ID        int64
	ServiceID int64
	Action    string
	Attempts  int
}

type ServiceActionResult struct {
	JobID     int64  `json:"job_id"`
	ServiceID int64  `json:"service_id"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Replayed  bool   `json:"replayed"`
}

func (s *Store) EnqueueServiceAction(ctx context.Context, actorID, serviceID int64, action, key string, admin bool) (ServiceActionResult, error) {
	if action == "reinstall" {
		return ServiceActionResult{}, conflict("reinstall_confirmation_required", "请使用系统重装确认流程")
	}
	if provision.IsStorageAction(action) {
		return ServiceActionResult{}, conflict("storage_confirmation_required", "请使用快照或备份确认流程")
	}
	if provision.IsNATAction(action) {
		return ServiceActionResult{}, conflict("nat_confirmation_required", "请使用 NAT 确认流程")
	}
	if provision.IsCDROMAction(action) {
		return ServiceActionResult{}, conflict("cdrom_confirmation_required", "请使用光驱确认流程")
	}
	return s.enqueueServiceAction(ctx, actorID, serviceID, action, key, admin, "", 0, nil, nil)
}

func (s *Store) ResetServicePassword(ctx context.Context, actorID, serviceID int64, password, key string, admin bool) (ServiceActionResult, error) {
	return s.enqueueServiceAction(ctx, actorID, serviceID, "reset_password", key, admin, password, 0, nil, nil)
}

func (s *Store) RescueService(ctx context.Context, actorID, serviceID int64, system int, tempPassword, key string, admin bool) (ServiceActionResult, error) {
	return s.enqueueServiceActionFor(ctx, actorID, serviceID, "rescue_system", key, admin, tempPassword, 0, nil, nil, system)
}

func (s *Store) ReinstallService(ctx context.Context, actorID, serviceID, templateID int64, acknowledged bool, key string, admin bool) (ServiceActionResult, error) {
	if !acknowledged || templateID < 1 || templateID > 2147483647 {
		return ServiceActionResult{}, conflict("invalid_reinstall_request", "请选择镜像并确认数据清除风险")
	}
	return s.enqueueServiceAction(ctx, actorID, serviceID, "reinstall", key, admin, "", templateID, nil, nil)
}

func (s *Store) enqueueServiceAction(ctx context.Context, actorID, serviceID int64, action, key string, admin bool, password string, templateID int64, storage *provision.StorageRequest, cdrom *provision.CDROMRequest) (ServiceActionResult, error) {
	return s.enqueueServiceActionFor(ctx, actorID, serviceID, action, key, admin, password, templateID, storage, cdrom, 0)
}

func (s *Store) enqueueServiceActionFor(ctx context.Context, actorID, serviceID int64, action, key string, admin bool, password string, templateID int64, storage *provision.StorageRequest, cdrom *provision.CDROMRequest, system int) (ServiceActionResult, error) {
	var out ServiceActionResult
	nat, _ := ctx.Value(natRequestKey{}).(*provision.NATRequest)
	if actorID < 1 || serviceID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_service_action", "服务、动作或幂等键无效")
	}
	if action != "create" && action != "suspend" && action != "resume" && action != "terminate" && action != "sync" && action != "reset_password" && action != "reinstall" && action != "rescue_system" && action != "exit_rescue_system" && !provision.IsPowerAction(action) && !provision.IsStorageAction(action) && !provision.IsNATAction(action) && !provision.IsCDROMAction(action) {
		return out, conflict("unsupported_service_action", "不支持该服务动作")
	}
	if provision.IsNATAction(action) && (nat == nil || action != "nat_"+nat.Kind+"_"+nat.Operation || !provision.ValidNATRequest(*nat)) {
		return out, conflict("nat_confirmation_required", "请使用 NAT 确认流程")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "service", key); err != nil {
		return out, err
	}
	var ownerID int64
	var current, mode, power string
	var credentials []byte
	if admin {
		if action == "reset_password" || action == "reinstall" || action == "rescue_system" || action == "exit_rescue_system" || provision.IsStorageAction(action) || provision.IsNATAction(action) || provision.IsCDROMAction(action) {
			if err = requireDeliveryManage(ctx, tx, actorID); err != nil {
				return out, err
			}
		}
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
			return out, err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT user_id,status,provision_mode,coalesce(resource_info->>'power',''),credentials_encrypted FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&ownerID, &current, &mode, &power, &credentials); err != nil {
		return out, err
	}
	if !admin && ownerID != actorID {
		return out, pgx.ErrNoRows
	}
	if !modeSupportsServiceAction(mode, action) {
		return out, conflict("unsupported_supplier_action", "该上游适配器暂不支持此操作")
	}
	if storage != nil || nat != nil || cdrom != nil || (mode == "supplier" && action == "reinstall") {
		if err = lockStorageCredential(ctx, tx, actorID); err != nil {
			return out, err
		}
		var active bool
		if err = tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 FOR SHARE", actorID).Scan(&active); err != nil {
			return out, err
		}
		if !active {
			return out, conflict("permission_denied", "账户已停用")
		}
		if admin {
			if err = requireDeliveryManage(ctx, tx, actorID); err != nil {
				return out, err
			}
		}
	}
	if (action == "create" || (action == "sync" && mode != "nokvm")) && !admin {
		return out, conflict("forbidden_service_action", "只有管理员可以同步服务")
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	fingerprint := ""
	if action == "reset_password" {
		if !provision.ValidSystemPassword(password) {
			return out, conflict("invalid_system_password", "系统密码须为12至128位非空格可见ASCII字符")
		}
		if len(s.ProvisionEncryptionKey) != 32 {
			return out, conflict("credentials_unavailable", "交付加密密钥未配置")
		}
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		domain := "nokvm-password"
		if mode == "supplier" {
			domain = "supplier-password"
		}
		requestJSON, _ := json.Marshal([]any{domain, serviceID, key, password})
		mac.Write(requestJSON)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	if action == "rescue_system" {
		if mode != "supplier" {
			return out, conflict("unsupported_rescue_mode", "该服务不支持救援系统")
		}
		if system != 1 && system != 2 {
			return out, conflict("invalid_rescue_system", "救援系统类型无效")
		}
		if !provision.ValidSystemPassword(password) {
			return out, conflict("invalid_rescue_password", "临时密码须为12至128位非空格可见ASCII字符")
		}
		if len(s.ProvisionEncryptionKey) != 32 {
			return out, conflict("credentials_unavailable", "交付加密密钥未配置")
		}
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		requestJSON, _ := json.Marshal([]any{"supplier-rescue", serviceID, key, system, password})
		mac.Write(requestJSON)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	if action == "reinstall" {
		if len(s.ProvisionEncryptionKey) != 32 {
			return out, conflict("credentials_unavailable", "交付加密密钥未配置")
		}
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		requestJSON, _ := json.Marshal([]any{"nokvm-reinstall", serviceID, key, templateID})
		mac.Write(requestJSON)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	var storedFingerprint string
	if cdrom != nil {
		if len(s.ProvisionEncryptionKey) != 32 {
			return out, conflict("credentials_unavailable", "交付密钥未配置")
		}
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		raw, _ := json.Marshal([]any{"nokvm-cdrom", serviceID, key, cdrom.Operation, cdrom.ImageID})
		mac.Write(raw)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	if storage != nil {
		if len(s.ProvisionEncryptionKey) != 32 {
			return out, conflict("credentials_unavailable", "交付加密密钥未配置")
		}
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		raw, _ := json.Marshal([]any{"nokvm-storage", serviceID, key, storage.Kind, storage.Operation, storage.PointID})
		mac.Write(raw)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	if nat != nil {
		if len(s.ProvisionEncryptionKey) != 32 {
			return out, conflict("credentials_unavailable", "交付加密密钥未配置")
		}
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		raw, _ := json.Marshal([]any{"nokvm-nat", serviceID, key, nat.Kind, nat.Operation, nat.PointID, nat.Name, nat.Type, nat.Domain, nat.ExteriorPort, nat.InteriorPort})
		mac.Write(raw)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	err = tx.QueryRow(ctx, `SELECT id,status,request_fingerprint FROM provision_jobs WHERE idempotency_key=$1`, key).Scan(&out.JobID, &out.Status, &storedFingerprint)
	if err == nil {
		var existingServiceID int64
		var existingAction string
		if err = tx.QueryRow(ctx, "SELECT service_id,action FROM provision_jobs WHERE id=$1", out.JobID).Scan(&existingServiceID, &existingAction); err != nil {
			return out, err
		}
		if existingServiceID != serviceID || existingAction != action || !hmac.Equal([]byte(storedFingerprint), []byte(fingerprint)) {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同服务动作")
		}
		out.ServiceID = serviceID
		out.Action = action
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var pendingCredentials []byte
	requestPayload := []byte(`{}`)
	if cdrom != nil {
		if mode != "nokvm" || power == "processing" || power == "suspended" || power == "network_limited" {
			return out, conflict("cdrom_unavailable", "服务当前不允许光驱操作")
		}
		var upstream, profile string
		if err = tx.QueryRow(ctx, "SELECT upstream_id,coalesce(delivery_snapshot->>'profile_id','') FROM services WHERE id=$1", serviceID).Scan(&upstream, &profile); err != nil {
			return out, err
		}
		if !provision.ValidResourceID(upstream) || profile == "" || s.Provisioners[profile] == nil {
			return out, conflict("invalid_cdrom_binding", "服务光驱绑定无效")
		}
		if cdrom.Prepared && (cdrom.UpstreamID != upstream || cdrom.ProfileID != profile) {
			return out, conflict("cdrom_resource_changed", "服务资源已变更，请刷新")
		}
		cdrom.UpstreamID, cdrom.ProfileID = upstream, profile
		var recent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND action IN ('cdrom_mount','cdrom_unmount') AND created_at>clock_timestamp()-interval '60 seconds')`, serviceID).Scan(&recent); err != nil {
			return out, err
		}
		if recent {
			return out, conflict("cdrom_cooldown", "操作频繁，请60秒后重试")
		}
		if !cdrom.Prepared {
			if !allowedServiceAction(current, action) && !(mode == "supplier" && action == "sync" && current == "provisioning") {
				return out, conflict("invalid_service_state", "服务状态不允许该操作")
			}
			if err = tx.Rollback(ctx); err != nil {
				return out, err
			}
			listing, readErr := s.ServiceCDROM(ctx, actorID, serviceID, admin)
			if readErr != nil {
				return out, readErr
			}
			if listing.binding.Profile != profile || listing.binding.Upstream != upstream {
				return out, conflict("cdrom_resource_changed", "服务资源已变更，请刷新")
			}
			if (cdrom.Operation == "mount" && listing.MountedID != "") || (cdrom.Operation == "unmount" && listing.MountedID != cdrom.ImageID) {
				return out, conflict("cdrom_state_changed", "光驱状态已变更，请刷新")
			}
			for _, image := range listing.Items {
				if image.ID == cdrom.ImageID {
					cdrom.Image = image
					break
				}
			}
			if cdrom.Image.ID == "" {
				return out, pgx.ErrNoRows
			}
			cdrom.MountedID, cdrom.Prepared = listing.MountedID, true
			return s.enqueueServiceAction(ctx, actorID, serviceID, action, key, admin, password, templateID, storage, cdrom)
		}
		requestPayload, _ = json.Marshal(cdrom)
	}
	if storage != nil {
		if mode != "nokvm" {
			return out, conflict("unsupported_storage_mode", "该服务不支持快照或备份")
		}
		if power == "processing" || power == "suspended" || power == "network_limited" {
			return out, conflict("power_processing", "资源当前状态不允许该操作")
		}
		var raw []byte
		var upstream string
		if err = tx.QueryRow(ctx, "SELECT delivery_snapshot,upstream_id FROM services WHERE id=$1", serviceID).Scan(&raw, &upstream); err != nil {
			return out, err
		}
		var delivery provision.Delivery
		if json.Unmarshal(raw, &delivery) != nil || delivery.Validate() != nil || !provision.ValidResourceID(upstream) {
			return out, conflict("invalid_storage_binding", "服务交付配置无效")
		}
		limit := delivery.Resources.Snapshots
		if storage.Kind == "backup" {
			limit = delivery.Resources.Backups
		}
		if storage.Prepared && (storage.ProfileID != delivery.ProfileID || storage.UpstreamID != upstream || storage.Limit != limit) {
			return out, conflict("storage_resource_changed", "服务资源已变更，请刷新")
		}
		storage.ProfileID = delivery.ProfileID
		storage.UpstreamID = upstream
		storage.Limit = limit
		if storage.Limit < 1 {
			return out, conflict("storage_disabled", "此服务未启用该项功能")
		}
		if s.Provisioners[storage.ProfileID] == nil {
			return out, conflict("supplier_unavailable", "供应商暂不可用")
		}
		if provision.IsStorageRestore(action) {
			if _, err = provision.Unseal(s.ProvisionEncryptionKey, serviceID, credentials); err != nil {
				return out, conflict("credentials_unavailable", "当前凭据不可用，请联系管理员")
			}
		}
		var recent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND (action LIKE 'snapshot_%' OR action LIKE 'backup_%') AND created_at>clock_timestamp()-interval '60 seconds')`, serviceID).Scan(&recent); err != nil {
			return out, err
		}
		if recent {
			return out, conflict("storage_cooldown", "操作频繁，请60秒后重试")
		}
		if !storage.Prepared {
			if !allowedServiceAction(current, action) && !(mode == "supplier" && action == "sync" && current == "provisioning") {
				return out, conflict("invalid_service_state", "服务状态不允许该操作")
			}
			if err = tx.Rollback(ctx); err != nil {
				return out, err
			}
			listing, readErr := s.ServiceStorage(ctx, actorID, serviceID, storage.Kind, admin)
			if readErr != nil {
				return out, readErr
			}
			if listing.binding.Profile != storage.ProfileID || listing.binding.Upstream != storage.UpstreamID || listing.Limit != storage.Limit {
				return out, conflict("storage_resource_changed", "服务资源已变更，请刷新")
			}
			if storage.Operation == "create" {
				if len(listing.Items) >= storage.Limit {
					return out, conflict("storage_quota_reached", "还原点数量已达到购买额度")
				}
			} else {
				for _, point := range listing.Items {
					if point.ID == storage.PointID {
						copy := point
						storage.Point = &copy
						break
					}
				}
				if storage.Point == nil {
					return out, pgx.ErrNoRows
				}
			}
			storage.Prepared = true
			return s.enqueueServiceAction(ctx, actorID, serviceID, action, key, admin, password, templateID, storage, cdrom)
		}
		requestPayload, _ = json.Marshal(storage)
	}
	if nat != nil {
		if mode != "nokvm" {
			return out, conflict("unsupported_nat_mode", "该服务不支持 NAT")
		}
		if power == "processing" || power == "suspended" || power == "network_limited" {
			return out, conflict("power_processing", "资源当前状态不允许该操作")
		}
		var raw []byte
		var upstream string
		if err = tx.QueryRow(ctx, "SELECT delivery_snapshot,upstream_id FROM services WHERE id=$1", serviceID).Scan(&raw, &upstream); err != nil {
			return out, err
		}
		var delivery provision.Delivery
		if json.Unmarshal(raw, &delivery) != nil || delivery.Validate() != nil || !provision.ValidResourceID(upstream) {
			return out, conflict("invalid_nat_binding", "服务交付配置无效")
		}
		limit := natLimit(delivery, nat.Kind)
		if nat.Prepared && (nat.ProfileID != delivery.ProfileID || nat.UpstreamID != upstream || nat.Limit != limit) {
			return out, conflict("nat_resource_changed", "服务资源已变更，请刷新")
		}
		nat.ProfileID, nat.UpstreamID, nat.Limit = delivery.ProfileID, upstream, limit
		if nat.Limit < 1 {
			return out, conflict("nat_disabled", "此服务未购买该 NAT 功能")
		}
		if s.Provisioners[nat.ProfileID] == nil {
			return out, conflict("supplier_unavailable", "供应商暂不可用")
		}
		var recent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND action IN ('nat_acl_create','nat_acl_delete','nat_web_create','nat_web_delete') AND created_at>clock_timestamp()-interval '60 seconds')`, serviceID).Scan(&recent); err != nil {
			return out, err
		}
		if recent {
			return out, conflict("nat_cooldown", "操作频繁，请60秒后重试")
		}
		if !nat.Prepared {
			if !allowedServiceAction(current, action) && !(mode == "supplier" && action == "sync" && current == "provisioning") {
				return out, conflict("invalid_service_state", "服务状态不允许该操作")
			}
			if err = tx.Rollback(ctx); err != nil {
				return out, err
			}
			listing, readErr := s.ServiceNAT(ctx, actorID, serviceID, nat.Kind, admin)
			if readErr != nil {
				return out, readErr
			}
			if listing.binding.Profile != nat.ProfileID || listing.binding.Upstream != nat.UpstreamID || listing.Limit != nat.Limit {
				return out, conflict("nat_resource_changed", "服务资源已变更，请刷新")
			}
			if nat.Operation == "create" {
				if len(listing.Items) >= nat.Limit {
					return out, conflict("nat_quota_reached", "NAT 数量已达到购买额度")
				}
			} else {
				for _, item := range listing.Items {
					if item.ID == nat.PointID {
						copy := item
						nat.Point = &copy
						break
					}
				}
				if nat.Point == nil {
					return out, pgx.ErrNoRows
				}
			}
			nat.Prepared = true
			return s.enqueueServiceAction(ctx, actorID, serviceID, action, key, admin, password, templateID, storage, cdrom)
		}
		requestPayload, _ = json.Marshal(nat)
	}
	if action == "reinstall" {
		if mode != "nokvm" && mode != "supplier" {
			return out, conflict("unsupported_reinstall_mode", "该服务不支持系统重装")
		}
		if power == "processing" || power == "suspended" || power == "network_limited" {
			return out, conflict("power_processing", "资源状态不允许重装，请先刷新状态")
		}
		var templates []provision.Template
		if mode == "supplier" {
			prepared, _ := ctx.Value(supplierReinstallKey{}).(*supplierTemplateListing)
			if prepared == nil {
				if err = tx.Rollback(ctx); err != nil {
					return out, err
				}
				listing, readErr := s.supplierTemplates(ctx, actorID, serviceID, admin)
				if readErr != nil {
					return out, readErr
				}
				ctx = context.WithValue(ctx, supplierReinstallKey{}, &listing)
				return s.enqueueServiceActionFor(ctx, actorID, serviceID, action, key, admin, password, templateID, storage, cdrom, system)
			}
			binding, bindingErr := s.supplierConsoleBindingTx(ctx, tx, actorID, serviceID, admin, true)
			if bindingErr != nil {
				return out, bindingErr
			}
			if binding != prepared.binding {
				return out, conflict("supplier_resource_changed", "服务供应商资源已变更，请刷新")
			}
			templates = prepared.templates
			err = nil
		} else {
			templates, err = serviceTemplates(ctx, tx, serviceID)
		}
		if err != nil {
			return out, err
		}
		found := false
		for _, template := range templates {
			if template.ID == templateID {
				requestPayload, _ = json.Marshal(template)
				found = true
				break
			}
		}
		if !found {
			return out, conflict("template_unavailable", "该镜像不在服务可重装目录中，请刷新后重选")
		}
		var recent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND action='reinstall' AND created_at>clock_timestamp()-interval '5 minutes')`, serviceID).Scan(&recent); err != nil {
			return out, err
		}
		if recent {
			return out, conflict("reinstall_cooldown", "系统重装频繁，请5分钟后重试")
		}
	}
	if action == "reset_password" || action == "rescue_system" {
		if action == "reset_password" && mode != "nokvm" && mode != "supplier" {
			return out, conflict("unsupported_password_mode", "该服务不支持系统密码重置")
		}
		if action == "rescue_system" && mode != "supplier" {
			return out, conflict("unsupported_rescue_mode", "该服务不支持救援系统")
		}
		if power == "processing" || power == "suspended" || power == "network_limited" {
			return out, conflict("power_processing", "资源状态不允许重置密码，请先刷新状态")
		}
		if mode == "nokvm" {
			if _, err = provision.Unseal(s.ProvisionEncryptionKey, serviceID, credentials); err != nil {
				return out, conflict("credentials_unavailable", "当前服务凭据不可用，请联系管理员")
			}
		}
		var recent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND action=$2 AND created_at>clock_timestamp()-interval '60 seconds')`, serviceID, action).Scan(&recent); err != nil {
			return out, err
		}
		if recent {
			return out, conflict("password_reset_cooldown", "密码重置频繁，请60秒后重试")
		}
		pendingCredentials, err = provision.SealJobCredentials(s.ProvisionEncryptionKey, serviceID, key, provision.Credentials{Password: password})
		if err != nil {
			return out, err
		}
		if action == "rescue_system" {
			requestPayload, _ = json.Marshal(map[string]int{"system": system})
		}
	}
	if action == "exit_rescue_system" && mode != "supplier" {
		return out, conflict("unsupported_rescue_mode", "该服务不支持退出救援系统")
	}
	if provision.IsPowerAction(action) {
		if mode != "nokvm" && mode != "supplier" {
			return out, conflict("unsupported_power_mode", "该服务不支持电源控制")
		}
		if power == "processing" {
			return out, conflict("power_processing", "电源操作尚未完成，请先刷新资源状态")
		}
		var recent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1
 AND action IN ('power_on','power_off','reboot','force_off','force_reboot') AND created_at>clock_timestamp()-interval '30 seconds')`, serviceID).Scan(&recent); err != nil {
			return out, err
		}
		if recent {
			return out, conflict("power_action_cooldown", "电源操作频繁，请30秒后重试")
		}
	}
	if !allowedServiceAction(current, action) && !(mode == "supplier" && action == "sync" && current == "provisioning") {
		return out, conflict("invalid_service_state", "服务当前状态不允许执行该动作")
	}
	if err = requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return out, err
	}
	if action == "create" {
		var paid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services s JOIN invoices i ON i.order_id=s.order_id WHERE s.id=$1 AND i.status='paid')`, serviceID).Scan(&paid); err != nil {
			return out, err
		}
		if !paid {
			return out, conflict("invoice_not_paid", "账单尚未支付")
		}
	}
	var pendingAction bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs
WHERE service_id=$1 AND status IN ('pending','running','waiting_manual','waiting_verification'))`, serviceID).Scan(&pendingAction); err != nil {
		return out, err
	}
	if pendingAction {
		return out, conflict("service_action_pending", "相同服务动作正在处理中")
	}
	err = tx.QueryRow(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,request_credentials_encrypted,request_fingerprint,request_payload)
VALUES($1,$2,$3,$4,$5,$6) RETURNING id,status`, serviceID, action, key, pendingCredentials, fingerprint, requestPayload).Scan(&out.JobID, &out.Status)
	if err != nil {
		return out, err
	}
	out.ServiceID, out.Action = serviceID, action
	if err = Audit(ctx, tx, actorID, "service.action.queued", "service", serviceID, map[string]any{"action": action, "job_id": out.JobID}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// modeSupportsServiceAction mirrors the delivery-mode capability gate used by
// the manual service-action API. A provisioning scheduler must apply the same
// gate: queueing an action the delivery mode cannot perform only produces a job
// that keeps failing until its lease attempts are exhausted while the local
// service stays unchanged. Only the supplier mode restricts actions today; the
// supplier adapter advertises create/sync/power/reset/rescue/reinstall but not
// suspend, resume or terminate.
func modeSupportsServiceAction(mode, action string) bool {
	gate, restricted := serviceModeActionGates[mode]
	return !restricted || gate(action)
}

// Keep the manual API and scheduler candidate filtering on the same capability
// rules. Modes without a gate retain their existing unrestricted behavior.
var serviceModeActionGates = map[string]func(string) bool{
	"supplier": supplierSupportsServiceAction,
}

func unsupportedServiceActionModes(action string) []string {
	modes := make([]string, 0, len(serviceModeActionGates))
	for mode, gate := range serviceModeActionGates {
		if !gate(action) {
			modes = append(modes, mode)
		}
	}
	return modes
}

func supplierSupportsServiceAction(action string) bool {
	switch action {
	case "create", "sync", "reset_password", "rescue_system", "exit_rescue_system", "reinstall":
		return true
	}
	return provision.IsPowerAction(action)
}

func allowedServiceAction(status, action string) bool {
	if provision.IsStorageAction(action) || provision.IsNATAction(action) || provision.IsCDROMAction(action) {
		return status == "active"
	}
	switch action {
	case "create":
		return status == "queued" || status == "failed" || status == "provisioning"
	case "suspend":
		return status == "active"
	case "resume":
		return status == "suspended"
	case "terminate":
		return status == "active" || status == "suspended" || status == "failed" || status == "queued" || status == "provisioning"
	case "sync", "renew":
		return status == "active" || status == "suspended"
	case "upgrade":
		return status == "active"
	case "power_on", "power_off", "reboot", "force_off", "force_reboot":
		return status == "active"
	case "reset_password", "reinstall", "rescue_system", "exit_rescue_system":
		return status == "active"
	default:
		return false
	}
}

func (s *Store) ClaimProvisionJob(ctx context.Context) (*ProvisionJob, error) {
	var job ProvisionJob
	err := s.Pool.QueryRow(ctx, `WITH candidate AS (
SELECT id FROM provision_jobs WHERE status='pending' AND available_at<=now()
ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE provision_jobs j SET status='running',attempts=j.attempts+1,locked_at=now(),updated_at=now()
FROM candidate c WHERE j.id=c.id
RETURNING j.id,j.service_id,j.action,j.attempts`).Scan(&job.ID, &job.ServiceID, &job.Action, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pgx.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// RecoverStaleProvisionJobs finalizes jobs whose worker died after claiming but
// before finishing them. The decision must come from durable row state alone:
// process memory, the response and the supplier lease are all gone.
//
// A dispatched action may have written upstream state, so its result is unknown
// and stays conservative: the job enters waiting_verification and keeps the
// supplier account lease until an administrator reconciles it. A read-only
// action (sync) cannot have written anything, so there is no unknown result to
// reconcile: its account lease is released and the job is retried or failed like
// an undispatched one. Without the release the account stays locked forever,
// because only finishSupplierJob (never reached) and manual verification clear it.
func (s *Store) RecoverStaleProvisionJobs(ctx context.Context) error {
	readOnly := provision.ReadOnlyActions()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Collect the stale dispatched read-only jobs under their own row locks first.
	// Dispatch locks service -> job -> supplier account, so releasing the account
	// lease before the job update would invert that order and risk a deadlock.
	rows, err := tx.Query(ctx, `SELECT id FROM provision_jobs
WHERE status='running' AND locked_at<now()-interval '5 minutes'
  AND external_started_at IS NOT NULL AND action=ANY($1::text[])
ORDER BY id FOR UPDATE`, readOnly)
	if err != nil {
		return err
	}
	var release []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		release = append(release, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET
 status=CASE WHEN external_started_at IS NOT NULL AND NOT (action=ANY($1::text[])) THEN 'waiting_verification'
             WHEN attempts>=5 THEN 'failed' ELSE 'pending' END,
 available_at=now(),locked_at=NULL,updated_at=now(),
 external_started_at=CASE WHEN external_started_at IS NOT NULL AND action=ANY($1::text[]) THEN NULL ELSE external_started_at END,
 last_error=CASE WHEN external_started_at IS NOT NULL AND NOT (action=ANY($1::text[])) THEN '供应商操作租约已过期，请核验实际资源，禁止直接重试'
                 WHEN external_started_at IS NOT NULL THEN '只读任务租约已过期，将自动重试'
                 ELSE 'worker lease expired' END,
 completed_at=CASE WHEN (external_started_at IS NULL OR action=ANY($1::text[])) AND attempts>=5 THEN now() ELSE NULL END
WHERE status='running' AND locked_at<now()-interval '5 minutes'`, readOnly); err != nil {
		return err
	}
	// Only read-only dispatches release the leased account; a write action keeps
	// it so the unknown upstream result stays fenced until manual verification.
	if len(release) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE supplier_accounts SET running_job_id=NULL,updated_at=now()
WHERE running_job_id=ANY($1::bigint[])`, release); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ProcessProvisionJob(ctx context.Context, job ProvisionJob) error {
	var dispatchMode string
	if err := s.Pool.QueryRow(ctx, "SELECT s.provision_mode FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE j.id=$1", job.ID).Scan(&dispatchMode); err != nil {
		return err
	}
	if dispatchMode == "supplier" {
		return s.processSupplierJob(ctx, job)
	}
	if dispatchMode == "nokvm" && job.Action != "upgrade" {
		return s.processNOKVMJob(ctx, job)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	overdueDue, err := lockOverdueSuspensionInvoice(ctx, tx, job)
	if err != nil {
		return err
	}
	if err = lockConfigurationChangeBalance(ctx, tx, job.ID, job.ServiceID); err != nil {
		return err
	}
	var serviceStatus, mode, jobStatus string
	var serviceID int64
	if err = tx.QueryRow(ctx, "SELECT service_id FROM provision_jobs WHERE id=$1", job.ID).Scan(&serviceID); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT status,provision_mode FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&serviceStatus, &mode); err != nil {
		return err
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return err
	}
	var actual ProvisionJob
	var leaseValid bool
	if err = tx.QueryRow(ctx, `SELECT id,service_id,action,attempts,status,coalesce(locked_at>clock_timestamp()-interval '5 minutes',false) FROM provision_jobs WHERE id=$1 FOR UPDATE`, job.ID).Scan(&actual.ID, &actual.ServiceID, &actual.Action, &actual.Attempts, &jobStatus, &leaseValid); err != nil {
		return err
	}
	if jobStatus != "running" || actual.Attempts != job.Attempts || !leaseValid {
		return conflict("job_lease_lost", "任务租约已失效")
	}
	job = actual
	if !overdueDue {
		if err = cancelObsoleteOverdueSuspension(ctx, tx, job); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if !allowedServiceAction(serviceStatus, job.Action) {
		if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='failed',last_error=$2,completed_at=now(),updated_at=now() WHERE id=$1`, job.ID, fmt.Sprintf("服务状态 %s 不允许 %s", serviceStatus, job.Action)); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if mode == "manual" {
		if job.Action == "create" || job.Action == "terminate" {
			if _, err = tx.Exec(ctx, `UPDATE services SET status='provisioning',last_error='等待管理员确认实际资源',version=version+1,updated_at=now() WHERE id=$1`, job.ServiceID); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='waiting_manual',locked_at=NULL,updated_at=now() WHERE id=$1`, job.ID); err != nil {
			return err
		}
		if job.Action == "upgrade" {
			if _, err = tx.Exec(ctx, "UPDATE service_configuration_changes SET status='waiting_manual',updated_at=now() WHERE job_id=$1", job.ID); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	if mode == "nokvm" {
		var kind string
		if err = tx.QueryRow(ctx, "SELECT change_type FROM service_configuration_changes WHERE job_id=$1", job.ID).Scan(&kind); err != nil {
			return err
		}
		if job.Action != "upgrade" || kind != "billing_cycle" {
			return conflict("unsupported_supplier_upgrade", "NOKVM 暂不支持资源升级")
		}
	} else if mode != "simulation" {
		return conflict("unsupported_delivery_mode", "未知交付模式")
	}
	if err = applyLocalServiceAction(ctx, tx, job, serviceStatus); err != nil {
		return err
	}
	if job.Action == "create" {
		if err = s.QueueServiceProvisionSuccessNotifications(ctx, tx, job.ServiceID); err != nil {
			return err
		}
	}
	if job.Action == "suspend" && serviceStatus == "active" {
		if err = s.QueueServiceSuspendedNotifications(ctx, tx, job.ServiceID); err != nil {
			return err
		}
	}
	resultMode, auditAction := "simulation", "service.simulation.completed"
	if mode == "nokvm" {
		resultMode = "local_billing_cycle"
		auditAction = "service.billing_cycle.completed"
	}
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='succeeded',completed_at=now(),locked_at=NULL,updated_at=now(),last_error='',result=jsonb_build_object('mode',$2::text) WHERE id=$1`, job.ID, resultMode); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(action,entity_type,entity_id,payload) VALUES($4,'service',$1,jsonb_build_object('job_id',$2::bigint,'action',$3::text))`, fmt.Sprint(job.ServiceID), job.ID, job.Action, auditAction); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func applyLocalServiceAction(ctx context.Context, tx pgx.Tx, job ProvisionJob, current string) error {
	switch job.Action {
	case "create":
		if current == "cancelled" || current == "terminated" {
			return fmt.Errorf("服务已终止，不能开通")
		}
		if current != "queued" && current != "failed" {
			return fmt.Errorf("服务状态 %s 不能开通", current)
		}
		if _, err := tx.Exec(ctx, `UPDATE services SET status='active',upstream_id=CASE WHEN upstream_id='' THEN 'local-'||id::text ELSE upstream_id END,
username=CASE WHEN username='' THEN 'service-'||id::text ELSE username END,last_error='',version=version+1,updated_at=now(),
next_due_at=CASE billing_cycle WHEN 'monthly' THEN now()+interval '1 month' WHEN 'quarterly' THEN now()+interval '3 months' WHEN 'annually' THEN now()+interval '1 year' ELSE NULL END WHERE id=$1`, job.ServiceID); err != nil {
			return err
		}
	case "suspend":
		if current != "active" {
			return fmt.Errorf("服务状态 %s 不能暂停", current)
		}
		if _, err := tx.Exec(ctx, "UPDATE services SET status='suspended',version=version+1,updated_at=now() WHERE id=$1", job.ServiceID); err != nil {
			return err
		}
	case "resume":
		if current != "suspended" {
			return fmt.Errorf("服务状态 %s 不能恢复", current)
		}
		if _, err := tx.Exec(ctx, "UPDATE services SET status='active',version=version+1,updated_at=now() WHERE id=$1", job.ServiceID); err != nil {
			return err
		}
	case "terminate":
		if current == "terminated" || current == "cancelled" {
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE services SET status='terminated',version=version+1,updated_at=now() WHERE id=$1", job.ServiceID); err != nil {
			return err
		}
		return releaseTerminatedInventory(ctx, tx, job.ServiceID)
	case "sync":
		if _, err := tx.Exec(ctx, "UPDATE services SET updated_at=now() WHERE id=$1", job.ServiceID); err != nil {
			return err
		}
	case "upgrade":
		return completeConfigurationChange(ctx, tx, job)
	}
	return nil
}

func lockOperationKey(ctx context.Context, tx pgx.Tx, scope, key string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", scope+":"+key)
	return err
}

func releaseTerminatedInventory(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	_, err := tx.Exec(ctx, `WITH released AS (
UPDATE services s SET inventory_released=true WHERE s.id=$1 AND NOT inventory_released
AND s.status IN ('cancelled','terminated') RETURNING product_id)
UPDATE products p SET stock=stock+1,updated_at=now() FROM released r WHERE p.id=r.product_id AND stock IS NOT NULL`, serviceID)
	return err
}

type ManualCompletion struct {
	JobID      int64  `json:"job_id"`
	UpstreamID string `json:"upstream_id"`
	Username   string `json:"username"`
	Note       string `json:"note"`
}

func (s *Store) CompleteManualServiceJob(ctx context.Context, actorID, serviceID int64, input ManualCompletion) (ServiceActionResult, error) {
	out := ServiceActionResult{ServiceID: serviceID, JobID: input.JobID, Status: "succeeded"}
	input.UpstreamID = strings.TrimSpace(input.UpstreamID)
	input.Username = strings.TrimSpace(input.Username)
	input.Note = strings.TrimSpace(input.Note)
	if input.JobID < 1 || len(input.Note) < 3 || len(input.Note) > 2000 || len(input.UpstreamID) > 256 || len(input.Username) > 256 {
		return out, conflict("invalid_delivery", "请填写有效任务和交付凭据")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var mode, current string
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
		return out, err
	}
	if err = lockConfigurationChangeBalance(ctx, tx, input.JobID, serviceID); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "SELECT provision_mode,status FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&mode, &current); err != nil {
		return out, err
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	var job ProvisionJob
	var status string
	var saved []byte
	if err = tx.QueryRow(ctx, "SELECT id,service_id,action,status,result FROM provision_jobs WHERE id=$1 AND service_id=$2 FOR UPDATE", input.JobID, serviceID).Scan(&job.ID, &job.ServiceID, &job.Action, &status, &saved); err != nil {
		return out, err
	}
	out.Action = job.Action
	result, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	if status == "succeeded" {
		var previous ManualCompletion
		if json.Unmarshal(saved, &previous) != nil || previous != input {
			return out, conflict("delivery_conflict", "任务已使用不同的交付凭据完成")
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if mode != "manual" || status != "waiting_manual" || !allowedServiceAction(current, job.Action) {
		return out, conflict("invalid_delivery_state", "任务当前不可人工确认")
	}
	if job.Action == "create" {
		if input.UpstreamID == "" {
			return out, conflict("resource_required", "开通确认必须填写实际资源标识")
		}
		if _, err = tx.Exec(ctx, `UPDATE services SET status='active',upstream_id=$2,username=$3,last_error='',version=version+1,updated_at=now(),
next_due_at=CASE billing_cycle WHEN 'monthly' THEN now()+interval '1 month' WHEN 'quarterly' THEN now()+interval '3 months' WHEN 'annually' THEN now()+interval '1 year' ELSE NULL END WHERE id=$1`, serviceID, input.UpstreamID, input.Username); err != nil {
			return out, err
		}
	} else if err = applyLocalServiceAction(ctx, tx, job, current); err != nil {
		return out, err
	}
	if job.Action == "create" {
		if err = s.QueueServiceProvisionSuccessNotifications(ctx, tx, serviceID); err != nil {
			return out, err
		}
	}
	if job.Action == "suspend" && current == "active" {
		if err = s.QueueServiceSuspendedNotifications(ctx, tx, serviceID); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET status='succeeded',result=$2,completed_at=now(),updated_at=now(),last_error='' WHERE id=$1", job.ID, result); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "service.manual.completed", "service", serviceID, input); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
