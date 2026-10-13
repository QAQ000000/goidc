package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type AffiliateLink struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	LandingPath string `json:"landing_path"`
	ProductID   *int64 `json:"product_id,omitempty"`
	Note        string `json:"note"`
	Status      string `json:"status"`
	Clicks      int64  `json:"clicks"`
	Registrants int64  `json:"registrants"`
}

type AffiliateOverview struct {
	AccountID        int64  `json:"account_id"`
	AccountType      string `json:"account_type"`
	AccountStatus    string `json:"account_status"`
	ProgramID        int64  `json:"program_id"`
	ProgramName      string `json:"program_name"`
	PendingCents     int64  `json:"pending_cents"`
	AvailableCents   int64  `json:"available_cents"`
	ReservedCents    int64  `json:"reserved_cents"`
	PaidCents        int64  `json:"paid_cents"`
	MinWithdrawCents int64  `json:"min_withdraw_cents"`
	FeeCents         int64  `json:"fee_cents"`
	AttributionID    int64  `json:"attribution_id,omitempty"`
	ReferredCount    int64  `json:"referred_count"`
}

// ListAffiliateCommissions exposes the immutable commission snapshots and
// current bucket projection to the customer console. Monetary values remain
// integer cents; the web client only formats them for display.
func (s *Store) ListAffiliateCommissions(ctx context.Context, userID int64, limit, offset int) ([]map[string]any, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.Pool.Query(ctx, `
SELECT c.id,c.invoice_id,i.invoice_no,c.order_id,c.product_id,COALESCE(p.name,''),
       c.event_type,c.status,c.amount_cents,c.basis_cents,c.rate_bps,
       c.pending_cents,c.available_cents,c.reserved_cents,c.paid_cents,c.reversed_cents,
       c.available_at,GREATEST(0,CEIL(EXTRACT(EPOCH FROM (c.available_at-now()))/86400))::int AS days_remaining,
       c.rule_snapshot,c.source_snapshot,c.created_at
FROM affiliate_commission_entries c
JOIN affiliate_accounts a ON a.id=c.account_id
JOIN invoices i ON i.id=c.invoice_id
LEFT JOIN products p ON p.id=c.product_id
WHERE a.user_id=$1
ORDER BY c.created_at DESC,c.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result, err := pgx.CollectRows(rows, pgx.RowToMap)
	if result == nil {
		result = []map[string]any{}
	}
	return result, err
}

type AffiliateClickResult struct {
	Valid           bool   `json:"valid"`
	Recorded        bool   `json:"recorded"`
	LandingPath     string `json:"landing_path"`
	AttributionDays int    `json:"attribution_days"`
}

func affiliateHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newAffiliateCode() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) ApplyAffiliateAccount(ctx context.Context, userID int64, key string) (map[string]any, error) {
	if userID < 1 || len(key) < 8 || len(key) > 128 {
		return nil, conflict("invalid_affiliate_request", "推广申请参数无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "affiliate-apply", fmt.Sprint(userID)); err != nil {
		return nil, err
	}
	requestHash := affiliateHash("apply")
	if result, replay, replayErr := affiliateOperation(ctx, tx, userID, "apply", key, requestHash); replayErr != nil {
		return nil, replayErr
	} else if replay {
		return result, tx.Commit(ctx)
	}
	var id int64
	var status, accountType string
	err = tx.QueryRow(ctx, `SELECT id,status,account_type FROM affiliate_accounts WHERE user_id=$1 FOR UPDATE`, userID).Scan(&id, &status, &accountType)
	if err == nil {
		result := map[string]any{"account_id": id, "status": status, "account_type": accountType}
		if err = saveAffiliateOperation(ctx, tx, userID, "apply", key, requestHash, result); err != nil {
			return nil, err
		}
		return result, tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	var programID int64
	if err = tx.QueryRow(ctx, "SELECT id FROM affiliate_programs WHERE status='active' ORDER BY id LIMIT 1").Scan(&programID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, conflict("affiliate_disabled", "推广计划未启用")
		}
		return nil, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO affiliate_accounts(user_id,program_id,status) VALUES($1,$2,'pending') RETURNING id`, userID, programID).Scan(&id); err != nil {
		return nil, err
	}
	result := map[string]any{"account_id": id, "status": "pending", "account_type": "referrer"}
	if err = saveAffiliateOperation(ctx, tx, userID, "apply", key, requestHash, result); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, userID, "affiliate.account.applied", "affiliate_account", id, map[string]any{"program_id": programID}); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func affiliateOperation(ctx context.Context, tx pgx.Tx, userID int64, operation, key, requestHash string) (map[string]any, bool, error) {
	var savedHash string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,result FROM affiliate_operations WHERE user_id=$1 AND operation=$2 AND idempotency_key=$3 FOR UPDATE`, userID, operation, key).Scan(&savedHash, &raw)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if savedHash != requestHash {
		return nil, false, conflict("idempotency_conflict", "相同幂等键对应不同推广操作")
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func saveAffiliateOperation(ctx context.Context, tx pgx.Tx, userID int64, operation, key, requestHash string, result map[string]any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO affiliate_operations(user_id,operation,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,$5)`, userID, operation, key, requestHash, raw)
	return err
}

