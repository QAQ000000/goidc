package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/jackc/pgx/v5"
)

type LegacyReviewInput struct {
	Version             int64  `json:"version"`
	Decision            string `json:"decision"`
	Evidence            string `json:"evidence"`
	State               string `json:"state"`
	UpstreamID          string `json:"upstream_id"`
	RecurringPriceCents *int64 `json:"recurring_price_cents"`
}

type LegacyReviewResult struct {
	ID               int64  `json:"id"`
	Entity           string `json:"entity"`
	TargetID         int64  `json:"target_id"`
	Version          int64  `json:"version"`
	Decision         string `json:"decision"`
	State            string `json:"state"`
	OperationBlocked bool   `json:"operation_blocked"`
	Replayed         bool   `json:"replayed"`
}

type legacyReviewObject struct {
	TargetID       int64  `json:"target_id"`
	Source         string `json:"source"`
	LegacyID       int64  `json:"legacy_id"`
	Version        int64  `json:"version"`
	ReviewRequired bool   `json:"review_required"`
	State          string `json:"state"`
	UserID         int64  `json:"user_id"`
	OrderID        int64  `json:"order_id"`
	AmountCents    int64  `json:"amount_cents"`
	BillingCycle   string `json:"billing_cycle"`
	UpstreamID     string `json:"upstream_id"`
}

func legacyReviewTable(entity string) (string, error) {
	if entity != "invoices" && entity != "services" {
		return "", conflict("invalid_review_entity", "只支持账单或服务复核")
	}
	return entity, nil
}

func legacyReviewObjectLocked(ctx context.Context, tx pgx.Tx, entity string, id int64) (legacyReviewObject, error) {
	var out legacyReviewObject
	table, err := legacyReviewTable(entity)
	if err != nil {
		return out, err
	}
	fields := "amount_cents,''::text,''::text"
	if entity == "services" {
		fields = "coalesce(recurring_price_cents,(SELECT price_cents FROM order_items WHERE id=services.order_item_id),0),billing_cycle,upstream_id"
	}
	err = tx.QueryRow(ctx, `SELECT legacy_source,legacy_id,import_review_version,import_review_required,status,user_id,coalesce(order_id,0),`+fields+` FROM `+table+` WHERE id=$1 AND legacy_source<>'' AND legacy_id IS NOT NULL FOR UPDATE`, id).
		Scan(&out.Source, &out.LegacyID, &out.Version, &out.ReviewRequired, &out.State, &out.UserID, &out.OrderID, &out.AmountCents, &out.BillingCycle, &out.UpstreamID)
	out.TargetID = id
	return out, err
}

func legacyReviewSource(ctx context.Context, tx pgx.Tx, entity string, obj legacyReviewObject) (legacy.Record, string, error) {
	if entity == "invoices" && obj.LegacyID < 0 {
		entity = "orphan_invoices"
	}
	var raw []byte
	var hash string
	if err := tx.QueryRow(ctx, "SELECT payload,checksum FROM legacy_source_records WHERE source=$1 AND entity=$2 AND legacy_id=$3", obj.Source, entity, obj.LegacyID).Scan(&raw, &hash); err != nil {
		return nil, "", err
	}
	var record legacy.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, "", err
	}
	canonical, _ := json.Marshal(record)
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, "", conflict("legacy_archive_changed", "旧数据归档校验失败")
	}
	return record, hash, nil
}

