package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type CancellationRequest struct {
	ID             int64      `json:"id"`
	ServiceID      int64      `json:"service_id"`
	UserID         int64      `json:"user_id"`
	Type           string     `json:"type"`
	Reason         string     `json:"reason"`
	Status         string     `json:"status"`
	ResolutionNote string     `json:"resolution_note"`
	CreatedAt      time.Time  `json:"created_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy     *int64     `json:"resolved_by,omitempty"`
	Replayed       bool       `json:"replayed"`
	JobID          *int64     `json:"job_id,omitempty"`
}

func (s *Store) RequestServiceCancellation(ctx context.Context, userID, serviceID int64, kind, reason, key string) (CancellationRequest, error) {
	var out CancellationRequest
	kind = strings.TrimSpace(strings.ToLower(kind))
	reason = strings.TrimSpace(reason)
	if userID < 1 || serviceID < 1 || (kind != "immediate" && kind != "end_of_billing") || len(reason) < 3 || len(reason) > 2000 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_cancellation", "请填写有效的取消类型、原因和幂等键")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "cancellation", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return out, err
	}
	var saved CancellationRequest
	err = tx.QueryRow(ctx, `SELECT id,service_id,user_id,type,reason,status,resolution_note,created_at,resolved_at,resolved_by
FROM service_cancellation_requests WHERE user_id=$1 AND idempotency_key=$2`, userID, key).
		Scan(&saved.ID, &saved.ServiceID, &saved.UserID, &saved.Type, &saved.Reason, &saved.Status, &saved.ResolutionNote, &saved.CreatedAt, &saved.ResolvedAt, &saved.ResolvedBy)
	if err == nil {
		if saved.ServiceID != serviceID || saved.Type != kind || saved.Reason != reason {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同的取消申请")
		}
		saved.Replayed = true
		return saved, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var ownerID int64
	var serviceStatus string
	if err = tx.QueryRow(ctx, "SELECT user_id,status FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&ownerID, &serviceStatus); err != nil {
		return out, err
	}
	if ownerID != userID {
		return out, pgx.ErrNoRows
	}
	var supplierMode bool
	if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM services WHERE id=$1", serviceID).Scan(&supplierMode); err != nil {
		return out, err
	}
	if supplierMode {
		return out, conflict("unsupported_supplier_cancellation", "该上游适配器暂未开放自动取消，请联系管理员处理")
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	if serviceStatus != "active" && serviceStatus != "suspended" {
		return out, conflict("invalid_cancellation_state", "只有已启用或已暂停的服务可以申请取消")
	}
	if err = requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return out, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$1 AND status='pending')", serviceID).Scan(&pending); err != nil {
		return out, err
	}
	if pending {
		return out, conflict("cancellation_pending", "该服务已有待处理的取消申请")
	}
	err = tx.QueryRow(ctx, `INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,idempotency_key)
VALUES($1,$2,$3,$4,$5)
RETURNING id,service_id,user_id,type,reason,status,resolution_note,created_at,resolved_at,resolved_by`, serviceID, userID, kind, reason, key).
		Scan(&out.ID, &out.ServiceID, &out.UserID, &out.Type, &out.Reason, &out.Status, &out.ResolutionNote, &out.CreatedAt, &out.ResolvedAt, &out.ResolvedBy)
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, userID, "service.cancellation.requested", "service", serviceID, map[string]any{"request_id": out.ID, "type": kind}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) WithdrawServiceCancellation(ctx context.Context, userID, serviceID int64) (CancellationRequest, error) {
	var out CancellationRequest
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var ownerID int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&ownerID); err != nil {
		return out, err
	}
	if ownerID != userID {
		return out, pgx.ErrNoRows
	}
	err = tx.QueryRow(ctx, `UPDATE service_cancellation_requests
SET status='withdrawn',resolved_at=now(),resolution_note='客户撤回'
WHERE service_id=$1 AND user_id=$2 AND status='pending'
RETURNING id,service_id,user_id,type,reason,status,resolution_note,created_at,resolved_at,resolved_by`, serviceID, userID).
		Scan(&out.ID, &out.ServiceID, &out.UserID, &out.Type, &out.Reason, &out.Status, &out.ResolutionNote, &out.CreatedAt, &out.ResolvedAt, &out.ResolvedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, conflict("cancellation_not_pending", "没有可撤回的取消申请")
	}
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, userID, "service.cancellation.withdrawn", "service", serviceID, map[string]any{"request_id": out.ID}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ResolveServiceCancellation(ctx context.Context, actorID, requestID int64, decision, note string) (CancellationRequest, error) {
	var out CancellationRequest
	decision = strings.TrimSpace(strings.ToLower(decision))
	note = strings.TrimSpace(note)
	if actorID < 1 || requestID < 1 || (decision != "approved" && decision != "declined") || len(note) > 2000 {
		return out, conflict("invalid_cancellation_decision", "审核决定或备注无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var serviceStatus, kind string
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "cancellation", requestID); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT id,service_id,user_id,type,reason,status,resolution_note,created_at,resolved_at,resolved_by
FROM service_cancellation_requests WHERE id=$1 FOR UPDATE`, requestID).
		Scan(&out.ID, &out.ServiceID, &out.UserID, &kind, &out.Reason, &out.Status, &out.ResolutionNote, &out.CreatedAt, &out.ResolvedAt, &out.ResolvedBy)
	if err != nil {
		return out, err
	}
	out.Type = kind
	if out.Status != "pending" {
		return out, conflict("cancellation_already_resolved", "取消申请已经处理")
	}
	if err = tx.QueryRow(ctx, "SELECT status FROM services WHERE id=$1 FOR UPDATE", out.ServiceID).Scan(&serviceStatus); err != nil {
		return out, err
	}
	if decision == "approved" {
		if err = requireServiceReviewed(ctx, tx, out.ServiceID); err != nil {
			return out, err
		}
		if !allowedServiceAction(serviceStatus, "terminate") {
			return out, conflict("invalid_cancellation_state", "服务当前状态不能执行取消")
		}
		var pending bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND status IN ('pending','running','waiting_manual','waiting_verification'))", out.ServiceID).Scan(&pending); err != nil {
			return out, err
		}
		if pending {
			return out, conflict("service_action_pending", "服务已有任务正在处理中")
		}
		var jobID int64
		available := "now()"
		if kind == "end_of_billing" {
			available = "COALESCE(s.next_due_at,now())"
		}
		err = tx.QueryRow(ctx, fmt.Sprintf(`INSERT INTO provision_jobs(service_id,action,idempotency_key,available_at)
SELECT s.id,'terminate',$2,%s FROM services s WHERE s.id=$1
RETURNING id`, available), out.ServiceID, fmt.Sprintf("service-cancellation-%d", requestID)).Scan(&jobID)
		if err != nil {
			return out, err
		}
		out.JobID = &jobID
	}
	err = tx.QueryRow(ctx, `UPDATE service_cancellation_requests SET status=$2,resolved_by=$3,resolution_note=$4,resolved_at=now()
WHERE id=$1 RETURNING status,resolution_note,resolved_at,resolved_by`, requestID, decision, actorID, note).
		Scan(&out.Status, &out.ResolutionNote, &out.ResolvedAt, &out.ResolvedBy)
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "service.cancellation."+decision, "service", out.ServiceID, map[string]any{"request_id": requestID, "type": kind, "job_id": out.JobID, "note": note}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
