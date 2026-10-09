package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type AffiliateWithdrawalInput struct {
	AmountCents int64  `json:"amount_cents"`
	Method      string `json:"method"`
	Target      string `json:"target"`
	Holder      string `json:"holder"`
}
type AffiliateWithdrawalReview struct {
	Decision        string `json:"decision"`
	Note            string `json:"note"`
	ExpectedVersion int64  `json:"expected_version"`
}
type AffiliatePayoutInput struct {
	ExternalRef     string `json:"external_ref"`
	Channel         string `json:"channel"`
	ProofHash       string `json:"proof_hash"`
	Note            string `json:"note"`
	ExpectedVersion int64  `json:"expected_version"`
	Confirmed       bool   `json:"confirmed"`
}

func affiliateValidKey(key string) bool { return len(key) >= 8 && len(key) <= 128 }
func (s *Store) affiliatePrivateHash(input any) (string, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return "", conflict("withdrawal_encryption_unavailable", "提现加密密钥未配置")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, s.ProvisionEncryptionKey)
	m.Write([]byte("affiliate-operation:v1:"))
	m.Write(raw)
	return hex.EncodeToString(m.Sum(nil)), nil
}
func (s *Store) affiliateTargetCrypt(raw []byte, aad string, seal bool) ([]byte, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return nil, conflict("withdrawal_encryption_unavailable", "提现加密密钥未配置")
	}
	block, err := aes.NewCipher(s.ProvisionEncryptionKey)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if seal {
		nonce := make([]byte, g.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return nil, err
		}
		return g.Seal(nonce, nonce, raw, []byte(aad)), nil
	}
	if len(raw) < g.NonceSize() {
		return nil, fmt.Errorf("withdrawal target unavailable")
	}
	return g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(aad))
}
func maskAffiliateTarget(value string) string {
	r := []rune(value)
	if len(r) <= 4 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}

// RBAC mutations use the exclusive counterpart. After waits we re-read role,
// permissions, credential and customer scope within the financial transaction.
func lockAffiliateActor(ctx context.Context, tx pgx.Tx, actor, customer int64, permission string) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	var allowed bool
	if permission == "" {
		if err := tx.QueryRow(ctx, "SELECT role='customer' AND status='active' FROM users WHERE id=$1", actor).Scan(&allowed); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND r.active AND $2=ANY(r.permissions))),false) FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, actor, permission).Scan(&allowed); err != nil {
			return err
		}
	}
	if !allowed {
		return conflict("permission_denied", "没有执行该返佣操作的权限")
	}
	if permission != "" {
		return LockAdminCustomer(ctx, tx, actor, customer)
	}
	return nil
}
func affiliateWithdrawalEvent(ctx context.Context, tx pgx.Tx, id, actor int64, event, before, after, note string) error {
	_, err := tx.Exec(ctx, "INSERT INTO affiliate_withdrawal_events(withdrawal_id,actor_id,event_type,before_status,after_status,note) VALUES($1,$2,$3,NULLIF($4,''),$5,$6)", id, nullableInt64(actor), event, before, after, note)
	return err
}