func legacyReleasePrerequisites(ctx context.Context, tx pgx.Tx, entity string, obj legacyReviewObject, source legacy.Record) error {
	if !obj.ReviewRequired {
		return conflict("legacy_already_released", "记录已解除业务隔离")
	}
	if entity == "invoices" && (obj.LegacyID <= 0 || obj.State != "unpaid" || (!strings.EqualFold(source["status"], "Unpaid") && !strings.EqualFold(source["status"], "Overdue")) || source["is_delete"] == "1" || (source["delete_time"] != "" && source["delete_time"] != "0")) {
		return conflict("legacy_invoice_archive_only", "该历史账单仅支持确认归档")
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1", obj.UserID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return conflict("legacy_customer_disabled", "客户账户尚未启用")
	}
	var mapped bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM legacy_id_map WHERE source=$1 AND entity=$2 AND legacy_id=$3 AND target_id=$4)", obj.Source, entity, obj.LegacyID, obj.TargetID).Scan(&mapped); err != nil {
		return err
	}
	if !mapped {
		return conflict("legacy_mapping_missing", "记录的旧 ID 映射尚未核对一致")
	}
	if entity == "services" {
		expected := map[string]string{"pending": "pending_payment", "active": "provisioning", "suspended": "suspended"}[strings.ToLower(source["domainstatus"])]
		if expected == "" || obj.State != expected {
			return conflict("legacy_service_state", "旧服务状态或当前状态不允许恢复操作")
		}
		var owner, productID int64
		var recurring, first int64
		var cycle string
		if err := tx.QueryRow(ctx, `SELECT o.user_id,oi.product_id,coalesce(s.recurring_price_cents,oi.price_cents),oi.total_cents,oi.billing_cycle
FROM services s JOIN orders o ON o.id=s.order_id JOIN order_items oi ON oi.id=s.order_item_id
WHERE s.id=(SELECT target_id FROM legacy_id_map WHERE source=$1 AND entity='services' AND legacy_id=$2)
AND oi.order_id=o.id AND oi.product_id=s.product_id`, obj.Source, obj.LegacyID).Scan(&owner, &productID, &recurring, &first, &cycle); err != nil {
			return err
		}
		amount, err := legacy.Cents(source["amount"])
		legacyProduct, parseErr := strconv.ParseInt(source["productid"], 10, 64)
		var productMatches bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM products WHERE id=$1 AND legacy_source=$2 AND legacy_id=$3)", productID, obj.Source, legacyProduct).Scan(&productMatches); err != nil {
			return err
		}
		if err != nil || parseErr != nil || !productMatches || recurring != amount || owner != obj.UserID || cycle != obj.BillingCycle {
			return conflict("legacy_service_relationship", "服务归属、账期或周期金额尚未核对一致")
		}
		if expected == "pending_payment" {
			initial, err := legacy.Cents(source["firstpaymentamount"])
			if err != nil || initial != first {
				return conflict("legacy_initial_amount", "首次支付金额与订单项目不一致，需先完成金额拆分")
			}
		}
		var pending bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=(SELECT target_id FROM legacy_id_map WHERE source=$1 AND entity='services' AND legacy_id=$2) AND status IN ('pending','running','waiting_manual','waiting_verification'))", obj.Source, obj.LegacyID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return conflict("service_action_pending", "服务存在尚未完成的资源任务")
		}
		return nil
	}
	if obj.OrderID == 0 {
		return conflict("legacy_invoice_relationship", "账单缺少有效订单关联")
	}
	for _, field := range []string{"credit", "tax", "tax2"} {
		amount, err := legacy.Cents(source[field])
		if err != nil || amount != 0 {
			return conflict("legacy_invoice_adjustments", "历史抵扣或税费尚未完成迁移")
		}
	}
	amount, err := legacy.Cents(source["total"])
	if err != nil || amount != obj.AmountCents {
		return conflict("legacy_invoice_amount", "当前账单金额与旧账单不一致")
	}
	var owner, total int64
	var state string
	if err := tx.QueryRow(ctx, "SELECT user_id,total_cents,status FROM orders WHERE id=$1 FOR UPDATE", obj.OrderID).Scan(&owner, &total, &state); err != nil {
		return err
	}
	if owner != obj.UserID || total != amount || state != "pending" {
		return conflict("legacy_order_amount", "订单归属、状态或应收金额与账单不一致")
	}
	var itemTotal int64
	var unsupported bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount_cents),0),coalesce(bool_or(item_type<>'host'),false) FROM invoice_items
