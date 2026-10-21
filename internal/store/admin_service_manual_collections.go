package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const adminManualCollectionScope = "admin:service.renewal.collect_manual"

type AdminServiceManualCollectionInput struct {
	ExpectedVersion        int64     `json:"expected_version"`
	ExpectedServiceVersion int64     `json:"expected_service_version"`
	AmountCents            int64     `json:"amount_cents"`
	ReceivedAt             time.Time `json:"received_at"`
	Method                 string    `json:"method"`
	ReceivingAccount       string    `json:"receiving_account"`
	TransactionNo          string    `json:"transaction_no"`
	EvidenceRef            string    `json:"evidence_ref"`
	EvidenceSHA256         string    `json:"evidence_sha256"`
	Reason                 string    `json:"reason"`
	QuoteHash              string    `json:"quote_hash,omitempty"`
}

type AdminServiceManualCollectionPreview struct {
	ServiceID        int64     `json:"service_id"`
	CustomerID       int64     `json:"customer_id"`
	InvoiceID        int64     `json:"invoice_id"`
	InvoiceNo        string    `json:"invoice_no"`
	InvoiceVersion   int64     `json:"invoice_version"`
	ServiceVersion   int64     `json:"service_version"`
	AmountCents      int64     `json:"amount_cents"`
	ReceivedAt       time.Time `json:"received_at"`
	Method           string    `json:"method"`
	ReceivingAccount string    `json:"receiving_account"`
	TransactionNo    string    `json:"transaction_no"`
	QuoteHash        string    `json:"quote_hash"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type AdminServiceManualCollectionResult struct {
	ServiceID      int64     `json:"service_id"`
	CustomerID     int64     `json:"customer_id"`
	InvoiceID      int64     `json:"invoice_id"`
	InvoiceNo      string    `json:"invoice_no"`
	InvoiceVersion int64     `json:"invoice_version"`
	ServiceVersion int64     `json:"service_version"`
	PaymentID      int64     `json:"payment_id"`
	CollectionID   int64     `json:"collection_id"`
	AmountCents    int64     `json:"amount_cents"`
	ReceivedAt     time.Time `json:"received_at"`
	Status         string    `json:"status"`
	RenewalStatus  string    `json:"renewal_status"`
	Replayed       bool      `json:"replayed"`
}

type manualCollectionState struct {
	Customer, ServiceCustomer, InvoiceOwner, InvoiceVersion, ServiceVersion, Amount int64
	InvoiceNo, InvoiceStatus, Kind, Mode                                            string
	Order                                                                           int64
	CreatedAt                                                                       time.Time
	Invoice, Service, Items, Sources                                                json.RawMessage
}

func validateAdminManualCollection(in *AdminServiceManualCollectionInput, apply bool) error {
	in.Reason, in.ReceivingAccount, in.TransactionNo = strings.TrimSpace(in.Reason), strings.TrimSpace(in.ReceivingAccount), strings.TrimSpace(in.TransactionNo)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	in.ReceivedAt = in.ReceivedAt.UTC()
	if in.ExpectedVersion < 1 || in.ExpectedServiceVersion < 1 || in.AmountCents < 1 || in.ReceivedAt.IsZero() || in.ReceivedAt.Year() < 2000 || in.ReceivedAt.Year() > 9999 || in.ReceivedAt.Nanosecond()%1000 != 0 ||
		(in.Method != "bank_transfer" && in.Method != "cash" && in.Method != "other_offline") ||
		in.Reason == "" || !contentTextValid(in.Reason, 500, true) || in.EvidenceRef == "" || !contentTextValid(in.EvidenceRef, 500, false) ||
		in.ReceivingAccount == "" || !contentTextValid(in.ReceivingAccount, 120, false) || in.TransactionNo == "" || !contentTextValid(in.TransactionNo, 160, false) || !validRenewalBatchID(in.EvidenceSHA256) ||
		(apply && !validRenewalBatchID(in.QuoteHash)) || (!apply && in.QuoteHash != "") {
		return &AccountBusinessError{400, "invalid_manual_collection", "请提供账单及服务版本、正数实收金额、精确到账时间、线下来源、唯一流水、凭据摘要和原因"}
	}
	return nil
}

func lockAdminManualCollectionAuthority(ctx context.Context, tx pgx.Tx, actor, service, invoice int64) (manualCollectionState, error) {
	var out manualCollectionState
	if err := tx.QueryRow(ctx, "SELECT i.user_id,s.user_id FROM invoices i CROSS JOIN services s WHERE i.id=$1 AND s.id=$2", invoice, service).
		Scan(&out.Customer, &out.ServiceCustomer); err != nil {
		return out, err
	}
	if err := lockAdminRenewalAuthority(ctx, tx, actor, out.Customer, "services.view", "invoices.view", "invoices.collect_manual"); err != nil {
		return out, err
	}
	if out.ServiceCustomer != out.Customer {
		if err := LockAdminCustomer(ctx, tx, actor, out.ServiceCustomer); err != nil {
			return out, err
		}
	}
	if err := LockFinancialCustomer(ctx, tx, out.Customer); err != nil {
		return out, err
	}
	return out, checkAdminRenewalCredential(ctx, tx, actor)
}

func (s *Store) lockAdminManualCollectionFresh(ctx context.Context, tx pgx.Tx, actor, service, invoice int64, state *manualCollectionState, in AdminServiceManualCollectionInput) error {
	if err := tx.QueryRow(ctx, `SELECT i.user_id,i.version,i.invoice_no,i.status,i.invoice_kind,coalesce(i.order_id,0),i.amount_cents,i.created_at,external_refund_order_snapshot(i)
 FROM invoices i WHERE i.id=$1 FOR UPDATE`, invoice).Scan(&state.InvoiceOwner, &state.InvoiceVersion, &state.InvoiceNo, &state.InvoiceStatus, &state.Kind, &state.Order, &state.Amount, &state.CreatedAt, &state.Invoice); err != nil {
		return err
	}
	if err := checkAdminRenewalCredential(ctx, tx, actor); err != nil {
		return err
	}
	command := adminRenewalPaymentCommand{Actor: actor, ServiceID: service, Input: AdminRenewalPaymentInput{ExpectedVersion: in.ExpectedVersion, AmountCents: in.AmountCents, Reason: in.Reason}}
	if err := s.validateAdminRenewalInvoiceAdmission(ctx, tx, state.Customer, invoice, state.InvoiceOwner, state.Order, state.Kind, state.InvoiceVersion, state.Amount, command, false); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT version,provision_mode,external_refund_order_snapshot(s) FROM services s WHERE id=$1`, service).
		Scan(&state.ServiceVersion, &state.Mode, &state.Service); err != nil {
		return err
	}
	if state.ServiceVersion != in.ExpectedServiceVersion {
		return conflict("stale_service", "服务已更新，请重新预览人工收款")
	}
	if state.InvoiceStatus != "unpaid" {
		return conflict("invoice_not_payable", "只有未付续费账单可以登记人工收款")
	}
	if err := requireInvoiceReviewed(ctx, tx, invoice); err != nil {
		return err
	}
	if err := requireNoRealPaymentPending(ctx, tx, invoice); err != nil {
		return err
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status IN ('pending','expired','succeeded'))
 OR EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$1 AND entry_type IN ('payment','refund'))
 OR EXISTS(SELECT 1 FROM credit_invoice_payments WHERE invoice_id=$1)
 OR EXISTS(SELECT 1 FROM external_refunds WHERE invoice_id=$1 AND status<>'rejected')
 OR EXISTS(SELECT 1 FROM services s WHERE s.id=$2 AND (s.inventory_released OR external_refund_order_blocked(s.order_id)))
 OR EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$2 AND status IN ('pending','approved'))
 OR EXISTS(SELECT 1 FROM invoice_application_items x JOIN invoice_applications a ON a.id=x.application_id WHERE x.source_invoice_id=$1 AND a.status IN ('pending','issued'))`, invoice, service).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return conflict("manual_collection_unavailable", "账单或服务存在其他收款、退款、取消、未完成业务或有效发票申请")
	}
	var timeValid bool
	if err := tx.QueryRow(ctx, "SELECT $1::timestamptz>=$2::timestamptz AND $1::timestamptz<=clock_timestamp()", in.ReceivedAt, state.CreatedAt).Scan(&timeValid); err != nil {
		return err
	}
	if !timeValid {
		return conflict("manual_collection_time_invalid", "到账时间不能早于账单创建时间或晚于当前时间")
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('refund-original:'||$1,0))", in.TransactionNo); err != nil {
		return err
	}
	if err := checkAdminRenewalCredential(ctx, tx, actor); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM manual_collections WHERE transaction_no=$1)
 OR EXISTS(SELECT 1 FROM payment_transactions WHERE provider<>'mock' AND external_transaction_id=$1)
 OR EXISTS(SELECT 1 FROM credit_repayments WHERE receipt=$1)
 OR EXISTS(SELECT 1 FROM payment_close_anomalies WHERE provider<>'mock' AND external_transaction_id=$1)
 OR EXISTS(SELECT 1 FROM payment_close_anomaly_refunds WHERE external_transaction_id=$1 AND status<>'rejected')`, in.TransactionNo).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return conflict("manual_collection_source_occupied", "该线下流水已用于收款、还款或异常退回，不能再次入账")
	}
	return tx.QueryRow(ctx, `SELECT
 (SELECT coalesce(jsonb_agg(external_refund_order_snapshot(ii) ORDER BY ii.id),'[]'::jsonb) FROM invoice_items ii WHERE ii.invoice_id=$1),
 (SELECT coalesce(jsonb_agg(external_refund_order_snapshot(p) ORDER BY p.id),'[]'::jsonb) FROM payment_transactions p WHERE p.invoice_id=$1)`, invoice).
		Scan(&state.Items, &state.Sources)
}