func (s *Store) CreateAffiliateWithdrawal(ctx context.Context, actor int64, key string, in AffiliateWithdrawalInput) (map[string]any, bool, error) {
	in.Method = strings.TrimSpace(in.Method)
	in.Target = strings.TrimSpace(in.Target)
	in.Holder = strings.TrimSpace(in.Holder)
	if !affiliateValidKey(key) || in.AmountCents <= 0 || (in.Method != "bank" && in.Method != "alipay" && in.Method != "wechat" && in.Method != "other") || utf8.RuneCountInString(in.Target) < 4 || utf8.RuneCountInString(in.Target) > 200 || utf8.RuneCountInString(in.Holder) < 1 || utf8.RuneCountInString(in.Holder) > 120 || strings.ContainsAny(in.Target+in.Holder, "\r\n\x00") {
		return nil, false, conflict("invalid_withdrawal", "提现金额和收款资料无效")
	}
	hash, err := s.affiliatePrivateHash(in)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "affiliate-withdrawal", fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return nil, false, err
	}
	if err = lockAffiliateActor(ctx, tx, actor, actor, ""); err != nil {
		return nil, false, err
	}
	var account int64
	var status string
	if err = tx.QueryRow(ctx, "SELECT id,status FROM affiliate_accounts WHERE user_id=$1 FOR UPDATE", actor).Scan(&account, &status); err != nil {
		return nil, false, err
	}
	if result, replay, e := affiliateOperation(ctx, tx, actor, "withdrawal.create", key, hash); e != nil {
		return nil, false, e
	} else if replay {
		return result, true, tx.Commit(ctx)
	}
	if status != "approved" {
		return nil, false, conflict("affiliate_not_approved", "推广账户未获批准或已停用")
	}
	var minimum, fee int64
	var programStatus string
	if err = tx.QueryRow(ctx, `SELECT p.status,p.min_withdraw_cents,p.fee_cents FROM affiliate_programs p JOIN affiliate_accounts a ON a.program_id=p.id WHERE a.id=$1 FOR SHARE OF p`, account).Scan(&programStatus, &minimum, &fee); err != nil {
		return nil, false, err
	}
	if programStatus != "active" {
		return nil, false, conflict("affiliate_disabled", "推广计划已停用")
	}
	if in.AmountCents < minimum || in.AmountCents <= fee {
		return nil, false, conflict("withdrawal_below_minimum", "低于最低提现额或不足以支付手续费")
	}
	var debt bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM affiliate_clawbacks c JOIN affiliate_commission_entries e ON e.id=c.entry_id WHERE e.account_id=$1 AND c.outstanding_cents>0)`, account).Scan(&debt); err != nil {
		return nil, false, err
	}
	if debt {
		return nil, false, conflict("withdrawal_balance_conflict", "存在待追缴佣金，请先处理")
	}
	rows, err := tx.Query(ctx, "SELECT id,available_cents,status FROM affiliate_commission_entries WHERE account_id=$1 AND available_cents>0 AND reversal_at IS NULL ORDER BY id FOR UPDATE", account)
	if err != nil {
		return nil, false, err
	}
	type allocation struct {
		id, amount int64
		before     string
	}
	var allocations []allocation
	left := in.AmountCents
	for rows.Next() {
		var a allocation
		if err = rows.Scan(&a.id, &a.amount, &a.before); err != nil {
			rows.Close()
			return nil, false, err
		}
		a.amount = min(left, a.amount)
		if a.amount > 0 {
			allocations = append(allocations, a)
			left -= a.amount
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if left > 0 {
		return nil, false, conflict("withdrawal_balance_conflict", "可提现佣金不足")
	}
	var id int64
	if err = tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('affiliate_withdrawals','id'))`).Scan(&id); err != nil {
		return nil, false, err
	}
	aad := fmt.Sprintf("zjmfgo:affiliate-withdrawal:%d:v1", id)
	raw, _ := json.Marshal(map[string]string{"target": in.Target, "holder": in.Holder})
	sealed, err := s.affiliateTargetCrypt(raw, aad, true)
	if err != nil {
		return nil, false, err
	}
	masked := maskAffiliateTarget(in.Target)
	snapshot, _ := json.Marshal(map[string]any{"target_masked": masked, "holder_masked": string([]rune(in.Holder)[:1]) + "**", "method": in.Method})
	var number string
	if err = tx.QueryRow(ctx, `INSERT INTO affiliate_withdrawals(id,account_id,amount_cents,fee_cents,payout_cents,payout_method,payout_target_ciphertext,payout_target_aad,payout_target_masked,payout_target_snapshot,idempotency_key) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$3::bigint-$4::bigint,$5,$6,$7,$8,$9,$10) RETURNING withdrawal_no`, id, account, in.AmountCents, fee, in.Method, sealed, aad, masked, snapshot, key).Scan(&number); err != nil {
		return nil, false, err
	}
	for _, a := range allocations {
		if _, err = tx.Exec(ctx, "UPDATE affiliate_commission_entries SET available_cents=available_cents-$2,reserved_cents=reserved_cents+$2 WHERE id=$1", a.id, a.amount); err != nil {
			return nil, false, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO affiliate_withdrawal_allocations(withdrawal_id,entry_id,amount_cents,status) VALUES($1,$2,$3,'reserved')", id, a.id, a.amount); err != nil {
			return nil, false, err
		}
		if err = affiliateEvent(ctx, tx, a.id, actor, id, 0, "reserved", -a.amount, a.before, "withdrawal submitted", fmt.Sprintf("affiliate:reserve:%d:%d", id, a.id)); err != nil {
			return nil, false, err
		}
	}
	result := map[string]any{"id": id, "withdrawal_no": number, "amount_cents": in.AmountCents, "fee_cents": fee, "payout_cents": in.AmountCents - fee, "status": "submitted", "version": int64(1), "target_masked": masked}
	if err = affiliateWithdrawalEvent(ctx, tx, id, actor, "submitted", "", "submitted", ""); err != nil {
		return nil, false, err
	}
	if err = affiliateAudit(ctx, tx, account, actor, "affiliate.withdrawal.submitted", "affiliate_withdrawal", id, result); err != nil {
		return nil, false, err
	}
	if err = saveAffiliateOperation(ctx, tx, actor, "withdrawal.create", key, hash, result); err != nil {
		return nil, false, err
	}
	return result, false, tx.Commit(ctx)
}

