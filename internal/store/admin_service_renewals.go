package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const adminRenewalCreateScope = "admin:service.renewal.create"

// These commands deliberately distinguish creating a bill from spending the
// customer's money. Neither quote nor create enables automatic renewal.
type AdminServiceRenewalInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	QuoteHash       string `json:"quote_hash"`
	Reason          string `json:"reason"`
}

type AdminServiceRenewalQuote struct {
	CustomerID       int64      `json:"customer_id"`
	ServiceID        int64      `json:"service_id"`
	ServiceVersion   int64      `json:"service_version"`
	BillingCycle     string     `json:"billing_cycle"`
	BaseCents        int64      `json:"base_cents"`
	DiscountCents    int64      `json:"discount_cents"`
	AmountCents      int64      `json:"amount_cents"`
	CurrentDueAt     *time.Time `json:"current_due_at"`
	PendingInvoiceID int64      `json:"pending_invoice_id"`
	CanCreate        bool       `json:"can_create"`
	ExpiresAt        time.Time  `json:"expires_at"`
	QuoteHash        string     `json:"quote_hash"`
}

type AdminServiceRenewalResult struct {
	RenewalResult
	CustomerID int64  `json:"customer_id"`
	Version    int64  `json:"version"`
	Status     string `json:"status"`
}

func adminRenewalCreateRequestHash(customer, service int64, in AdminServiceRenewalInput) string {
	return accountBusinessHash(struct {
		CustomerID, ServiceID int64
		Input                 AdminServiceRenewalInput
	}{customer, service, in})
}

// Authority takes principal/role locks before any business or credential lock.
// Password changes use that same principal -> credential order. The original
// HTTP credential is checked here without locking and is locked only after
// service/invoice waits, so a committed revocation remains observable.
func lockAdminRenewalAuthority(ctx context.Context, tx pgx.Tx, actor, customer int64, permissions ...string) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return conflict("unauthorized", "该操作需要有效的原始会话或 API 密钥")
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	for _, permission := range permissions {
		if err := RequireAdministratorPermission(ctx, tx, actor, permission); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return conflict("unauthorized", "登录已失效，请重新登录")
			}
			return err
		}
	}
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	return checkAdminRenewalCredential(ctx, tx, actor)
}

func checkAdminRenewalCredential(ctx context.Context, tx pgx.Tx, actor int64) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return conflict("unauthorized", "该操作需要有效的原始会话或 API 密钥")
	}
	query := `SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp())`
	if credential.APIKey {
		query = `SELECT EXISTS(SELECT 1 FROM api_keys WHERE secret_hash=$1 AND user_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()))`
	}
	var valid bool
	if err := tx.QueryRow(ctx, query, credential.Hash, actor).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return conflict("unauthorized", "登录已失效，请重新登录")
	}
	return nil
}

func lockAdminRenewalCredential(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := checkAdminRenewalCredential(ctx, tx, actor); err != nil {
		return err
	}
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	// clock_timestamp must be evaluated again after a credential lock wait.
	return checkAdminRenewalCredential(ctx, tx, actor)
}

func commitAdminRenewal(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) lockAdminRenewalTerms(ctx context.Context, tx pgx.Tx, actor, serviceID int64) (renewalTerms, bool, error) {
	var owner int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1", serviceID).Scan(&owner); err != nil {
		return renewalTerms{}, false, err
	}
	if err := requireAdminRenewalBatchCustomer(ctx, owner); err != nil {
		return renewalTerms{}, false, err
	}
	if err := lockAdminRenewalAuthority(ctx, tx, actor, owner, "services.view", "invoices.view", "services.renew"); err != nil {
		return renewalTerms{}, false, err
	}
	// Customer before service matches finance's existing customer -> invoice ->
	// service NOWAIT order, and keeps a concurrent account stop authoritative.
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer' FOR SHARE", owner).Scan(&active); err != nil {
		return renewalTerms{}, false, err
	}
	terms, err := lockRenewalTerms(ctx, tx, serviceID)
	if err != nil {
		return terms, false, err
	}
	if terms.CustomerID != owner {
		return terms, false, conflict("service_owner_changed", "服务客户已变更，请刷新后重试")
	}
	if err = checkAdminRenewalCredential(ctx, tx, actor); err != nil {
		return terms, false, err
	}
	return terms, active, nil
}

