package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type AdminRenewalPaymentInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	AmountCents     int64  `json:"amount_cents"`
	Reason          string `json:"reason"`
}

type AdminRenewalPaymentResult struct {
	PaymentResult
	ServiceID     int64  `json:"service_id"`
	CustomerID    int64  `json:"customer_id"`
	RenewalStatus string `json:"renewal_status"`
}

// The command supplies the real administrator to the shared settlement without
// impersonating the customer whose balance is being spent.
type adminRenewalPaymentCommand struct {
	Actor, ServiceID int64
	Input            AdminRenewalPaymentInput
}

const adminRenewalPaymentScope = "admin:service.renewal.pay"

func adminRenewalPaymentRequestHash(customer, service, invoice int64, in AdminRenewalPaymentInput) string {
	return accountBusinessHash(struct {
		ServiceID  int64                    `json:"service_id"`
		InvoiceID  int64                    `json:"invoice_id"`
		CustomerID int64                    `json:"customer_id"`
		Input      AdminRenewalPaymentInput `json:"input"`
	}{service, invoice, customer, in})
}

func (s *Store) AdminPayRenewalInvoice(ctx context.Context, actor, serviceID, invoiceID int64, in AdminRenewalPaymentInput, key string) (AdminRenewalPaymentResult, error) {
	var out AdminRenewalPaymentResult
	in.Reason = strings.TrimSpace(in.Reason)
	if actor < 1 || serviceID < 1 || invoiceID < 1 || in.ExpectedVersion < 1 || in.AmountCents < 0 || in.Reason == "" || !contentTextValid(in.Reason, 500, true) || len(key) < 8 || len(key) > 128 {
		return out, &AccountBusinessError{400, "invalid_admin_renewal_payment", "账单、确认金额、版本、原因或幂等键无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// Classification is deliberately unlocked: balance writers always lock the
	// customer before the invoice. Validate both associations again under locks.
	var customer, serviceCustomer int64
	if err = tx.QueryRow(ctx, "SELECT i.user_id,s.user_id FROM invoices i CROSS JOIN services s WHERE i.id=$1 AND s.id=$2", invoiceID, serviceID).Scan(&customer, &serviceCustomer); err != nil {
		return out, err
	}
	if err = requireAdminRenewalBatchCustomer(ctx, customer); err != nil {
		return out, err
	}
	if err = requireAdminRenewalBatchCustomer(ctx, serviceCustomer); err != nil {
		return out, err
	}
	if err = lockAdminRenewalAuthority(ctx, tx, actor, customer, "services.view", "invoices.view", "services.renew.pay_balance"); err != nil {
		return out, err
	}
	if serviceCustomer != customer {
		if err = LockAdminCustomer(ctx, tx, actor, serviceCustomer); err != nil {
			return out, err
		}
	}
	if err = LockFinancialCustomer(ctx, tx, customer); err != nil {
		return out, err
	}
	hash := adminRenewalPaymentRequestHash(customer, serviceID, invoiceID, in)
	replayed, err := ReplayOperation(ctx, tx, actor, adminRenewalPaymentScope, key, hash, &out)
	// The receipt lock may have waited while this exact credential was revoked
	// or naturally expired. Even a conflicting key must not answer from stale
	// authentication; leave the credential unlocked until business waits end.
	var receiptConflict *Conflict
	if err == nil || errors.As(err, &receiptConflict) {
		if authErr := checkAdminRenewalCredential(ctx, tx, actor); authErr != nil {
			return out, authErr
		}
	}
	if err != nil {
		return out, err
	}
	if replayed {
		// Current permission and credential remain mandatory, but an old invoice
		// version, consumed benefit or changed balance never invalidates a receipt.
		// Keep the existing finance order: invoice before service. NOWAIT avoids
		// a future ownership writer introducing a service -> finance reverse wait.
		var invoiceOwner, currentOwner int64
		if err = tx.QueryRow(ctx, "SELECT user_id FROM invoices WHERE id=$1 FOR SHARE", invoiceID).Scan(&invoiceOwner); err != nil {
			return out, err
		}
		if err = tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1 FOR SHARE NOWAIT", serviceID).Scan(&currentOwner); err != nil {
			return out, cancellationServiceLockError(err)
		}
		if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
			return out, err
		}
		if currentOwner != serviceCustomer {
			return out, conflict("service_owner_changed", "服务客户已变更，请重新核对历史付款")
		}
		if invoiceOwner != customer {
			return out, conflict("invoice_owner_changed", "账单客户已变更，请重新核对历史付款")
		}
		if err = LockAdminCustomer(ctx, tx, actor, currentOwner); err != nil {
			return out, err
		}
		out.Replayed = true
		return out, commitAdminRenewal(ctx, tx, actor)
	}
	command := &adminRenewalPaymentCommand{Actor: actor, ServiceID: serviceID, Input: in}
	// Ledger keys have a separate namespace from customer-supplied payment keys.
	// The durable administrator receipt remains keyed by the original actor/key.
	ledgerDigest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", actor, key)))
	ledgerKey := "admin-renewal-" + hex.EncodeToString(ledgerDigest[:])
	out.PaymentResult, err = s.payInvoiceTx(ctx, tx, customer, invoiceID, ledgerKey, false, nil, command, in.ExpectedVersion)
	if err != nil {
		return out, err
	}
	out.ServiceID, out.CustomerID = serviceID, customer
	var supplierMode bool
	if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM services WHERE id=$1", serviceID).Scan(&supplierMode); err != nil {
		return out, err
	}
	out.RenewalStatus = "completed"
	if supplierMode {
		out.RenewalStatus = "pending"
	}
	if err = SaveOperation(ctx, tx, actor, adminRenewalPaymentScope, key, hash, out); err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) validateAdminRenewalPaymentInvoice(ctx context.Context, tx pgx.Tx, customer, invoice, invoiceOwner, order int64, kind string, version, amount int64, command adminRenewalPaymentCommand) error {
	return s.validateAdminRenewalInvoiceAdmission(ctx, tx, customer, invoice, invoiceOwner, order, kind, version, amount, command, true)
}

