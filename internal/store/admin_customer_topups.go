package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type AdminCustomerTopupInput struct {
	AmountCents     int64  `json:"amount_cents"`
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note"`
}

func lockAdminCustomerTopup(ctx context.Context, tx pgx.Tx, actor, customer int64) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	for _, permission := range []string{"invoices.view", "invoices.manage"} {
		if err := RequireAdministratorPermission(ctx, tx, actor, permission); err != nil {
			return err
		}
	}
	if err := recheckCustomerAccountCredential(ctx, tx, actor); err != nil {
		return err
	}
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	if err := LockFinancialCustomer(ctx, tx, customer); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer'", customer).Scan(&active); err != nil {
		return err
	}
	if !active {
		return conflict("customer_inactive", "客户已停用，不能代建充值账单")
	}
	return recheckCustomerAccountCredential(ctx, tx, actor)
}

func (s *Store) AdminCustomerTopupOptions(ctx context.Context, actor, customer int64) (TopupOptions, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return TopupOptions{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockAdminCustomerTopup(ctx, tx, actor, customer); err != nil {
		return TopupOptions{}, err
	}
	out, err := readTopupOptions(ctx, tx, customer)
	if err != nil {
		return out, err
	}
	return out, commitCustomerAccountMutation(ctx, tx, actor)
}

// Administrative creation only issues an unpaid bill. Admission, gateway
// settlement and exactly-once wallet credit stay in the existing topup pipeline.
func (s *Store) AdminCreateCustomerTopup(ctx context.Context, actor, customer int64, in AdminCustomerTopupInput, key string) (map[string]any, error) {
	in.Note = strings.TrimSpace(in.Note)
	if customer < 1 || in.AmountCents < 1 || in.AmountCents > 10000000000 || in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 || in.Note == "" || !utf8.ValidString(in.Note) || utf8.RuneCountInString(in.Note) > 240 || strings.ContainsAny(in.Note, "\x00") {
		return nil, &AccountBusinessError{400, "invalid_topup", "充值金额、设置版本、备注或幂等键无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockAdminCustomerTopup(ctx, tx, actor, customer); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("customer.topup.create:%d", customer)
	hash := accountBusinessHash(in)
	if err = lockCustomerAccountMutation(ctx, tx, actor, operation, key); err != nil {
		return nil, err
	}
	if replay, ok, e := loadAccountBusinessReplay(ctx, tx, actor, operation, key, hash); e != nil {
		return nil, e
	} else if ok {
		if e = commitCustomerAccountMutation(ctx, tx, actor); e != nil {
			return nil, e
		}
		return replay, nil
	}
	options, err := readTopupOptions(ctx, tx, customer)
	if err != nil {
		return nil, err
	}
	if err = validateTopupOptions(options, in.AmountCents, in.ExpectedVersion); err != nil {
		return nil, err
	}
	if err = recheckCustomerAccountCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	// Keep customer and administrative keys separate without a second receipt table.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", actor, customer, key)))
	internalKey := "admin:" + hex.EncodeToString(sum[:])
	topup, err := insertOnlineTopupInvoice(ctx, tx, customer, OnlineTopupInput{in.AmountCents, in.ExpectedVersion}, internalKey, hash, options.Version)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"invoice_id": topup.InvoiceID, "invoice_no": topup.InvoiceNo, "amount_cents": topup.AmountCents, "status": "unpaid", "topup_status": topup.Status, "version": topup.Version, "settings_version": topup.SettingsVersion, "user_id": customer, "replayed": false}
	if err = Audit(ctx, tx, actor, "balance.topup.invoice_created", "invoice", topup.InvoiceID, map[string]any{"customer_id": customer, "amount_cents": in.AmountCents, "note": in.Note}); err != nil {
		return nil, err
	}
	if err = saveCustomerAccountOperation(ctx, tx, actor, operation, key, hash, result); err != nil {
		return nil, err
	}
	if err = commitCustomerAccountMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	return result, nil
}