func buildAdminManualCollectionPreview(ctx context.Context, tx pgx.Tx, service, invoice int64, state manualCollectionState, in AdminServiceManualCollectionInput) (AdminServiceManualCollectionPreview, error) {
	out := AdminServiceManualCollectionPreview{ServiceID: service, CustomerID: state.Customer, InvoiceID: invoice, InvoiceNo: state.InvoiceNo,
		InvoiceVersion: state.InvoiceVersion, ServiceVersion: state.ServiceVersion, AmountCents: state.Amount, ReceivedAt: in.ReceivedAt,
		Method: in.Method, ReceivingAccount: in.ReceivingAccount, TransactionNo: in.TransactionNo}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return out, err
	}
	out.ExpiresAt = time.Unix((now.Unix()/300+1)*300, 0).UTC()
	in.QuoteHash = ""
	out.QuoteHash = accountBusinessHash(struct {
		Scope   string
		Preview AdminServiceManualCollectionPreview
		Input   AdminServiceManualCollectionInput
		State   manualCollectionState
	}{adminManualCollectionScope, out, in, state})
	return out, nil
}

func (s *Store) QuoteAdminServiceManualCollection(ctx context.Context, actor, service, invoice int64, in AdminServiceManualCollectionInput) (out AdminServiceManualCollectionPreview, err error) {
	if err = validateAdminManualCollection(&in, false); err != nil {
		return out, err
	}
	if actor < 1 || service < 1 || invoice < 1 {
		return out, &AccountBusinessError{400, "invalid_manual_collection", "管理员、服务或账单编号无效"}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminManualCollectionAuthority(ctx, tx, actor, service, invoice)
	if err != nil {
		return out, err
	}
	if err = s.lockAdminManualCollectionFresh(ctx, tx, actor, service, invoice, &state, in); err != nil {
		return out, err
	}
	out, err = buildAdminManualCollectionPreview(ctx, tx, service, invoice, state, in)
	if err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

func manualCollectionDBError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return conflict("manual_collection_source_occupied", "该流水、账单或收款已登记，请核对原记录")
	}
	return err
}