func (s *Store) CreateAffiliateLink(ctx context.Context, userID int64, key, requestHash, landingPath, note string, productID *int64) (AffiliateLink, bool, error) {
	var out AffiliateLink
	if userID < 1 || len(key) < 8 || len(key) > 128 || len(requestHash) != 64 || !validAffiliateLandingPath(landingPath) || utf8.RuneCountInString(note) > 160 {
		return out, false, conflict("invalid_affiliate_link", "推广链接参数无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "affiliate-link", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return out, false, err
	}
	if result, replay, replayErr := affiliateOperation(ctx, tx, userID, "link.create", key, requestHash); replayErr != nil {
		return out, false, replayErr
	} else if replay {
		if err = mapToAffiliateLink(result, &out); err != nil {
			return out, false, err
		}
		return out, true, tx.Commit(ctx)
	}
	var accountID int64
	if err = tx.QueryRow(ctx, `SELECT a.id FROM affiliate_accounts a JOIN affiliate_programs p ON p.id=a.program_id WHERE a.user_id=$1 AND a.status='approved' AND p.status='active' FOR SHARE OF a,p`, userID).Scan(&accountID); err != nil {
		if err == pgx.ErrNoRows {
			return out, false, conflict("affiliate_not_approved", "推广账户尚未审核通过")
		}
		return out, false, err
	}
	code, err := newAffiliateCode()
	if err != nil {
		return out, false, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO affiliate_links(account_id,code,landing_path,product_id,note) VALUES($1,$2,$3,$4,$5) RETURNING id,code,landing_path,product_id,note,status`, accountID, code, landingPath, productID, note).
		Scan(&out.ID, &out.Code, &out.LandingPath, &out.ProductID, &out.Note, &out.Status); err != nil {
		return out, false, err
	}
	result := map[string]any{"id": out.ID, "code": out.Code, "landing_path": out.LandingPath, "note": out.Note, "status": out.Status}
	if productID != nil {
		result["product_id"] = *productID
	}
	if err = saveAffiliateOperation(ctx, tx, userID, "link.create", key, requestHash, result); err != nil {
		return out, false, err
	}
	if err = Audit(ctx, tx, userID, "affiliate.link.created", "affiliate_link", out.ID, result); err != nil {
		return out, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, false, err
	}
	return out, false, nil
}

func mapToAffiliateLink(result map[string]any, out *AffiliateLink) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (s *Store) UpdateAffiliateLink(ctx context.Context, userID, linkID int64, key, requestHash, landingPath, note, status string) (AffiliateLink, error) {
	var out AffiliateLink
	if userID < 1 || linkID < 1 || len(key) < 8 || len(key) > 128 || len(requestHash) != 64 || (landingPath != "" && !validAffiliateLandingPath(landingPath)) || utf8.RuneCountInString(note) > 160 || status != "" && status != "active" && status != "disabled" {
		return out, conflict("invalid_affiliate_link", "推广链接参数无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "affiliate-link-update", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return out, err
	}
	requestHash = affiliateHash(fmt.Sprintf("%d:%s", linkID, requestHash))
	if result, replay, replayErr := affiliateOperation(ctx, tx, userID, "link.update", key, requestHash); replayErr != nil {
		return out, replayErr
	} else if replay {
		if err = mapToAffiliateLink(result, &out); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, `UPDATE affiliate_links l SET landing_path=CASE WHEN $3='' THEN l.landing_path ELSE $3 END,note=CASE WHEN $4='' THEN l.note ELSE $4 END,status=CASE WHEN $5='' THEN l.status ELSE $5 END,updated_at=now()
FROM affiliate_accounts a WHERE l.id=$1 AND a.id=l.account_id AND a.user_id=$2
	RETURNING l.id,l.code,l.landing_path,l.product_id,l.note,l.status`, linkID, userID, landingPath, note, status).
		Scan(&out.ID, &out.Code, &out.LandingPath, &out.ProductID, &out.Note, &out.Status)
	if err != nil {
		return out, err
	}
	result := map[string]any{"id": out.ID, "code": out.Code, "landing_path": out.LandingPath, "note": out.Note, "status": out.Status}
	if out.ProductID != nil {
		result["product_id"] = *out.ProductID
	}
	if err = saveAffiliateOperation(ctx, tx, userID, "link.update", key, requestHash, result); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, userID, "affiliate.link.updated", "affiliate_link", linkID, result); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ListAffiliateLinks(ctx context.Context, userID int64) ([]AffiliateLink, error) {
	rows, err := s.Pool.Query(ctx, `SELECT l.id,l.code,l.landing_path,l.product_id,l.note,l.status,
 (SELECT count(*) FROM affiliate_clicks c WHERE c.link_id=l.id),
 (SELECT count(*) FROM affiliate_attributions at WHERE at.link_id=l.id)
FROM affiliate_links l JOIN affiliate_accounts a ON a.id=l.account_id WHERE a.user_id=$1 ORDER BY l.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AffiliateLink{}
	for rows.Next() {
		var item AffiliateLink
		if err = rows.Scan(&item.ID, &item.Code, &item.LandingPath, &item.ProductID, &item.Note, &item.Status, &item.Clicks, &item.Registrants); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RecordAffiliateClick(ctx context.Context, code, sessionHash, referrer, landingPath, clientVersion string) (AffiliateClickResult, error) {
	result := AffiliateClickResult{LandingPath: "/", AttributionDays: 30}
	if len(code) < 12 || len(code) > 32 || !validAffiliateSessionHash(sessionHash) {
		return result, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var linkID, days int64
	var target string
	err = tx.QueryRow(ctx, `SELECT l.id,l.landing_path,p.attribution_days FROM affiliate_links l JOIN affiliate_accounts a ON a.id=l.account_id JOIN affiliate_programs p ON p.id=a.program_id
WHERE l.code=$1 AND l.status='active' AND a.status='approved' AND p.status='active'`, code).Scan(&linkID, &target, &days)
	if err == pgx.ErrNoRows {
		return result, tx.Commit(ctx)
	}
	if err != nil {
		return result, err
	}
	result.LandingPath, result.AttributionDays = target, int(days)
	result.Valid = true
	command, err := tx.Exec(ctx, `INSERT INTO affiliate_clicks(link_id,session_hash,referrer,landing_path,client_version)
VALUES($1,$2,$3,$4,$5) ON CONFLICT (link_id,session_hash,click_day) DO NOTHING`, linkID, sessionHash, affiliateReferrer(referrer), target, truncateAffiliateText(clientVersion, 120))
	if err != nil {
		return result, err
	}
	result.Recorded = command.RowsAffected() == 1
	return result, tx.Commit(ctx)
}

func truncateAffiliateText(value string, max int) string {
	value = strings.ToValidUTF8(value, "")
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

func validAffiliateSessionHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func validAffiliateLandingPath(value string) bool {
	u, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") && u.Host == "" && !u.IsAbs() && !strings.ContainsAny(value+u.Path, "\\\r\n\x00")
}

func affiliateReferrer(value string) string {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return truncateAffiliateText(u.String(), 2048)
}

func (s *Store) ConvertAffiliateClickTx(ctx context.Context, tx pgx.Tx, userID int64, code, sessionHash string) error {
	if userID < 1 || len(code) < 12 || len(code) > 32 || !validAffiliateSessionHash(sessionHash) {
		return nil
	}
	if err := lockOperationKey(ctx, tx, "affiliate-convert", sessionHash); err != nil {
		return err
	}
	if err := lockOperationKey(ctx, tx, "affiliate-convert-user", fmt.Sprint(userID)); err != nil {
		return err
	}
	var existing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM affiliate_attributions WHERE user_id=$1)
 OR EXISTS(SELECT 1 FROM affiliate_clicks WHERE session_hash=$2 AND registered_user_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM affiliate_clicks WHERE registered_user_id=$1)`, userID, sessionHash).Scan(&existing); err != nil || existing {
		return err
	}
	var clickID, accountID, linkID int64
	var firstClicked time.Time
	var source map[string]any
	err := tx.QueryRow(ctx, `SELECT c.id,a.id,c.link_id,c.created_at,jsonb_build_object('code',l.code,'landing_path',c.landing_path,'click_id',c.id)
FROM affiliate_clicks c JOIN affiliate_links l ON l.id=c.link_id JOIN affiliate_accounts a ON a.id=l.account_id JOIN affiliate_programs p ON p.id=a.program_id
WHERE c.session_hash=$1 AND c.registered_user_id IS NULL AND c.created_at>=now()-(p.attribution_days * interval '1 day')
  AND l.status='active' AND a.status='approved' AND p.status='active'
ORDER BY c.created_at,c.id LIMIT 1 FOR UPDATE OF c`, sessionHash).Scan(&clickID, &accountID, &linkID, &firstClicked, &source)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var ownerID int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM affiliate_accounts WHERE id=$1", accountID).Scan(&ownerID); err != nil {
		return err
	}
	if ownerID == userID {
		_, err = tx.Exec(ctx, "UPDATE affiliate_clicks SET registered_user_id=$2,converted_at=now() WHERE id=$1", clickID, userID)
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO affiliate_attributions(user_id,account_id,link_id,click_id,first_clicked_at,source_snapshot) VALUES($1,$2,$3,$4,$5,$6)`, userID, accountID, linkID, clickID, firstClicked, source); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE affiliate_clicks SET registered_user_id=$2,converted_at=now() WHERE id=$1", clickID, userID)
	if err != nil {
		return err
	}
	return Audit(ctx, tx, userID, "affiliate.attribution.created", "affiliate_account", accountID, source)
}

func (s *Store) AffiliateOverview(ctx context.Context, userID int64) (AffiliateOverview, error) {
	var out AffiliateOverview
	err := s.Pool.QueryRow(ctx, `SELECT a.id,a.account_type,a.status,p.id,p.name,
 COALESCE((SELECT sum(pending_cents) FROM affiliate_commission_entries WHERE account_id=a.id),0),
 COALESCE((SELECT sum(available_cents) FROM affiliate_commission_entries WHERE account_id=a.id),0),
 COALESCE((SELECT sum(reserved_cents) FROM affiliate_commission_entries WHERE account_id=a.id),0),
 COALESCE((SELECT sum(paid_cents) FROM affiliate_commission_entries WHERE account_id=a.id),0),
 p.min_withdraw_cents,p.fee_cents,
 COALESCE((SELECT count(*) FROM affiliate_attributions WHERE account_id=a.id),0)
FROM affiliate_accounts a JOIN affiliate_programs p ON p.id=a.program_id WHERE a.user_id=$1`, userID).
		Scan(&out.AccountID, &out.AccountType, &out.AccountStatus, &out.ProgramID, &out.ProgramName, &out.PendingCents, &out.AvailableCents, &out.ReservedCents, &out.PaidCents, &out.MinWithdrawCents, &out.FeeCents, &out.ReferredCount)
	if err == pgx.ErrNoRows {
		return AffiliateOverview{AccountStatus: "none"}, nil
	}
	return out, err
}

func (s *Store) ApproveAffiliateAccount(ctx context.Context, actorID, accountID int64, key, status string, expectedVersion int64) error {
	if status != "approved" && status != "suspended" && status != "rejected" && status != "closed" {
		return conflict("invalid_affiliate_status", "推广账户状态无效")
	}
	if actorID < 1 || accountID < 1 || len(key) < 8 || len(key) > 128 || expectedVersion < 1 {
		return conflict("invalid_affiliate_request", "推广申请参数无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT u.role='super_admin' OR (u.role='admin' AND coalesce(r.active AND 'affiliates.manage'=ANY(r.permissions),false))
FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return conflict("permission_denied", "没有审核推广账户的权限")
	}
	if err = lockOperationKey(ctx, tx, "affiliate-account-status", fmt.Sprintf("%d:%s", actorID, key)); err != nil {
		return err
	}
	requestHash := affiliateHash(fmt.Sprintf("%d:%s:%d", accountID, status, expectedVersion))
	if _, replay, replayErr := affiliateOperation(ctx, tx, actorID, "account.status", key, requestHash); replayErr != nil {
		return replayErr
	} else if replay {
		return tx.Commit(ctx)
	}
	var version int64
	var before string
	if err = tx.QueryRow(ctx, "SELECT version,status FROM affiliate_accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&version, &before); err != nil {
		return err
	}
	if version != expectedVersion {
		return conflict("version_conflict", "推广账户已变更，请重新加载")
	}
	command, err := tx.Exec(ctx, `UPDATE affiliate_accounts SET status=$2,approved_at=CASE WHEN $2='approved' THEN COALESCE(approved_at,now()) ELSE approved_at END,version=version+1,updated_at=now() WHERE id=$1`, accountID, status)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return conflict("affiliate_account_not_found", "推广账户不存在")
	}
	if err = Audit(ctx, tx, actorID, "affiliate.account.status_changed", "affiliate_account", accountID, map[string]any{"before_status": before, "status": status, "version": version + 1}); err != nil {
		return err
	}
	if err = saveAffiliateOperation(ctx, tx, actorID, "account.status", key, requestHash, map[string]any{"account_id": accountID, "status": status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// QueueAffiliateCommissions must run in the payment settlement transaction.
// It only appends pending commission rows and never updates users.balance_cents.
func (s *Store) QueueAffiliateCommissions(ctx context.Context, tx pgx.Tx, invoiceID, paymentTransactionID int64) error {
	var referredUser int64
	var invoiceStatus string
	var invoiceSnapshot json.RawMessage
	if err := tx.QueryRow(ctx, "SELECT user_id,status,to_jsonb(i) FROM invoices i WHERE id=$1 FOR SHARE", invoiceID).Scan(&referredUser, &invoiceStatus, &invoiceSnapshot); err != nil {
		return err
	}
	var creditOutstanding bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM credit_invoice_payments WHERE invoice_id=$1 AND status<>'repaid')", invoiceID).Scan(&creditOutstanding); err != nil {
		return err
	}
	if creditOutstanding {
		return nil
	}
	if invoiceStatus != "paid" {
		return nil
	}
	// Serialize first/repeat purchase classification for this customer. Payment
	// and invoice locks stay in their existing caller transactions.
	if err := lockOperationKey(ctx, tx, "affiliate-commission-customer", fmt.Sprint(referredUser)); err != nil {
		return err
	}
	var recorded bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM affiliate_commission_entries WHERE invoice_id=$1)", invoiceID).Scan(&recorded); err != nil || recorded {
		return err
	}
	var accountID, programID, ruleSetID int64
	var holdDays, ruleVersion int
	var defaultJSON json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT a.id,a.program_id,p.hold_days,rs.id,rs.version,rs.default_rules
FROM affiliate_attributions at JOIN affiliate_accounts a ON a.id=at.account_id
JOIN users owner ON owner.id=a.user_id JOIN affiliate_programs p ON p.id=a.program_id
JOIN affiliate_rule_sets rs ON rs.program_id=p.id JOIN invoices i ON i.id=$2
WHERE at.user_id=$1 AND at.status='active' AND a.status='approved' AND p.status='active'
 AND a.user_id<>$1 AND owner.status='active'
 AND rs.effective_from<=i.created_at AND (rs.effective_to IS NULL OR rs.effective_to>i.created_at)
FOR SHARE OF at,a,p,rs`, referredUser, invoiceID).
		Scan(&accountID, &programID, &holdDays, &ruleSetID, &ruleVersion, &defaultJSON); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	var defaultRules map[string]affiliateRule
	if err := json.Unmarshal(defaultJSON, &defaultRules); err != nil {
		return err
	}
	items, err := affiliateInvoiceItems(ctx, tx, invoiceID)
	if err != nil {
		return err
	}
	// Account-scoped serialization counts each settled invoice exactly once.
	if err = lockOperationKey(ctx, tx, "affiliate-turnover-account", fmt.Sprint(accountID)); err != nil {
		return err
	}
	var turnoverRecorded bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM affiliate_turnover_entries WHERE invoice_id=$1)`, invoiceID).Scan(&turnoverRecorded); err != nil || turnoverRecorded {
		return err
	}
	var paidAt, windowStart, windowEnd time.Time
	if err = tx.QueryRow(ctx, `WITH settlement AS (SELECT COALESCE((SELECT received_at FROM credit_repayments WHERE invoice_id=i.id AND remaining_after=0 ORDER BY version_after DESC LIMIT 1),i.paid_at,clock_timestamp()) AS paid_at FROM invoices i WHERE i.id=$1) SELECT paid_at,date_trunc('month',paid_at AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai',(date_trunc('month',paid_at AT TIME ZONE 'Asia/Shanghai')+interval '1 month') AT TIME ZONE 'Asia/Shanghai' FROM settlement`, invoiceID).Scan(&paidAt, &windowStart, &windowEnd); err != nil {
		return err
	}
	var invoiceBasis int64
	for _, item := range items {
		if item.BasisCents > 0 {
			if item.BasisCents > math.MaxInt64-invoiceBasis {
				return conflict("invalid_affiliate_rule", "计佣净额溢出")
			}
			invoiceBasis += item.BasisCents
		}
	}
	if invoiceBasis == 0 {
		return nil
	}
	var priorBasis int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(t.basis_cents),0)::bigint FROM affiliate_turnover_entries t JOIN invoices i ON i.id=t.invoice_id WHERE t.account_id=$1 AND t.paid_at>=$2 AND t.paid_at<$3 AND i.status='paid' AND NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=i.order_id AND o.status IN ('cancelled','refunded')) AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id AND c.status<>'repaid')`, accountID, windowStart, windowEnd).Scan(&priorBasis); err != nil {
		return err
	}
	if invoiceBasis > math.MaxInt64-priorBasis {
		return conflict("invalid_affiliate_rule", "计佣营业额溢出")
	}
	turnover := priorBasis + invoiceBasis
	var tier affiliateRule
	var tierLower int64
	var tierUpper *int64
	tierFound := true
	if err = tx.QueryRow(ctx, `SELECT rate_bps,amount_cents,lower_cents,upper_cents FROM affiliate_tier_rules WHERE rule_set_id=$1 AND lower_cents<=$2 AND (upper_cents IS NULL OR upper_cents>$2) ORDER BY lower_cents DESC LIMIT 1`, ruleSetID, turnover).Scan(&tier.RateBPS, &tier.AmountCents, &tierLower, &tierUpper); err != nil {
		if err != pgx.ErrNoRows {
			return err
		}
		tierFound = false
	}
	if _, err = tx.Exec(ctx, `INSERT INTO affiliate_turnover_entries(invoice_id,account_id,rule_set_id,basis_cents,paid_at) VALUES($1,$2,$3,$4,$5)`, invoiceID, accountID, ruleSetID, invoiceBasis, paidAt); err != nil {
		return err
	}
	for _, item := range items {
		rule, ruleSource, err := affiliateRuleForItem(ctx, tx, ruleSetID, item.ProductID, item.EventType, defaultRules)
		if err != nil {
			return err
		}
		if ruleSource == "default" && tierFound {
			// Tier replaces the amount/rate, retaining the event's plan cap.
			tier.MaxCents = rule.MaxCents
			rule = tier
			ruleSource = "tier"
		}
		amount, err := affiliateCommissionAmount(item.BasisCents, rule)
		if err != nil {
			return err
		}
		if rule.MaxCents != nil && amount > *rule.MaxCents {
			amount = *rule.MaxCents
		}
		if amount <= 0 || amount > item.BasisCents {
			continue
		}
		ruleSnapshot := map[string]any{"program_id": programID, "rule_set_id": ruleSetID, "rule_version": ruleVersion, "hold_days": holdDays, "rule_source": ruleSource, "event_type": item.EventType, "rate_bps": rule.RateBPS, "amount_cents": rule.AmountCents, "max_cents": rule.MaxCents, "basis": "net_amount", "rounding_remainder_bps": (item.BasisCents % 10000) * int64(rule.RateBPS) % 10000}
		if ruleSource == "tier" {
			ruleSnapshot["tier_lower_cents"] = tierLower
			ruleSnapshot["tier_upper_cents"] = tierUpper
		}
		ruleSnapshot["turnover_cents"] = turnover
		ruleSnapshot["invoice_net_cents"] = invoiceBasis
		ruleSnapshot["window_start"] = windowStart
		ruleSnapshot["window_end"] = windowEnd
		ruleSnapshot["turnover_timezone"] = "Asia/Shanghai"
		sourceSnapshot := map[string]any{"invoice_id": invoiceID, "invoice": invoiceSnapshot, "item": item.Snapshot, "invoice_item_id": item.InvoiceItemID, "order_item_id": item.OrderItemID, "service_id": item.ServiceID, "product_id": item.ProductID, "basis_cents": item.BasisCents, "description": item.Description}
		ruleJSON, _ := json.Marshal(ruleSnapshot)
		sourceJSON, _ := json.Marshal(sourceSnapshot)
		key := fmt.Sprintf("affiliate:invoice:%d:payment:%d:item:%d:%s:rule:%d:version:%d", invoiceID, paymentTransactionID, item.SourceID(), item.EventType, ruleSetID, ruleVersion)
		var entryID int64
		if err = tx.QueryRow(ctx, `INSERT INTO affiliate_commission_entries(account_id,referred_user_id,order_id,invoice_id,order_item_id,invoice_item_id,payment_transaction_id,product_id,event_type,status,amount_cents,basis_cents,rate_bps,available_at,rule_snapshot,source_snapshot,idempotency_key)
		VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,0),$8,$9,'pending',$10,$11,$12,now()+($13::int * interval '1 day'),$14,$15,$16)
