package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

type CDROMListing struct {
	Items     []provision.CDROMImage `json:"items"`
	MountedID string                 `json:"mounted_id"`
	FetchedAt time.Time              `json:"fetched_at"`
	binding   monitoringBinding
}

func (s *Store) ServiceCDROM(ctx context.Context, actorID, serviceID int64, admin bool) (CDROMListing, error) {
	out := CDROMListing{Items: []provision.CDROMImage{}}
	binding, err := s.monitoringBinding(ctx, actorID, serviceID, admin)
	if err != nil {
		return out, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	listing, err := s.Provisioners[binding.Profile].ListCDROM(callCtx, binding.Upstream)
	cancel()
	if err != nil {
		return out, conflict("supplier_cdrom_failed", "光驱目录暂不可用，请稍后重试")
	}
	current, err := s.monitoringBinding(ctx, actorID, serviceID, admin)
	if err != nil {
		return out, err
	}
	if current != binding {
		return out, conflict("cdrom_resource_changed", "服务资源已变更，请刷新")
	}
	out.Items, out.MountedID, out.FetchedAt, out.binding = listing.Items, listing.MountedID, time.Now().UTC(), binding
	return out, nil
}

func (s *Store) CDROMServiceAction(ctx context.Context, actorID, serviceID int64, operation, imageID, key string, ack, admin bool) (ServiceActionResult, error) {
	if (operation != "mount" && operation != "unmount") || !provision.ValidResourceID(imageID) || !ack {
		return ServiceActionResult{}, conflict("invalid_cdrom_request", "请选择镜像并确认光驱及启动顺序变更风险")
	}
	return s.enqueueServiceAction(ctx, actorID, serviceID, "cdrom_"+operation, key, admin, "", 0, nil, &provision.CDROMRequest{Operation: operation, ImageID: imageID})
}

func pendingCDROMRequest(ctx context.Context, tx pgx.Tx, job ProvisionJob) (provision.CDROMRequest, error) {
	var in provision.CDROMRequest
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT request_payload FROM provision_jobs WHERE id=$1 AND service_id=$2", job.ID, job.ServiceID).Scan(&raw); err != nil {
		return in, err
	}
	if json.Unmarshal(raw, &in) != nil || "cdrom_"+in.Operation != job.Action || !provision.IsCDROMAction(job.Action) || !provision.ValidResourceID(in.UpstreamID) || !provision.ValidResourceID(in.ImageID) || in.ProfileID == "" || in.Image.ID != in.ImageID || in.Image.Name == "" || (in.MountedID != "" && !provision.ValidResourceID(in.MountedID)) {
		return in, conflict("invalid_cdrom_request", "光驱任务无效")
	}
	return in, nil
}

func (s *Store) applyCDROMVerification(ctx context.Context, tx pgx.Tx, job ProvisionJob, current string) error {
	if !allowedServiceAction(current, job.Action) {
		return conflict("invalid_delivery_state", "服务状态不允许完成该操作")
	}
	request, err := pendingCDROMRequest(ctx, tx, job)
	if err != nil {
		return err
	}
	var upstream, profile string
	if err = tx.QueryRow(ctx, "SELECT upstream_id,coalesce(delivery_snapshot->>'profile_id','') FROM services WHERE id=$1", job.ServiceID).Scan(&upstream, &profile); err != nil {
		return err
	}
	if upstream != request.UpstreamID || profile != request.ProfileID {
		return conflict("cdrom_resource_changed", "任务资源绑定已变更")
	}
	return nil
}