func requireAdminRenewalNoPendingJobs(ctx context.Context, tx pgx.Tx, service int64) error {
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND
status IN ('pending','running','waiting_manual','waiting_verification'))`, service).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return conflict("service_action_pending", "服务存在未完成或待核验任务，请先处理原操作")
	}
	return nil
}

func validateAdminRenewalVersion(terms renewalTerms, expected int64, active bool) error {
	if !active {
		return conflict("customer_inactive", "客户已停用，不能代建续费账单")
	}
	if terms.Version != expected {
		return conflict("stale_service", "服务已更新，请刷新后重新报价")
	}
	return nil
}

// buildAdminRenewalQuote gives the displayed expiry authority without accepting
// a caller-controlled timestamp. Five-minute windows end on a database-clock
// boundary; submission recomputes both the window and every pricing snapshot.
func buildAdminRenewalQuote(ctx context.Context, tx pgx.Tx, service int64, terms renewalTerms, pending int64, supplierSnapshot []byte) (AdminServiceRenewalQuote, error) {
	out := AdminServiceRenewalQuote{CustomerID: terms.CustomerID, ServiceID: service, ServiceVersion: terms.Version, BillingCycle: terms.Cycle, BaseCents: terms.BaseCents, CurrentDueAt: terms.Due, PendingInvoiceID: pending, CanCreate: pending == 0}
	discount, _, _, err := terms.Benefit.Quote(terms.BaseCents)
	if err != nil {
		return out, err
	}
	out.DiscountCents, out.AmountCents = discount, terms.BaseCents-discount
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return out, err
	}
	out.ExpiresAt = time.Unix((now.Unix()/300+1)*300, 0).UTC()
	if !out.CanCreate {
		return out, nil
	}
	raw, err := json.Marshal(struct {
		Quote    AdminServiceRenewalQuote
		Terms    renewalTerms
		Supplier json.RawMessage
	}{out, terms, supplierSnapshot})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.QuoteHash = hex.EncodeToString(sum[:])
	return out, nil
}

// Existing unpaid invoices are displayed for deliberate payment selection; a
// preview never replaces them, creates a second bill or starts collection.
func (s *Store) AdminRenewalQuote(ctx context.Context, actor, serviceID, expectedVersion int64) (AdminServiceRenewalQuote, error) {
	if actor < 1 || serviceID < 1 || expectedVersion < 1 {
		return AdminServiceRenewalQuote{}, &AccountBusinessError{400, "invalid_renewal", "服务或服务版本无效"}
	}
	out, prepared, err := s.adminRenewalQuoteStep(ctx, actor, serviceID, expectedVersion, nil)
	if err != nil || prepared == nil {
		return out, err
	}
	prepared.request, err = prepared.adapter.QuoteRenewal(ctx, prepared.snapshot.RenewalRequest)
	if err != nil {
		if authorityErr := s.recheckAdminRenewalReadAuthority(ctx, actor, serviceID); authorityErr != nil {
			return out, authorityErr
		}
		return out, supplierReadError(err)
	}
	out, _, err = s.adminRenewalQuoteStep(ctx, actor, serviceID, expectedVersion, prepared)
	return out, err
}

// Failed reads still cross the same authorization boundary: a revoked original
// credential is never answered using the preflight's former authority.
func (s *Store) recheckAdminRenewalReadAuthority(ctx context.Context, actor, service int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, _, err = s.lockAdminRenewalTerms(ctx, tx, actor, service); err != nil {
		return err
	}
	return commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) adminRenewalQuoteStep(ctx context.Context, actor, service, version int64, prepared *supplierRenewalQuote) (AdminServiceRenewalQuote, *supplierRenewalQuote, error) {
	var out AdminServiceRenewalQuote
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback(ctx)
	terms, active, err := s.lockAdminRenewalTerms(ctx, tx, actor, service)
	if err != nil {
		return out, nil, err
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, nil, err
	}
	if err = validateAdminRenewalVersion(terms, version, active); err != nil {
		return out, nil, err
	}
	if err = requireAdminRenewalNoPendingJobs(ctx, tx, service); err != nil {
		return out, nil, err
	}
	pending, err := requireRenewalAdmission(ctx, tx, service, terms, true)
	if err != nil {
		return out, nil, err
	}
	snapshot := []byte(`{}`)
	if pending == 0 {
		var next *supplierRenewalQuote
		snapshot, next, err = s.adminRenewalSupplierSnapshot(ctx, tx, service, terms, prepared)
		if err != nil {
			return out, nil, err
		}
		if next != nil {
			return out, next, commitAdminRenewal(ctx, tx, actor)
		}
	}
	out, err = buildAdminRenewalQuote(ctx, tx, service, terms, pending, snapshot)
	if err != nil {
		return out, nil, err
	}
	return out, nil, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) adminRenewalSupplierSnapshot(ctx context.Context, tx pgx.Tx, service int64, terms renewalTerms, prepared *supplierRenewalQuote) ([]byte, *supplierRenewalQuote, error) {
	if !terms.SupplierMode {
		if prepared != nil {
			return nil, nil, conflict("supplier_renewal_quote_changed", "服务交付方式已变更，请重新获取续费报价")
		}
		return []byte(`{}`), nil, nil
	}
	adapter, snapshot, err := s.requireSupplierRenewal(ctx, tx, service)
	if err != nil {
		return nil, nil, err
	}
	if prepared == nil {
		return nil, &supplierRenewalQuote{adapter: adapter, snapshot: snapshot, serviceVersion: terms.Version, due: terms.Due}, nil
	}
	sameDue := terms.Due == nil && prepared.due == nil || terms.Due != nil && prepared.due != nil && terms.Due.Equal(*prepared.due)
	if terms.Version != prepared.serviceVersion || !sameDue || snapshot != prepared.snapshot {
		return nil, nil, conflict("supplier_renewal_quote_changed", "服务或供应商配置已变更，请重新获取续费报价")
	}
	snapshot.RenewalRequest = prepared.request
	raw, err := json.Marshal(snapshot)
	return raw, nil, err
}

func (s *Store) AdminCreateRenewalInvoice(ctx context.Context, actor, serviceID int64, in AdminServiceRenewalInput, key string) (AdminServiceRenewalResult, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	decoded, hashErr := hex.DecodeString(in.QuoteHash)
	if actor < 1 || serviceID < 1 || in.ExpectedVersion < 1 || hashErr != nil || len(decoded) != 32 || in.QuoteHash != strings.ToLower(in.QuoteHash) || len(key) < 8 || len(key) > 128 || in.Reason == "" || !utf8.ValidString(in.Reason) || utf8.RuneCountInString(in.Reason) > 240 || strings.ContainsAny(in.Reason, "\x00") {
		return AdminServiceRenewalResult{}, &AccountBusinessError{400, "invalid_renewal", "服务、版本、报价、原因或幂等键无效"}
	}
	out, prepared, err := s.adminCreateRenewalInvoiceStep(ctx, actor, serviceID, in, key, nil)
	if err != nil || prepared == nil {
		return out, err
	}
	prepared.request, err = prepared.adapter.QuoteRenewal(ctx, prepared.snapshot.RenewalRequest)
	if err != nil {
		if authorityErr := s.recheckAdminRenewalReadAuthority(ctx, actor, serviceID); authorityErr != nil {
			return out, authorityErr
		}
		return out, supplierReadError(err)
	}
	out, _, err = s.adminCreateRenewalInvoiceStep(ctx, actor, serviceID, in, key, prepared)
	return out, err
}

func (s *Store) adminCreateRenewalInvoiceStep(ctx context.Context, actor, service int64, in AdminServiceRenewalInput, key string, prepared *supplierRenewalQuote) (AdminServiceRenewalResult, *supplierRenewalQuote, error) {
	var out AdminServiceRenewalResult
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback(ctx)
	terms, active, err := s.lockAdminRenewalTerms(ctx, tx, actor, service)
	if err != nil {
		return out, nil, err
	}
	requestHash := adminRenewalCreateRequestHash(terms.CustomerID, service, in)
	replayed, err := ReplayOperation(ctx, tx, actor, adminRenewalCreateScope, key, requestHash, &out)
	if err != nil {
		// An application-level hash conflict can follow a long receipt wait.
		// Recheck authority before revealing that result, while preserving SQL
		// errors from an aborted transaction rather than masking them.
		var business *Conflict
		if errors.As(err, &business) {
			if authorityErr := checkAdminRenewalCredential(ctx, tx, actor); authorityErr != nil {
				return out, nil, authorityErr
			}
		}
		return out, nil, err
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, nil, err
	}
	if replayed {
		out.Replayed = true
		return out, nil, commitAdminRenewal(ctx, tx, actor)
	}
	if err = validateAdminRenewalVersion(terms, in.ExpectedVersion, active); err != nil {
		return out, nil, err
	}
	if err = requireAdminRenewalNoPendingJobs(ctx, tx, service); err != nil {
		return out, nil, err
	}
	if _, err = requireRenewalAdmission(ctx, tx, service, terms, false); err != nil {
		return out, nil, err
	}
	snapshot, next, err := s.adminRenewalSupplierSnapshot(ctx, tx, service, terms, prepared)
	if err != nil {
		return out, nil, err
	}
	if next != nil {
		return out, next, commitAdminRenewal(ctx, tx, actor)
	}
	quote, err := buildAdminRenewalQuote(ctx, tx, service, terms, 0, snapshot)
	if err != nil {
		return out, nil, err
	}
	if quote.QuoteHash != in.QuoteHash {
		return out, nil, conflict("stale_renewal_quote", "续费报价已过期或发生变更，请重新报价")
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, nil, err
	}
	// Customer-supplied renewal keys cannot address an administrative invoice.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", actor, adminRenewalCreateScope, key)))
	internalKey := "admin-renew:" + hex.EncodeToString(sum[:])
	out.RenewalResult, err = insertRenewalInvoice(ctx, tx, terms.CustomerID, service, internalKey, terms, snapshot, actor, in.Reason)
	if err != nil {
		return out, nil, err
	}
	out.CustomerID, out.Status = terms.CustomerID, "unpaid"
	if err = tx.QueryRow(ctx, "SELECT version FROM invoices WHERE id=$1", out.InvoiceID).Scan(&out.Version); err != nil {
		return out, nil, err
	}
	if err = SaveOperation(ctx, tx, actor, adminRenewalCreateScope, key, requestHash, out); err != nil {
		return out, nil, err
	}
	var quoteCurrent bool
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()<$1", quote.ExpiresAt).Scan(&quoteCurrent); err != nil {
		return out, nil, err
	}
	if !quoteCurrent {
		return out, nil, conflict("stale_renewal_quote", "续费报价已过期，请重新报价")
	}
	return out, nil, commitAdminRenewal(ctx, tx, actor)
}