WHERE invoice_id=(SELECT target_id FROM legacy_id_map WHERE source=$1 AND entity='invoices' AND legacy_id=$2)`, obj.Source, obj.LegacyID).Scan(&itemTotal, &unsupported); err != nil {
		return err
	}
	if unsupported || itemTotal != amount {
		return conflict("legacy_invoice_items", "账单项目类型或合计尚未核对一致")
	}
	itemRows, err := tx.Query(ctx, `SELECT legacy_id,legacy_source,amount_cents FROM invoice_items WHERE invoice_id=$1 ORDER BY id FOR UPDATE`, obj.TargetID)
	if err != nil {
		return err
	}
	type item struct {
		id, amount int64
		source     string
	}
	items, err := pgx.CollectRows(itemRows, func(row pgx.CollectableRow) (item, error) {
		var i item
		err := row.Scan(&i.id, &i.source, &i.amount)
		return i, err
	})
	if err != nil {
		return err
	}
	var archivedCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM legacy_source_records WHERE source=$1 AND entity='invoice_items'
AND payload->>'invoice_id'=$2 AND coalesce(payload->>'delete_time','0')='0'`, obj.Source, fmt.Sprint(obj.LegacyID)).Scan(&archivedCount); err != nil {
		return err
	}
	if len(items) != archivedCount {
		return conflict("legacy_invoice_items", "账单项目数量与旧记录不一致")
	}
	seenServices := map[int64]bool{}
	for _, i := range items {
		if i.source != obj.Source {
			return conflict("legacy_invoice_items", "账单包含未经核验的新增项目")
		}
		r, _, err := legacyReviewSource(ctx, tx, "invoice_items", legacyReviewObject{Source: obj.Source, LegacyID: i.id})
		if err != nil {
			return err
		}
		original, err := legacy.Cents(r["amount"])
		if err != nil || original != i.amount || r["type"] != "host" || r["invoice_id"] != fmt.Sprint(obj.LegacyID) || r["uid"] != source["uid"] {
			return conflict("legacy_invoice_items", "账单项目金额或归属与旧记录不一致")
		}
		host, err := strconv.ParseInt(r["rel_id"], 10, 64)
		if err != nil || seenServices[host] {
			return conflict("legacy_invoice_relationship", "账单包含无效或重复的服务关联")
		}
		seenServices[host] = true
		var linked bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM legacy_id_map m JOIN services s ON s.id=m.target_id
WHERE m.source=$1 AND m.entity='services' AND m.legacy_id=$2 AND s.legacy_source=$1 AND s.legacy_id=$2 AND s.order_id=$3 AND s.user_id=$4)`, obj.Source, host, obj.OrderID, obj.UserID).Scan(&linked); err != nil {
			return err
		}
		if !linked {
			return conflict("legacy_invoice_relationship", "账单存在缺失或跨订单的旧服务")
		}
	}
	rows, err := tx.Query(ctx, `SELECT s.id,s.import_review_required,s.status,oi.total_cents FROM services s
