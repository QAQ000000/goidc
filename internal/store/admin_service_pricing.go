package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const adminServicePricingScope = "admin:service.pricing.set"
const adminFixedRenewalScope = "admin:service.renewal.fixed.create"

type AdminServicePricingInput struct {
	ExpectedVersion  int64  `json:"expected_version"`
	Mode             string `json:"mode"`
	FixedAmountCents *int64 `json:"fixed_amount_cents"`
	QuoteHash        string `json:"quote_hash"`
	Reason           string `json:"reason"`
}

type AdminServicePricingQuote struct {
	CustomerID            int64      `json:"customer_id"`
	ServiceID             int64      `json:"service_id"`
	ServiceVersion        int64      `json:"service_version"`
	Mode                  string     `json:"mode"`
	FixedAmountCents      *int64     `json:"fixed_amount_cents"`
	ContractBaseCents     int64      `json:"contract_base_cents"`
	ContractDiscountCents int64      `json:"contract_discount_cents"`
	ContractAmountCents   int64      `json:"contract_amount_cents"`
	AmountCents           int64      `json:"amount_cents"`
	BillingCycle          string     `json:"billing_cycle"`
	CurrentDueAt          *time.Time `json:"current_due_at"`
	ExpiresAt             time.Time  `json:"expires_at"`
	QuoteHash             string     `json:"quote_hash"`
}

type AdminServicePricingResult struct {
	AdminServicePricingQuote
	Version  int64 `json:"version"`
	Replayed bool  `json:"replayed"`
}

type AdminServiceFixedRenewalInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	AmountCents     *int64 `json:"amount_cents"`
	QuoteHash       string `json:"quote_hash"`
	Reason          string `json:"reason"`
}

func validAdminPricingReason(reason string) bool {
	return reason != "" && utf8.ValidString(reason) && utf8.RuneCountInString(reason) <= 240 && !strings.ContainsRune(reason, 0)
}

func validAdminPricingHash(hash string) bool {
	raw, err := hex.DecodeString(hash)
	return err == nil && len(raw) == 32 && hash == strings.ToLower(hash)
}

func validAdminPricingMode(in AdminServicePricingInput) bool {
	return in.Mode == "contract" && in.FixedAmountCents == nil || in.Mode == "fixed" && in.FixedAmountCents != nil && *in.FixedAmountCents >= 0
}

// This lock order is shared with explicit renewal: authority, customer, service,
// and finally the original credential after all potentially blocking waits.
func lockAdminPricingTerms(ctx context.Context, tx pgx.Tx, actor, service int64, permissions ...string) (renewalTerms, bool, error) {
	var owner int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1", service).Scan(&owner); err != nil {
		return renewalTerms{}, false, err
	}
	if err := lockAdminRenewalAuthority(ctx, tx, actor, owner, permissions...); err != nil {
		return renewalTerms{}, false, err
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer' FOR SHARE", owner).Scan(&active); err != nil {
		return renewalTerms{}, false, err
	}
	terms, err := lockRenewalTerms(ctx, tx, service)
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

func requireAdminPricingIdle(ctx context.Context, tx pgx.Tx, service int64) error {
	var reason string
	if err := tx.QueryRow(ctx, "SELECT service_manual_pricing_reason($1)", service).Scan(&reason); err != nil {
		return err
	}
	if reason != "" {
		return conflict(reason, "服务存在未完成业务、退款冻结或不支持修改，请先处理后重新报价")
	}
	return nil
}

func buildAdminServicePricingQuote(ctx context.Context, tx pgx.Tx, service int64, terms renewalTerms, in AdminServicePricingInput) (AdminServicePricingQuote, error) {
	out := AdminServicePricingQuote{CustomerID: terms.CustomerID, ServiceID: service, ServiceVersion: terms.Version, Mode: in.Mode, FixedAmountCents: in.FixedAmountCents, BillingCycle: terms.Cycle, CurrentDueAt: terms.Due}
	// Read the preserved purchase contract, never the effective override returned
	// by lockRenewalTerms. Restoring it also preserves consumed benefit ordinals.
	var benefit RenewalBenefit
	if err := tx.QueryRow(ctx, `SELECT coalesce(s.recurring_price_cents,oi.price_cents,p.price_cents),s.renewal_discount_percent,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used,s.renewal_promo_code
FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1`, service).
		Scan(&out.ContractBaseCents, &benefit.Percent, &benefit.FixedCents, &benefit.Limit, &benefit.Used, &benefit.Code); err != nil {
		return out, err
	}
	discount, _, _, err := benefit.Quote(out.ContractBaseCents)
	if err != nil {
		return out, err
	}
	out.ContractDiscountCents, out.ContractAmountCents = discount, out.ContractBaseCents-discount
	out.AmountCents = out.ContractAmountCents
	if in.Mode == "fixed" {
		out.AmountCents = *in.FixedAmountCents
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return out, err
	}
	out.ExpiresAt = time.Unix((now.Unix()/300+1)*300, 0).UTC()
	out.QuoteHash = accountBusinessHash(struct {
		Scope   string
		Quote   AdminServicePricingQuote
		Benefit RenewalBenefit
	}{adminServicePricingScope, out, benefit})
	return out, nil
}