func (s *Store) lockWithdrawal(ctx context.Context, tx pgx.Tx, actor, id int64, permission string) (int64, int64, string, int64, error) {
	var account, owner, version int64
	var status string
	if err := tx.QueryRow(ctx, "SELECT w.account_id,a.user_id FROM affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id WHERE w.id=$1", id).Scan(&account, &owner); err != nil {
		return 0, 0, "", 0, err
	}
	if err := lockAffiliateActor(ctx, tx, actor, owner, permission); err != nil {
		return 0, 0, "", 0, err
	}
	if err := lockAffiliateAccount(ctx, tx, account); err != nil {
		return 0, 0, "", 0, err
	}
	err := tx.QueryRow(ctx, "SELECT status,version FROM affiliate_withdrawals WHERE id=$1 FOR UPDATE", id).Scan(&status, &version)
	return account, owner, status, version, err
}

func (s *Store) ReviewAffiliateWithdrawal(ctx context.Context, actor, id int64, key string, in AffiliateWithdrawalReview) (map[string]any, error) {
	in.Note = strings.TrimSpace(in.Note)
	if !affiliateValidKey(key) || in.ExpectedVersion < 1 || in.Note == "" || utf8.RuneCountInString(in.Note) > 1000 || (in.Decision != "approved" && in.Decision != "rejected" && in.Decision != "request_info") {
		return nil, conflict("invalid_withdrawal_review", "审核结果、备注或版本无效")
	}
	hash, err := s.affiliatePrivateHash(struct {
		ID    int64
		Input AffiliateWithdrawalReview
	}{id, in})
	if err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "affiliate-withdrawal-review", fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return nil, err
	}
	account, _, status, version, err := s.lockWithdrawal(ctx, tx, actor, id, "affiliates.withdrawals.review")
	if err != nil {
		return nil, err
	}
	if result, replay, e := affiliateOperation(ctx, tx, actor, "withdrawal.review", key, hash); e != nil {
		return nil, e
	} else if replay {
		return result, tx.Commit(ctx)
	}
	if version != in.ExpectedVersion {
		return nil, conflict("version_conflict", "提现申请已更新")
	}
	if status != "submitted" && status != "reviewing" && status != "failed" && !(status == "approved" && in.Decision == "rejected") {
		return nil, conflict("withdrawal_state_conflict", "提现申请当前状态不可审核")
	}
	next := in.Decision
	if status == "approved" && next == "rejected" {
		next = "cancelled"
	}
	if next == "request_info" {
		next = "reviewing"
	}
	if next == "approved" {
		var reversed bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM affiliate_withdrawal_allocations a JOIN affiliate_commission_entries e ON e.id=a.entry_id WHERE a.withdrawal_id=$1 AND a.status='reserved' AND e.reversal_at IS NOT NULL)`, id).Scan(&reversed); err != nil {
			return nil, err
		}
		if reversed {
			return nil, conflict("withdrawal_state_conflict", "预留佣金已退款，请驳回提现")
		}
	}
	if next == "rejected" || next == "cancelled" {
		if err = releaseAffiliateWithdrawal(ctx, tx, actor, account, id); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO affiliate_withdrawal_reviews(withdrawal_id,reviewer_id,decision,note) VALUES($1,$2,$3,$4)", id, actor, in.Decision, in.Note); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE affiliate_withdrawals SET status=$2,version=version+1,reviewed_at=now(),updated_at=now() WHERE id=$1", id, next); err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "status": next, "version": version + 1}
	if err = affiliateWithdrawalEvent(ctx, tx, id, actor, in.Decision, status, next, in.Note); err != nil {
		return nil, err
	}
	if err = affiliateAudit(ctx, tx, account, actor, "affiliate.withdrawal.reviewed", "affiliate_withdrawal", id, map[string]any{"decision": in.Decision, "note": in.Note, "version": version + 1}); err != nil {
		return nil, err
	}
	if err = saveAffiliateOperation(ctx, tx, actor, "withdrawal.review", key, hash, result); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func releaseAffiliateWithdrawal(ctx context.Context, tx pgx.Tx, actor, account, id int64) error {
	rows, err := tx.Query(ctx, `SELECT e.id,a.amount_cents,e.status,e.reversal_at IS NOT NULL FROM affiliate_withdrawal_allocations a JOIN affiliate_commission_entries e ON e.id=a.entry_id WHERE a.withdrawal_id=$1 AND a.status='reserved' ORDER BY e.id FOR UPDATE OF e,a`, id)
	if err != nil {
		return err
	}
	type item struct {
		id, amount int64
		before     string
		reversed   bool
	}
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.amount, &i.before, &i.reversed); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		state := "released"
		event := "released"
		sql := "UPDATE affiliate_commission_entries SET reserved_cents=reserved_cents-$2,available_cents=available_cents+$2 WHERE id=$1"
		if i.reversed {
			state = "reversed"
			event = "reversed"
			sql = "UPDATE affiliate_commission_entries SET reserved_cents=reserved_cents-$2,reversed_cents=reversed_cents+$2 WHERE id=$1"
		}
		if _, err = tx.Exec(ctx, sql, i.id, i.amount); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE affiliate_withdrawal_allocations SET status=$3 WHERE withdrawal_id=$1 AND entry_id=$2", id, i.id, state); err != nil {
			return err
		}
		if i.reversed {
			if _, err = tx.Exec(ctx, "UPDATE affiliate_clawbacks SET outstanding_cents=0 WHERE entry_id=$1 AND withdrawal_id=$2", i.id, id); err != nil {
				return err
			}
		}
		if err = affiliateEvent(ctx, tx, i.id, actor, id, 0, event, i.amount, i.before, "withdrawal rejected", fmt.Sprintf("affiliate:unreserve:%d:%d", id, i.id)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RegisterAffiliatePayout(ctx context.Context, actor, id int64, key string, in AffiliatePayoutInput) (map[string]any, error) {
	in.ExternalRef = strings.TrimSpace(in.ExternalRef)
	in.Channel = strings.TrimSpace(in.Channel)
	in.Note = strings.TrimSpace(in.Note)
	if !affiliateValidKey(key) || in.ExpectedVersion < 1 || !in.Confirmed || in.ExternalRef == "" || len(in.ExternalRef) > 160 || in.Channel == "" || utf8.RuneCountInString(in.Channel) > 160 || in.Note == "" || utf8.RuneCountInString(in.Note) > 1000 || len(in.ProofHash) != 64 {
		return nil, conflict("invalid_payout", "请确认线下打款并提供渠道、流水、备注及SHA256凭证摘要")
	}
	if raw, err := hex.DecodeString(in.ProofHash); err != nil || len(raw) != 32 {
		return nil, conflict("invalid_payout", "凭证摘要无效")
	}
	hash, err := s.affiliatePrivateHash(struct {
		ID    int64
		Input AffiliatePayoutInput
	}{id, in})
	if err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "affiliate-withdrawal-payout", fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return nil, err
	}
	account, _, status, version, err := s.lockWithdrawal(ctx, tx, actor, id, "affiliates.payout")
	if err != nil {
		return nil, err
	}
	if result, replay, e := affiliateOperation(ctx, tx, actor, "withdrawal.payout", key, hash); e != nil {
		return nil, e
	} else if replay {
		return result, tx.Commit(ctx)
	}
	if status == "paid" {
		return nil, conflict("payout_duplicate", "提现已登记打款")
	}
	if status != "approved" || version != in.ExpectedVersion {
		return nil, conflict("withdrawal_state_conflict", "提现未批准或版本已更新")
	}
	var method string
	var amount int64
	if err = tx.QueryRow(ctx, "SELECT payout_method,amount_cents FROM affiliate_withdrawals WHERE id=$1", id).Scan(&method, &amount); err != nil {
		return nil, err
	}
	if err = lockOperationKey(ctx, tx, "affiliate-payout-ref", method+":"+in.ExternalRef); err != nil {
		return nil, err
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM affiliate_payouts WHERE method=$1 AND external_ref=$2)", method, in.ExternalRef).Scan(&duplicate); err != nil {
		return nil, err
	}
	if duplicate {
		return nil, conflict("payout_duplicate", "打款流水已被登记")
	}
	rows, err := tx.Query(ctx, `SELECT e.id,a.amount_cents,e.status,e.reversal_at IS NOT NULL FROM affiliate_withdrawal_allocations a JOIN affiliate_commission_entries e ON e.id=a.entry_id WHERE a.withdrawal_id=$1 AND a.status='reserved' ORDER BY e.id FOR UPDATE OF e,a`, id)
	if err != nil {
		return nil, err
	}
	type item struct {
		id, amount int64
		before     string
		reversed   bool
	}
	var items []item
	left := amount
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.amount, &i.before, &i.reversed); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, i)
		left -= i.amount
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if left != 0 {
		return nil, conflict("withdrawal_balance_conflict", "预留资金与提现额不一致")
	}
	for _, i := range items {
		if i.reversed {
			return nil, conflict("withdrawal_state_conflict", "佣金已退款，禁止打款")
		}
	}
	channel, _ := json.Marshal(map[string]any{"channel": in.Channel, "note": in.Note, "registration": "offline"})
	var payoutID int64
	if err = tx.QueryRow(ctx, `INSERT INTO affiliate_payouts(withdrawal_id,method,external_ref,channel_snapshot,paid_at,proof_hash,actor_id) VALUES($1,$2,$3,$4,now(),$5,$6) RETURNING id`, id, method, in.ExternalRef, channel, in.ProofHash, actor).Scan(&payoutID); err != nil {
		return nil, err
	}
	// Offline registration records approved -> paying -> paid atomically.
	if err = affiliateWithdrawalEvent(ctx, tx, id, actor, "paying", status, "paying", in.Note); err != nil {
		return nil, err
	}
	for _, i := range items {
		if _, err = tx.Exec(ctx, "UPDATE affiliate_commission_entries SET reserved_cents=reserved_cents-$2,paid_cents=paid_cents+$2 WHERE id=$1", i.id, i.amount); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, "UPDATE affiliate_withdrawal_allocations SET status='paid' WHERE withdrawal_id=$1 AND entry_id=$2", id, i.id); err != nil {
			return nil, err
		}
		if err = affiliateEvent(ctx, tx, i.id, actor, id, 0, "paid", -i.amount, i.before, "offline payout registered", fmt.Sprintf("affiliate:payout:%d:%d", id, i.id)); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE affiliate_withdrawals SET status='paid',paid_at=now(),version=version+1,updated_at=now() WHERE id=$1", id); err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "payout_id": payoutID, "status": "paid", "version": version + 1}
	if err = affiliateWithdrawalEvent(ctx, tx, id, actor, "paid", "paying", "paid", in.Note); err != nil {
		return nil, err
	}
	if err = affiliateAudit(ctx, tx, account, actor, "affiliate.withdrawal.paid", "affiliate_withdrawal", id, map[string]any{"payout_id": payoutID, "external_ref": in.ExternalRef, "proof_hash": in.ProofHash, "amount_cents": amount}); err != nil {
		return nil, err
	}
	if err = saveAffiliateOperation(ctx, tx, actor, "withdrawal.payout", key, hash, result); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

const affiliateWithdrawalProjection = `w.id,w.withdrawal_no,w.account_id,w.amount_cents,w.fee_cents,w.payout_cents,w.status,w.payout_method,w.payout_target_masked,w.payout_target_snapshot,w.version,w.submitted_at,w.reviewed_at,w.paid_at,w.created_at`

func (s *Store) ListAffiliateWithdrawals(ctx context.Context, actor int64, admin bool, limit, offset int) ([]map[string]any, error) {
	where := "a.user_id=$1"
	if admin {
		where = "admin_customer_owner_allowed($1,u.sales_owner_id)"
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+affiliateWithdrawalProjection+" FROM affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id JOIN users u ON u.id=a.user_id WHERE "+where+" ORDER BY w.id DESC LIMIT $2 OFFSET $3", actor, limit, offset)
	if err != nil {
		return nil, err
	}
	result, err := pgx.CollectRows(rows, pgx.RowToMap)
	if result == nil {
		result = []map[string]any{}
	}
	return result, err
}
func (s *Store) AffiliateWithdrawalDetail(ctx context.Context, actor, id int64, admin bool) (map[string]any, error) {
	where := "a.user_id=$2"
	if admin {
		where = "admin_customer_owner_allowed($2,u.sales_owner_id)"
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+affiliateWithdrawalProjection+" FROM affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id JOIN users u ON u.id=a.user_id WHERE w.id=$1 AND "+where, id, actor)
	if err != nil {
		return nil, err
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	rows, err = s.Pool.Query(ctx, "SELECT id,event_type,before_status,after_status,note,created_at FROM affiliate_withdrawal_events WHERE withdrawal_id=$1 ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	events, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	item["events"] = events
	rows, err = s.Pool.Query(ctx, "SELECT id,method,external_ref,status,paid_at,proof_hash,channel_snapshot FROM affiliate_payouts WHERE withdrawal_id=$1", id)
	if err != nil {
		return nil, err
	}
	payouts, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	item["payouts"] = payouts
	rows, err = s.Pool.Query(ctx, `SELECT a.entry_id,a.amount_cents,a.status,e.status AS commission_status,
       e.invoice_id,i.invoice_no,e.product_id,COALESCE(p.name,''),e.event_type,
       e.rule_snapshot,e.available_at
