package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

type natRequestKey struct{}

func (s *Store) NATServiceAction(ctx context.Context, actorID, serviceID int64, request provision.NATRequest, key string, ack, admin bool) (ServiceActionResult, error) {
	if !ack || !provision.ValidNATRequest(request) {
		return ServiceActionResult{}, conflict("invalid_nat_request", "请选择有效 NAT 操作并确认风险")
	}
	action := "nat_" + request.Kind + "_" + request.Operation
	ctx = context.WithValue(ctx, natRequestKey{}, &request)
	return s.enqueueServiceAction(ctx, actorID, serviceID, action, key, admin, "", 0, nil)
}

func pendingNATRequest(ctx context.Context, tx pgx.Tx, job ProvisionJob) (provision.NATRequest, error) {
	var raw []byte
	var r provision.NATRequest
	if err := tx.QueryRow(ctx, "SELECT request_payload FROM provision_jobs WHERE id=$1 AND service_id=$2", job.ID, job.ServiceID).Scan(&raw); err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r) != nil || !provision.IsNATAction(job.Action) || job.Action != "nat_"+r.Kind+"_"+r.Operation || !provision.ValidNATRequest(r) || !provision.ValidResourceID(r.UpstreamID) || r.ProfileID == "" || r.Limit < 1 || r.Limit > 100000000 || (r.Operation == "create" && r.Point != nil) || (r.Operation == "delete" && (r.Point == nil || r.Point.ID != r.PointID)) {
		return r, conflict("invalid_nat_request", "NAT 任务载荷无效")
	}
	return r, nil
}

type NATListing struct {
	Kind      string              `json:"kind"`
	Limit     int                 `json:"limit"`
	Items     []provision.NATItem `json:"items"`
	FetchedAt time.Time           `json:"fetched_at"`
	binding   monitoringBinding
}

func (s *Store) ServiceNAT(ctx context.Context, actorID, serviceID int64, kind string, admin bool) (NATListing, error) {
	out := NATListing{Kind: kind, Items: []provision.NATItem{}}
	if kind != "acl" && kind != "web" {
		return out, conflict("invalid_nat_kind", "请选择端口转发或 NAT 建站")
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
		return out, conflict("invalid_nat_binding", "服务交付配置无效")
	}
	out.Limit = delivery.Resources.NATACL
	if kind == "web" {
		out.Limit = delivery.Resources.NATWeb
	}
	if out.Limit > 0 {
		callCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		out.Items, err = s.Provisioners[binding.Profile].ListNAT(callCtx, kind, binding.Upstream)
		cancel()
		if err != nil {
			return NATListing{}, conflict("supplier_nat_failed", "NAT 列表暂不可用，请稍后重试")
		}
	}
	current, err := s.monitoringBinding(ctx, actorID, serviceID, admin)
	if err != nil {
		return NATListing{}, err
	}
	var after []byte
	if err = s.Pool.QueryRow(ctx, "SELECT delivery_snapshot FROM services WHERE id=$1", serviceID).Scan(&after); err != nil {
		return NATListing{}, err
	}
	if current != binding || string(raw) != string(after) {
		return NATListing{}, conflict("nat_resource_changed", "服务资源已变更，请刷新")
	}
	out.FetchedAt = time.Now().UTC()
	out.binding = binding
	return out, nil
}

func (s *Store) applyNATVerification(ctx context.Context, tx pgx.Tx, job ProvisionJob, current string) error {
	if !allowedServiceAction(current, job.Action) {
		return conflict("invalid_delivery_state", "服务状态不允许完成 NAT 操作")
	}
	r, err := pendingNATRequest(ctx, tx, job)
	if err != nil {
		return err
	}
	var upstream, profile string
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT upstream_id,coalesce(delivery_snapshot->>'profile_id',''),delivery_snapshot FROM services WHERE id=$1", job.ServiceID).Scan(&upstream, &profile, &raw); err != nil {
		return err
	}
	var delivery provision.Delivery
	if json.Unmarshal(raw, &delivery) != nil || delivery.Validate() != nil || upstream != r.UpstreamID || profile != r.ProfileID || natLimit(delivery, r.Kind) != r.Limit {
		return conflict("nat_resource_changed", "任务资源绑定已变更")
	}
	return nil
}

func natLimit(d provision.Delivery, kind string) int {
	if kind == "web" {
		return d.Resources.NATWeb
	}
	return d.Resources.NATACL
}