func (s *Store) AdminServicePricingQuote(ctx context.Context, actor, service int64, in AdminServicePricingInput) (AdminServicePricingQuote, error) {
	var out AdminServicePricingQuote
	if actor < 1 || service < 1 || in.ExpectedVersion < 1 || !validAdminPricingMode(in) || in.QuoteHash != "" || in.Reason != "" {
		return out, &AccountBusinessError{400, "invalid_service_pricing", "定价模式、固定应付或版本无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	terms, active, err := lockAdminPricingTerms(ctx, tx, actor, service, "services.view", "services.pricing")
	if err != nil {
		return out, err
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = validateAdminRenewalVersion(terms, in.ExpectedVersion, active); err != nil {
		return out, err
	}
	if err = requireAdminPricingIdle(ctx, tx, service); err != nil {
		return out, err
	}
	out, err = buildAdminServicePricingQuote(ctx, tx, service, terms, in)
	if err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) AdminSetServicePricing(ctx context.Context, actor, service int64, in AdminServicePricingInput, key string) (AdminServicePricingResult, error) {
	var out AdminServicePricingResult
	in.Reason = strings.TrimSpace(in.Reason)
	if actor < 1 || service < 1 || in.ExpectedVersion < 1 || !validAdminPricingMode(in) || !validAdminPricingReason(in.Reason) || !validAdminPricingHash(in.QuoteHash) || len(key) < 8 || len(key) > 128 {
		return out, &AccountBusinessError{400, "invalid_service_pricing", "定价模式、金额、版本、报价、原因或幂等键无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	terms, active, err := lockAdminPricingTerms(ctx, tx, actor, service, "services.view", "services.pricing")
	if err != nil {
		return out, err
	}
	hash := accountBusinessHash(struct {
		Customer, Service int64
		Input             AdminServicePricingInput
	}{terms.CustomerID, service, in})
	replay, err := ReplayOperation(ctx, tx, actor, adminServicePricingScope, key, hash, &out)
	if err != nil {
		return out, adminPricingReplayError(ctx, tx, actor, err)
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	if replay {
		out.Replayed = true
		return out, commitAdminRenewal(ctx, tx, actor)
	}
	if err = validateAdminRenewalVersion(terms, in.ExpectedVersion, active); err != nil {
		return out, err
	}
	if err = requireAdminPricingIdle(ctx, tx, service); err != nil {
		return out, err
	}
	quote, err := buildAdminServicePricingQuote(ctx, tx, service, terms, in)
	if err != nil {
		return out, err
	}
	if quote.QuoteHash != in.QuoteHash {
		return out, conflict("stale_pricing_quote", "定价报价已过期或变化，请重新报价")
	}
	var before *int64
	if err = tx.QueryRow(ctx, "SELECT (SELECT fixed_amount_cents FROM service_renewal_pricing WHERE service_id=$1)", service).Scan(&before); err != nil {
		return out, err
	}
	if in.Mode == "contract" {
		_, err = tx.Exec(ctx, "DELETE FROM service_renewal_pricing WHERE service_id=$1", service)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO service_renewal_pricing(service_id,fixed_amount_cents) VALUES($1,$2)
ON CONFLICT(service_id) DO UPDATE SET fixed_amount_cents=excluded.fixed_amount_cents
WHERE service_renewal_pricing.fixed_amount_cents IS DISTINCT FROM excluded.fixed_amount_cents`, service, *in.FixedAmountCents)
	}
	if err != nil {
		return out, err
	}
	out.AdminServicePricingQuote = quote
	if err = tx.QueryRow(ctx, "SELECT version FROM services WHERE id=$1", service).Scan(&out.Version); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "service.pricing.updated", "service", service, map[string]any{"customer_id": terms.CustomerID, "before_fixed_amount_cents": before, "mode": in.Mode, "fixed_amount_cents": in.FixedAmountCents, "contract_base_cents": quote.ContractBaseCents, "contract_amount_cents": quote.ContractAmountCents, "reason": in.Reason, "before_version": terms.Version, "version": out.Version}); err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, adminServicePricingScope, key, hash, out); err != nil {
		return out, err
	}
	if err = requireAdminPricingQuoteCurrent(ctx, tx, quote.ExpiresAt, "stale_pricing_quote"); err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

func adminPricingReplayError(ctx context.Context, tx pgx.Tx, actor int64, err error) error {
	var business *Conflict
	if errors.As(err, &business) {
		if authorityErr := checkAdminRenewalCredential(ctx, tx, actor); authorityErr != nil {
			return authorityErr
		}
	}
	return err
}

func requireAdminPricingQuoteCurrent(ctx context.Context, tx pgx.Tx, expires time.Time, code string) error {
	var current bool
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()<$1", expires).Scan(&current); err != nil {
		return err
	}
	if !current {
		return conflict(code, "报价已过期，请重新报价")
	}
	return nil
}

// Fixed applies to this invoice only, including zero-price unpaid invoices. It
// never consumes a promotional ordinal or modifies the preserved contract.
func buildAdminFixedRenewalQuote(ctx context.Context, tx pgx.Tx, service int64, terms renewalTerms, pending, amount int64, snapshot []byte) (AdminServiceRenewalQuote, error) {
	terms.BaseCents, terms.Benefit = amount, RenewalBenefit{}
	out, err := buildAdminRenewalQuote(ctx, tx, service, terms, pending, snapshot)
	if err == nil && out.CanCreate {
		out.QuoteHash = accountBusinessHash(struct {
			Scope, Quote string
			Amount       int64
		}{adminFixedRenewalScope, out.QuoteHash, amount})
	}
	return out, err
}

func (s *Store) AdminFixedRenewalQuote(ctx context.Context, actor, service, expectedVersion, amount int64) (AdminServiceRenewalQuote, error) {
	var out AdminServiceRenewalQuote
	if actor < 1 || service < 1 || expectedVersion < 1 || amount < 0 {
		return out, &AccountBusinessError{400, "invalid_fixed_renewal", "服务、版本或单次固定应付无效"}
	}
	out, prepared, err := s.adminFixedRenewalStep(ctx, actor, service, expectedVersion, amount, nil, "", nil)
	if err != nil || prepared == nil {
		return out, err
	}
	prepared.request, err = prepared.adapter.QuoteRenewal(ctx, prepared.snapshot.RenewalRequest)
	if err != nil {
		if authorityErr := s.recheckAdminFixedRenewalAuthority(ctx, actor, service); authorityErr != nil {
			return out, authorityErr
		}
		return out, supplierReadError(err)
	}
	out, _, err = s.adminFixedRenewalStep(ctx, actor, service, expectedVersion, amount, nil, "", prepared)
	return out, err
}

func (s *Store) recheckAdminFixedRenewalAuthority(ctx context.Context, actor, service int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, _, err = lockAdminPricingTerms(ctx, tx, actor, service, "services.view", "invoices.view", "services.renew", "services.renew.override"); err != nil {
		return err
	}
	return commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) AdminCreateFixedRenewalInvoice(ctx context.Context, actor, service int64, in AdminServiceFixedRenewalInput, key string) (AdminServiceRenewalResult, error) {
	var out AdminServiceRenewalResult
	in.Reason = strings.TrimSpace(in.Reason)
	if actor < 1 || service < 1 || in.ExpectedVersion < 1 || in.AmountCents == nil || *in.AmountCents < 0 || !validAdminPricingHash(in.QuoteHash) || !validAdminPricingReason(in.Reason) || len(key) < 8 || len(key) > 128 {
		return out, &AccountBusinessError{400, "invalid_fixed_renewal", "服务、版本、单次固定应付、报价、原因或幂等键无效"}
	}
	_, prepared, err := s.adminFixedRenewalStep(ctx, actor, service, in.ExpectedVersion, *in.AmountCents, &adminFixedRenewalSubmission{Input: in, Result: &out}, key, nil)
	if err != nil || prepared == nil {
		return out, err
	}
	prepared.request, err = prepared.adapter.QuoteRenewal(ctx, prepared.snapshot.RenewalRequest)
	if err != nil {
		if authorityErr := s.recheckAdminFixedRenewalAuthority(ctx, actor, service); authorityErr != nil {
			return out, authorityErr
		}
		return out, supplierReadError(err)
	}
	_, _, err = s.adminFixedRenewalStep(ctx, actor, service, in.ExpectedVersion, *in.AmountCents, &adminFixedRenewalSubmission{Input: in, Result: &out}, key, prepared)
	return out, err
}

type adminFixedRenewalSubmission struct {
	Input  AdminServiceFixedRenewalInput
	Result *AdminServiceRenewalResult
}

func (s *Store) adminFixedRenewalStep(ctx context.Context, actor, service, version, amount int64, submission *adminFixedRenewalSubmission, key string, prepared *supplierRenewalQuote) (AdminServiceRenewalQuote, *supplierRenewalQuote, error) {
	var out AdminServiceRenewalQuote
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback(ctx)
	terms, active, err := lockAdminPricingTerms(ctx, tx, actor, service, "services.view", "invoices.view", "services.renew", "services.renew.override")
	if err != nil {
		return out, nil, err
	}
	var hash string
	if submission != nil {
		hash = accountBusinessHash(struct {
			Customer, Service int64
			Input             AdminServiceFixedRenewalInput
		}{terms.CustomerID, service, submission.Input})
		replay, replayErr := ReplayOperation(ctx, tx, actor, adminFixedRenewalScope, key, hash, submission.Result)
		if replayErr != nil {
			return out, nil, adminPricingReplayError(ctx, tx, actor, replayErr)
		}
		if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
			return out, nil, err
		}
		if replay {
			submission.Result.Replayed = true
			return out, nil, commitAdminRenewal(ctx, tx, actor)
		}
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
	pending, err := requireRenewalAdmission(ctx, tx, service, terms, submission == nil)
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
	out, err = buildAdminFixedRenewalQuote(ctx, tx, service, terms, pending, amount, snapshot)
	if err != nil || submission == nil {
		if err != nil {
			return out, nil, err
		}
		return out, nil, commitAdminRenewal(ctx, tx, actor)
	}
	if out.QuoteHash != submission.Input.QuoteHash {
		return out, nil, conflict("stale_renewal_quote", "单次固定续费报价已过期或变化，请重新报价")
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, nil, err
	}
	terms.BaseCents, terms.Benefit = amount, RenewalBenefit{}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", actor, adminFixedRenewalScope, key)))
	result := submission.Result
	result.RenewalResult, err = insertRenewalInvoice(ctx, tx, terms.CustomerID, service, "admin-fixed-renew:"+hex.EncodeToString(sum[:]), terms, snapshot, actor, submission.Input.Reason)
	if err != nil {
		return out, nil, err
	}
	result.CustomerID, result.Status = terms.CustomerID, "unpaid"
	if err = tx.QueryRow(ctx, "SELECT version FROM invoices WHERE id=$1", result.InvoiceID).Scan(&result.Version); err != nil {
		return out, nil, err
	}
	if err = Audit(ctx, tx, actor, "service.renewal.fixed", "service", service, map[string]any{"customer_id": terms.CustomerID, "invoice_id": result.InvoiceID, "amount_cents": amount, "reason": submission.Input.Reason}); err != nil {
		return out, nil, err
	}
	if err = SaveOperation(ctx, tx, actor, adminFixedRenewalScope, key, hash, result); err != nil {
		return out, nil, err
	}
	if err = requireAdminPricingQuoteCurrent(ctx, tx, out.ExpiresAt, "stale_renewal_quote"); err != nil {
		return out, nil, err
	}
	return out, nil, commitAdminRenewal(ctx, tx, actor)
}

// A fixed final price cannot silently turn into a differently configured
// contract. Restore contract mode explicitly before quoting a service change.
func requireContractRenewalPricing(ctx context.Context, tx pgx.Tx, service int64) error {
	var fixed bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM service_renewal_pricing WHERE service_id=$1)", service).Scan(&fixed); err != nil {
		return err
	}
	if fixed {
		return conflict("fixed_service_pricing", "服务使用固定续费应付，请先恢复合同定价后再变更商品、配置或账期")
	}
	return nil
}