// Manual collection still has source-identity waits after service admission.
// Keep its original credential revocable until those waits finish, while the
// established balance settlement retains its credential-locking boundary.
func (s *Store) validateAdminRenewalInvoiceAdmission(ctx context.Context, tx pgx.Tx, customer, invoice, invoiceOwner, order int64, kind string, version, amount int64, command adminRenewalPaymentCommand, lockCredential bool) error {
	var owner int64
	var status, mode, cycle string
	if err := tx.QueryRow(ctx, "SELECT user_id,status,provision_mode,billing_cycle FROM services WHERE id=$1 FOR UPDATE NOWAIT", command.ServiceID).Scan(&owner, &status, &mode, &cycle); err != nil {
		return cancellationServiceLockError(err)
	}
	// Finance and service locks can wait or lose a race. Authenticate the exact
	// original request again before exposing fresh business validation results.
	credentialCheck := checkAdminRenewalCredential
	if lockCredential {
		credentialCheck = lockAdminRenewalCredential
	}
	if err := credentialCheck(ctx, tx, command.Actor); err != nil {
		return err
	}
	var singleRenewal bool
	if err := tx.QueryRow(ctx, `SELECT $2::bigint=0 AND $3::text='automatic'
 AND (SELECT count(*) FROM invoice_items WHERE invoice_id=$1)=1
 AND EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=$1 AND item_type='renew' AND service_id=$4 AND amount_cents=$5)`, invoice, order, kind, command.ServiceID, amount).Scan(&singleRenewal); err != nil {
		return err
	}
	if !singleRenewal {
		return conflict("unsupported_admin_renewal_invoice", "只能使用客户余额支付该服务的单项续费账单")
	}
	if owner != customer || invoiceOwner != customer {
		return conflict("service_owner_changed", "服务与账单客户不一致，请刷新后重试")
	}
	if err := LockAdminCustomer(ctx, tx, command.Actor, customer); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' AND role='customer' FROM users WHERE id=$1", customer).Scan(&active); err != nil {
		return err
	}
	if !active {
		return conflict("customer_inactive", "客户已停用，不能代付续费")
	}
	if status != "active" && status != "suspended" {
		return conflict("invalid_renewal_state", "只有已启用或已暂停的服务可以续费")
	}
	if cycle == "once" {
		return conflict("nonrenewable_service", "一次性服务不可续费")
	}
	if version != command.Input.ExpectedVersion {
		return conflict("stale_invoice", "账单已更新，请刷新后重新支付")
	}
	if amount != command.Input.AmountCents {
		return conflict("renewal_amount_changed", "续费金额与确认金额不一致，请重新核对")
	}
	if err := requireServiceReviewed(ctx, tx, command.ServiceID); err != nil {
		return err
	}
	if err := requireNoLocalRenewalHold(ctx, tx, command.ServiceID); err != nil {
		return err
	}
	if err := requireNoConfigurationChange(ctx, tx, command.ServiceID); err != nil {
		return err
	}
	if err := requireServiceCancellationOpen(ctx, tx, command.ServiceID); err != nil {
		return err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1
 AND status IN ('pending','running','waiting_manual','waiting_verification'))`, command.ServiceID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return conflict("service_action_pending", "服务存在未完成或待核验任务，请先处理")
	}
	if mode == "supplier" {
		return s.requireAutomaticSupplierRenewalPayment(ctx, tx, invoice, command.ServiceID)
	}
	return nil
}