FROM affiliate_withdrawal_allocations a
JOIN affiliate_commission_entries e ON e.id=a.entry_id
JOIN invoices i ON i.id=e.invoice_id
LEFT JOIN products p ON p.id=e.product_id
WHERE a.withdrawal_id=$1 ORDER BY a.entry_id`, id)
	if err != nil {
		return nil, err
	}
	allocations, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	item["allocations"] = allocations
	return item, nil
}
func (s *Store) AffiliatePayoutTarget(ctx context.Context, actor, id int64, reason string) (map[string]string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 1000 {
		return nil, conflict("invalid_payout_reason", "读取收款账户需填写用途")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	account, _, status, _, err := s.lockWithdrawal(ctx, tx, actor, id, "affiliates.payout")
	if err != nil {
		return nil, err
	}
	if status != "approved" {
		return nil, conflict("withdrawal_state_conflict", "仅可读取已批准申请的收款账户")
	}
	var raw []byte
	var aad string
	if err = tx.QueryRow(ctx, "SELECT payout_target_ciphertext,payout_target_aad FROM affiliate_withdrawals WHERE id=$1", id).Scan(&raw, &aad); err != nil {
		return nil, err
	}
	clear, err := s.affiliateTargetCrypt(raw, aad, false)
	if err != nil {
		return nil, fmt.Errorf("withdrawal target unavailable")
	}
	var result map[string]string
	if err = json.Unmarshal(clear, &result); err != nil {
		return nil, err
	}
	if err = affiliateAudit(ctx, tx, account, actor, "affiliate.withdrawal.target_read", "affiliate_withdrawal", id, map[string]any{"reason": reason}); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