JOIN order_items oi ON oi.id=s.order_item_id WHERE s.order_id=$1 ORDER BY s.id FOR UPDATE OF s`, obj.OrderID)
	if err != nil {
		return err
	}
	type entry struct {
		id, total int64
		held      bool
		state     string
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (entry, error) {
		var e entry
		err := row.Scan(&e.id, &e.held, &e.state, &e.total)
		return e, err
	})
	if err != nil {
		return err
	}
	if len(entries) == 0 || len(entries) != len(seenServices) {
		return conflict("legacy_invoice_relationship", "订单缺少服务项目")
	}
	var serviceTotal int64
	for _, e := range entries {
		if e.held || e.state != "pending_payment" {
			return conflict("legacy_service_review_pending", "需先核验订单下的待支付服务")
		}
		if e.total > math.MaxInt64-serviceTotal {
			return conflict("legacy_order_items", "服务订单项目合计溢出")
		}
		serviceTotal += e.total
	}
	if serviceTotal != amount {
		return conflict("legacy_order_items", "服务订单项目合计与应收金额不一致")
	}
	return nil
}

func (s *Store) ReviewLegacy(ctx context.Context, actorID int64, entity string, targetID int64, key string, input LegacyReviewInput) (LegacyReviewResult, error) {
	var out LegacyReviewResult
	table, err := legacyReviewTable(entity)
	if err != nil {
		return out, err
	}
	input.Evidence = strings.TrimSpace(input.Evidence)
	input.UpstreamID = strings.TrimSpace(input.UpstreamID)
	if targetID < 1 || input.Version < 1 || len(key) < 8 || len(key) > 128 || utf8.RuneCountInString(input.Evidence) < 3 || utf8.RuneCountInString(input.Evidence) > 2000 || (input.Decision != "archive" && input.Decision != "release") || len(input.UpstreamID) > 256 {
		return out, conflict("invalid_legacy_review", "复核版本、决定、凭据说明或幂等键无效")
	}
	raw, _ := json.Marshal(struct {
		Entity string
		ID     int64
		Input  LegacyReviewInput
	}{entity, targetID, input})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active' AND role='super_admin')", actorID).Scan(&allowed); err != nil {
		return out, err
	}
	if !allowed {
		return out, conflict("super_admin_required", "迁移复核需要超级管理员权限")
	}
	if err = lockOperationKey(ctx, tx, "legacy-review", fmt.Sprintf("%d:%s", actorID, key)); err != nil {
		return out, err
	}
	var savedHash string
	var saved []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM legacy_review_decisions WHERE actor_id=$1 AND idempotency_key=$2", actorID, key).Scan(&savedHash, &saved)
	if err == nil {
		if savedHash != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同复核决定")
		}
		if err = json.Unmarshal(saved, &out); err != nil {
			return out, err
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var sourceName string
	if err = tx.QueryRow(ctx, "SELECT legacy_source FROM "+table+" WHERE id=$1 AND legacy_source<>''", targetID).Scan(&sourceName); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "legacy-import:"+sourceName); err != nil {
		return out, err
	}
	obj, err := legacyReviewObjectLocked(ctx, tx, entity, targetID)
	if err != nil {
		return out, err
	}
	if obj.Version != input.Version {
		return out, conflict("version_conflict", "复核记录已变化，请刷新后重试")
	}
	if !obj.ReviewRequired {
		return out, conflict("legacy_already_released", "记录已解除业务隔离")
	}
	source, sourceHash, err := legacyReviewSource(ctx, tx, entity, obj)
	if err != nil {
		return out, err
	}
	state := obj.State
	if input.Decision == "release" {
		if err = legacyReleasePrerequisites(ctx, tx, entity, obj, source); err != nil {
			return out, err
		}
		if entity == "services" {
			state = map[string]string{"pending": "pending_payment", "active": "active", "suspended": "suspended"}[strings.ToLower(source["domainstatus"])]
			amount, _ := legacy.Cents(source["amount"])
			if input.State != state || input.RecurringPriceCents == nil || *input.RecurringPriceCents != amount {
				return out, conflict("legacy_service_terms", "服务状态或周期金额与核验记录不一致")
			}
			if (state == "pending_payment" && input.UpstreamID != "") || (state != "pending_payment" && (input.UpstreamID == "" || strings.HasPrefix(input.UpstreamID, "legacy-host-"))) {
				return out, conflict("legacy_resource_required", "请提供已核验的实际资源标识")
			}
			if _, err = tx.Exec(ctx, `UPDATE services SET status=$2,upstream_id=$3,recurring_price_cents=$4,provision_mode='manual',import_review_required=false,