ON CONFLICT (idempotency_key) DO NOTHING RETURNING id`, accountID, referredUser, nullableAffiliateID(item.OrderID), invoiceID, nullableAffiliateID(item.OrderItemID), nullableAffiliateID(item.InvoiceItemID), paymentTransactionID, item.ProductID, item.EventType, amount, item.BasisCents, rule.RateBPS, holdDays, ruleJSON, sourceJSON, key).Scan(&entryID); err != nil {
			if err == pgx.ErrNoRows {
				continue
			}
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO affiliate_commission_events(entry_id,event_type,delta_cents,before_status,after_status,reason,idempotency_key) VALUES($1,'created',$2,NULL,'pending','payment settlement',$3)`, entryID, amount, key+":created")
		if err != nil {
			return err
		}
	}
	return nil
}

type affiliateInvoiceItem struct {
	OrderID, OrderItemID, InvoiceItemID, ServiceID, ProductID, BasisCents int64
	Description, EventType                                                string
	Snapshot                                                              json.RawMessage
}

func (i affiliateInvoiceItem) SourceID() int64 {
	if i.InvoiceItemID > 0 {
		return i.InvoiceItemID
	}
	return i.OrderItemID
}

func affiliateInvoiceItems(ctx context.Context, tx pgx.Tx, invoiceID int64) ([]affiliateInvoiceItem, error) {
	var orderID *int64
	if err := tx.QueryRow(ctx, "SELECT order_id FROM invoices WHERE id=$1", invoiceID).Scan(&orderID); err != nil {
		return nil, err
	}
	items := []affiliateInvoiceItem{}
	if orderID != nil {
		rows, err := tx.Query(ctx, `WITH lines AS (
 SELECT oi.*,sum(total_cents) OVER (ORDER BY id) AS cumulative,
 sum(total_cents) OVER () AS gross_total FROM order_items oi WHERE oi.order_id=$1
)
SELECT oi.order_id,oi.id,oi.product_id,
(floor(least(current.amount_cents::numeric,oi.gross_total)*oi.cumulative/nullif(oi.gross_total,0))
 -floor(least(current.amount_cents::numeric,oi.gross_total)*(oi.cumulative-oi.total_cents)/nullif(oi.gross_total,0)))::bigint,
oi.product_name,
CASE WHEN EXISTS (
  SELECT 1 FROM invoices prior JOIN order_items prior_item ON prior_item.order_id=prior.order_id
  WHERE prior.user_id=current.user_id AND prior.status='paid' AND prior.id<>current.id
    AND prior_item.product_id=oi.product_id
) THEN 'reorder' ELSE 'first_purchase' END,
to_jsonb(oi)-'purchase_fields'-'cumulative'-'gross_total'
FROM lines oi JOIN invoices current ON current.order_id=oi.order_id
WHERE oi.order_id=$1 ORDER BY oi.id`, *orderID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var item affiliateInvoiceItem
			var basis *int64
			if err = rows.Scan(&item.OrderID, &item.OrderItemID, &item.ProductID, &basis, &item.Description, &item.EventType, &item.Snapshot); err != nil {
				return nil, err
			}
			if basis != nil {
				item.BasisCents = *basis
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}
	rows, err := tx.Query(ctx, `SELECT ii.id,ii.amount_cents,ii.description,ii.service_id,s.product_id,ii.item_type,to_jsonb(ii)
FROM invoice_items ii JOIN services s ON s.id=ii.service_id JOIN invoices i ON i.id=ii.invoice_id
WHERE ii.invoice_id=$1 AND ii.item_type IN ('renew','upgrade') AND s.user_id=i.user_id ORDER BY ii.id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item affiliateInvoiceItem
		if err = rows.Scan(&item.InvoiceItemID, &item.BasisCents, &item.Description, &item.ServiceID, &item.ProductID, &item.EventType, &item.Snapshot); err != nil {
			return nil, err
		}
		if item.EventType == "renew" {
			item.EventType = "renewal"
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type affiliateRule struct {
	RateBPS     int    `json:"rate_bps"`
	AmountCents int64  `json:"amount_cents"`
	MaxCents    *int64 `json:"max_cents"`
}

func affiliateRuleForItem(ctx context.Context, tx pgx.Tx, ruleSetID, productID int64, eventType string, defaults map[string]affiliateRule) (affiliateRule, string, error) {
	var rule affiliateRule
	var mode string
	var max *int64
	err := tx.QueryRow(ctx, "SELECT mode,rate_bps,amount_cents,max_cents FROM affiliate_product_rules WHERE rule_set_id=$1 AND product_id=$2 AND event_type=$3", ruleSetID, productID, eventType).Scan(&mode, &rule.RateBPS, &rule.AmountCents, &max)
	if err == nil {
		if mode == "disabled" {
			return affiliateRule{}, "disabled", nil
		}
		rule.MaxCents = max
		if mode == "override" {
			return rule, "product", nil
		}
	} else if err != pgx.ErrNoRows {
		return rule, "", err
	}
	value, ok := defaults[eventType]
	if !ok {
		return affiliateRule{}, "default", nil
	}
	return value, "default", nil
}

func affiliateCommissionAmount(basis int64, rule affiliateRule) (int64, error) {
	if rule.RateBPS < 0 || rule.RateBPS > 10000 || rule.AmountCents < 0 || (rule.RateBPS != 0 && rule.AmountCents != 0) || (rule.MaxCents != nil && *rule.MaxCents <= 0) {
		return 0, conflict("invalid_affiliate_rule", "推广计佣规则无效")
	}
	if basis <= 0 {
		return 0, nil
	}
	if rule.RateBPS == 0 {
		return rule.AmountCents, nil
	}
	// Split the product so a valid bigint basis never overflows at 10000 bps.
	return (basis/10000)*int64(rule.RateBPS) + (basis%10000)*int64(rule.RateBPS)/10000, nil
}
func nullableAffiliateID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (s *Store) ListAdminAffiliateAccounts(ctx context.Context, limit, offset int) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT a.id,a.user_id,u.username,u.email,a.account_type,a.status,a.program_id,p.name,a.level,a.version,a.created_at,a.updated_at
FROM affiliate_accounts a JOIN users u ON u.id=a.user_id JOIN affiliate_programs p ON p.id=a.program_id ORDER BY a.id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToMap)
}

func (s *Store) ListAdminAffiliateAttributions(ctx context.Context, limit, offset int) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT at.id,at.user_id,u.username,at.account_id,a.user_id AS affiliate_user_id,au.username AS affiliate_username,at.link_id,at.first_clicked_at,at.registered_at,at.status
FROM affiliate_attributions at JOIN users u ON u.id=at.user_id JOIN affiliate_accounts a ON a.id=at.account_id JOIN users au ON au.id=a.user_id ORDER BY at.id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToMap)
}