func (s *Store) CollectAdminServiceRenewal(ctx context.Context, actor, service, invoice int64, key string, in AdminServiceManualCollectionInput) (out AdminServiceManualCollectionResult, err error) {
	defer func() { err = manualCollectionDBError(err) }()
	key = strings.TrimSpace(key)
	if err = validateAdminManualCollection(&in, true); err != nil {
		return out, err
	}
	if actor < 1 || service < 1 || invoice < 1 || len(key) < 8 || len(key) > 128 {
		return out, &AccountBusinessError{400, "invalid_manual_collection", "管理员、服务、账单或幂等键无效"}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminManualCollectionAuthority(ctx, tx, actor, service, invoice)
	if err != nil {
		return out, err
	}
	hash := accountBusinessHash(struct {
		Customer, Service, Invoice int64
		Input                      AdminServiceManualCollectionInput
	}{state.Customer, service, invoice, in})
	replayed, err := ReplayOperation(ctx, tx, actor, adminManualCollectionScope, key, hash, &out)
	if err == nil || isRenewalBatchConflict(err) {
		if authErr := checkAdminRenewalCredential(ctx, tx, actor); authErr != nil {
			return out, authErr
		}
	}
	if err != nil {
		return out, err
	}
	if replayed {
		var owner, invoiceOwner int64
		if err = tx.QueryRow(ctx, "SELECT user_id FROM invoices WHERE id=$1 FOR SHARE", invoice).Scan(&invoiceOwner); err != nil {
			return out, err
		}
		if err = tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1 FOR SHARE NOWAIT", service).Scan(&owner); err != nil {
			return out, cancellationServiceLockError(err)
		}
		if err = checkAdminRenewalCredential(ctx, tx, actor); err != nil {
			return out, err
		}
		if owner != state.ServiceCustomer || invoiceOwner != state.Customer || owner != invoiceOwner {
			return out, conflict("service_owner_changed", "账单或服务客户已变化，请重新核对历史人工收款")
		}
		out.Replayed = true
		return out, commitAdminRenewal(ctx, tx, actor)
	}
	if err = s.lockAdminManualCollectionFresh(ctx, tx, actor, service, invoice, &state, in); err != nil {
		return out, err
	}
	preview, err := buildAdminManualCollectionPreview(ctx, tx, service, invoice, state, in)
	if err != nil {
		return out, err
	}
	if preview.QuoteHash != in.QuoteHash {
		return out, conflict("stale_manual_collection_quote", "人工收款预览已过期或账单、服务及来源发生变化，请重新预览")
	}
	reference := manualCollectionReference(in.Method, in.ReceivingAccount, in.TransactionNo)
	if err = tx.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,external_transaction_id,invoice_id,user_id,amount_cents,status,completed_at,credential_snapshot,channel_instance_id,checkout_url,next_reconcile_at,raw_result)
 VALUES('manual_offline',$1,$1,$2,$3,$4,'succeeded',$5,''::bytea,NULL,'',NULL,jsonb_build_object('source','administrator_offline_collection')) RETURNING id`, reference, invoice, state.Customer, state.Amount, in.ReceivedAt).Scan(&out.PaymentID); err != nil {
		return out, err
	}
	if err = settleExternalInvoice(ctx, tx, invoice); err != nil {
		return out, err
	}
	// Cash and commission windows use actual receipt time. Entitlement still
	// starts at the existing paid-transition policy's verified settlement time.
	if _, err = tx.Exec(ctx, "UPDATE invoices SET paid_at=$2 WHERE id=$1", invoice, in.ReceivedAt); err != nil {
		return out, err
	}
	var settledInvoiceVersion, settledServiceVersion int64
	var settledInvoice, settledService json.RawMessage
	if err = tx.QueryRow(ctx, "SELECT i.version,s.version,external_refund_order_snapshot(i),external_refund_order_snapshot(s) FROM invoices i JOIN services s ON s.id=$2 WHERE i.id=$1", invoice, service).
		Scan(&settledInvoiceVersion, &settledServiceVersion, &settledInvoice, &settledService); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO manual_collections(payment_id,invoice_id,service_id,user_id,actor_id,amount_cents,received_at,method,receiving_account,transaction_no,evidence_ref,evidence_sha256,reason,idempotency_key,request_hash,invoice_version,service_version,invoice_snapshot,service_snapshot)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING id`, out.PaymentID, invoice, service, state.Customer, actor, state.Amount, in.ReceivedAt, in.Method, in.ReceivingAccount, in.TransactionNo, in.EvidenceRef, in.EvidenceSHA256, in.Reason, key, hash, settledInvoiceVersion, settledServiceVersion, settledInvoice, settledService).Scan(&out.CollectionID); err != nil {
		return out, err
	}
	if err = s.QueuePaymentSuccessNotifications(ctx, tx, invoice); err != nil {
		return out, err
	}
	if err = s.QueueAffiliateCommissions(ctx, tx, invoice, out.PaymentID); err != nil {
		return out, err
	}
	out.ServiceID, out.CustomerID, out.InvoiceID, out.InvoiceNo, out.AmountCents, out.Status = service, state.Customer, invoice, state.InvoiceNo, state.Amount, "paid"
	out.ReceivedAt = in.ReceivedAt
	out.RenewalStatus = "completed"
	if state.Mode == "supplier" {
		out.RenewalStatus = "pending"
	}
	out.InvoiceVersion, out.ServiceVersion = settledInvoiceVersion, settledServiceVersion
	if err = Audit(ctx, tx, actor, "invoice.manual_collection.recorded", "invoice", invoice, map[string]any{"collection_id": out.CollectionID, "payment_id": out.PaymentID, "service_id": service, "customer_id": state.Customer, "amount_cents": state.Amount, "received_at": in.ReceivedAt, "method": in.Method, "reason": in.Reason, "evidence_ref": in.EvidenceRef, "evidence_sha256": in.EvidenceSHA256, "request_hash": hash}); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "service.renewal.manual_collected", "service", service, map[string]any{"collection_id": out.CollectionID, "invoice_id": invoice, "payment_id": out.PaymentID, "customer_id": state.Customer, "amount_cents": state.Amount, "received_at": in.ReceivedAt, "method": in.Method, "reason": in.Reason, "request_hash": hash}); err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, adminManualCollectionScope, key, hash, out); err != nil {
		return out, err
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	var current bool
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()<$1", preview.ExpiresAt).Scan(&current); err != nil {
		return out, err
	}
	if !current {
		return out, conflict("stale_manual_collection_quote", "人工收款预览已过期，请重新预览")
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}