import_review_version=import_review_version+1,auto_renew=false,last_error='',version=version+1,updated_at=now() WHERE id=$1`, targetID, state, input.UpstreamID, amount); err != nil {
				return out, err
			}
		} else {
			if input.State != "" || input.UpstreamID != "" || input.RecurringPriceCents != nil {
				return out, conflict("invalid_legacy_review", "账单复核不接受服务参数")
			}
			if _, err = tx.Exec(ctx, "UPDATE invoices SET import_review_required=false,import_review_version=import_review_version+1 WHERE id=$1", targetID); err != nil {
				return out, err
			}
		}
	} else {
		if input.State != "" || input.UpstreamID != "" || input.RecurringPriceCents != nil {
			return out, conflict("invalid_legacy_review", "归档决定不接受业务变更参数")
		}
		if _, err = tx.Exec(ctx, "UPDATE "+table+" SET import_review_version=import_review_version+1 WHERE id=$1", targetID); err != nil {
			return out, err
		}
	}
	out = LegacyReviewResult{Entity: entity, TargetID: targetID, Version: obj.Version + 1, Decision: input.Decision, State: state, OperationBlocked: input.Decision == "archive"}
	result, _ := json.Marshal(out)
	if err = tx.QueryRow(ctx, `INSERT INTO legacy_review_decisions(entity,target_id,source,legacy_id,version,decision,actor_id,evidence,source_checksum,idempotency_key,request_hash,result)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, entity, targetID, obj.Source, obj.LegacyID, out.Version, input.Decision, actorID, input.Evidence, sourceHash, key, hash, result).Scan(&out.ID); err != nil {
		return out, err
	}
	result, _ = json.Marshal(out)
	if _, err = tx.Exec(ctx, "UPDATE legacy_review_decisions SET result=$2 WHERE id=$1", out.ID, result); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "legacy.review."+input.Decision, entity, targetID, map[string]any{"review_id": out.ID, "source": obj.Source, "legacy_id": obj.LegacyID, "version": out.Version, "source_checksum": sourceHash, "evidence": input.Evidence}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) LegacyReviewDetail(ctx context.Context, entity string, targetID int64) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	obj, err := legacyReviewObjectLocked(ctx, tx, entity, targetID)
	if err != nil {
		return nil, err
	}
	source, hash, err := legacyReviewSource(ctx, tx, entity, obj)
	if err != nil {
		return nil, err
	}
	blockers := []string{}
	if err = legacyReleasePrerequisites(ctx, tx, entity, obj, source); err != nil {
		var c *Conflict
		if !errors.As(err, &c) {
			return nil, err
		}
		blockers = append(blockers, c.Message)
	}
	rows, err := tx.Query(ctx, "SELECT id,version,decision,evidence,actor_id,created_at FROM legacy_review_decisions WHERE entity=$1 AND target_id=$2 ORDER BY version DESC", entity, targetID)
	if err != nil {
		return nil, err
	}
	decisions, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if decisions == nil {
		decisions = []map[string]any{}
	}
	rows, err = tx.Query(ctx, `SELECT entity,legacy_id,code,detail FROM legacy_import_issues
WHERE run_id=(SELECT max(id) FROM legacy_import_runs WHERE source=$1 AND status='succeeded')
AND ((entity=$2 AND legacy_id=$3) OR ($2='invoices' AND entity='invoice_items' AND legacy_id IN
(SELECT legacy_id FROM legacy_source_records WHERE source=$1 AND entity='invoice_items' AND payload->>'invoice_id'=$4)))
ORDER BY id LIMIT 100`, obj.Source, entity, obj.LegacyID, fmt.Sprint(absLegacyID(obj.LegacyID)))
	if err != nil {
		return nil, err
	}
	issues, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if issues == nil {
		issues = []map[string]any{}
	}
	return map[string]any{"entity": entity, "target_id": targetID, "current": obj, "source_record": source, "source_checksum": hash, "release_blockers": blockers, "decisions": decisions, "issues": issues}, nil
}

func absLegacyID(id int64) int64 {
	if id < 0 {
		return -id
	}
	return id
}
