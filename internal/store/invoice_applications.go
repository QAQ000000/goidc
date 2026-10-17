package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type InvoiceProfileInput struct {
	ProfileType     string `json:"profile_type"`
	Title           string `json:"title"`
	TaxNumber       string `json:"tax_number"`
	BankName        string `json:"bank_name"`
	BankAccount     string `json:"bank_account"`
	CompanyAddr     string `json:"company_address"`
	Phone           string `json:"phone"`
	ExpectedVersion int64  `json:"expected_version"`
}

type InvoiceAddressInput struct {
	Recipient       string `json:"recipient"`
	Phone           string `json:"phone"`
	Province        string `json:"province"`
	City            string `json:"city"`
	Region          string `json:"region"`
	Address         string `json:"address"`
	Postcode        string `json:"postcode"`
	IsDefault       bool   `json:"is_default"`
	ExpectedVersion int64  `json:"expected_version"`
}

type InvoiceApplicationInput struct {
	ProfileID  int64   `json:"profile_id"`
	AddressID  int64   `json:"address_id"`
	InvoiceIDs []int64 `json:"invoice_ids"`
}

type InvoiceApplicationReview struct {
	Status          string `json:"status"`
	ReviewNote      string `json:"review_note"`
	DocumentNumber  string `json:"document_number"`
	Carrier         string `json:"carrier"`
	TrackingNumber  string `json:"tracking_number"`
	ExpectedVersion int64  `json:"expected_version"`
}

type InvoiceApplicationResult struct {
	ApplicationID int64  `json:"application_id"`
	Status        string `json:"status"`
	AmountCents   int64  `json:"amount_cents"`
	Version       int64  `json:"version"`
	AdjustmentID  int64  `json:"adjustment_id,omitempty"`
	Action        string `json:"action,omitempty"`
	Provider      string `json:"provider,omitempty"`
	TaxService    string `json:"tax_service,omitempty"`
	Replayed      bool   `json:"replayed"`
}

type InvoiceApplicationAdjustment struct {
	Action          string `json:"action"`
	DocumentNumber  string `json:"document_number"`
	Note            string `json:"note"`
	ExpectedVersion int64  `json:"expected_version"`
}

type InvoiceApplicationDocument struct {
	Filename       string
	MIME           string
	Content        []byte
	ExpectedSHA256 string
}

func (s *Store) invoiceCustomerTx(ctx context.Context, userID int64) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('invoice-customer:'||$1::bigint::text,0))", userID); err == nil {
		var id int64
		err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND role='customer' AND status='active'", userID).Scan(&id)
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func normalizeInvoiceProfile(in *InvoiceProfileInput) error {
	in.ProfileType = strings.ToLower(strings.TrimSpace(in.ProfileType))
	in.Title, in.TaxNumber = strings.TrimSpace(in.Title), strings.TrimSpace(in.TaxNumber)
	in.BankName, in.BankAccount = strings.TrimSpace(in.BankName), strings.TrimSpace(in.BankAccount)
	in.CompanyAddr, in.Phone = strings.TrimSpace(in.CompanyAddr), strings.TrimSpace(in.Phone)
	if (in.ProfileType != "individual" && in.ProfileType != "company") || in.Title == "" || !contentTextValid(in.Title, 160, false) || !contentTextValid(in.TaxNumber, 80, false) || !contentTextValid(in.BankName, 160, false) || !contentTextValid(in.BankAccount, 120, false) || !contentTextValid(in.CompanyAddr, 300, true) || !contentTextValid(in.Phone, 80, false) || (in.ProfileType == "company" && in.TaxNumber == "") {
		return conflict("invalid_invoice_profile", "发票抬头字段无效")
	}
	return nil
}

func normalizeInvoiceAddress(in *InvoiceAddressInput) error {
	in.Recipient, in.Phone = strings.TrimSpace(in.Recipient), strings.TrimSpace(in.Phone)
	in.Province, in.City, in.Region = strings.TrimSpace(in.Province), strings.TrimSpace(in.City), strings.TrimSpace(in.Region)
	in.Address, in.Postcode = strings.TrimSpace(in.Address), strings.TrimSpace(in.Postcode)
	if in.Recipient == "" || !contentTextValid(in.Recipient, 80, false) || len(in.Phone) < 3 || !contentTextValid(in.Phone, 80, false) || in.Address == "" || !contentTextValid(in.Address, 300, true) || !contentTextValid(in.Province, 80, false) || !contentTextValid(in.City, 80, false) || !contentTextValid(in.Region, 80, false) || !contentTextValid(in.Postcode, 30, false) {
		return conflict("invalid_invoice_address", "收货地址字段无效")
	}
	return nil
}

func (s *Store) ListInvoiceProfiles(ctx context.Context, userID int64) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, `SELECT id,profile_type,title,tax_number,bank_name,bank_account,company_address,phone,version,created_at,updated_at FROM invoice_profiles WHERE user_id=$1 ORDER BY id DESC`, userID))
}

func (s *Store) SaveInvoiceProfile(ctx context.Context, userID, id int64, in InvoiceProfileInput) (int64, error) {
	if err := normalizeInvoiceProfile(&in); err != nil {
		return 0, err
	}
	if id > 0 && in.ExpectedVersion < 1 || id == 0 && in.ExpectedVersion != 0 {
		return 0, conflict("invalid_invoice_profile_version", "抬头版本无效")
	}
	tx, err := s.invoiceCustomerTx(ctx, userID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if id == 0 {
		if err = tx.QueryRow(ctx, `INSERT INTO invoice_profiles(user_id,profile_type,title,tax_number,bank_name,bank_account,company_address,phone) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, userID, in.ProfileType, in.Title, in.TaxNumber, in.BankName, in.BankAccount, in.CompanyAddr, in.Phone).Scan(&id); err != nil {
			return 0, err
		}
	} else {
		var owner, version int64
		if err = tx.QueryRow(ctx, "SELECT user_id,version FROM invoice_profiles WHERE id=$1 AND user_id=$2 FOR UPDATE", id, userID).Scan(&owner, &version); err != nil {
			return 0, err
		}
		if owner != userID {
			return 0, conflict("invoice_profile_not_found", "发票抬头不存在")
		}
		if version != in.ExpectedVersion {
			return 0, conflict("stale_invoice_profile", "发票抬头已更新，请刷新后操作")
		}
		if _, err = tx.Exec(ctx, `UPDATE invoice_profiles SET profile_type=$2,title=$3,tax_number=$4,bank_name=$5,bank_account=$6,company_address=$7,phone=$8,version=version+1,updated_at=now() WHERE id=$1`, id, in.ProfileType, in.Title, in.TaxNumber, in.BankName, in.BankAccount, in.CompanyAddr, in.Phone); err != nil {
			return 0, err
		}
	}
	if err = Audit(ctx, tx, userID, "invoice_profile.saved", "invoice_profile", id, map[string]any{"profile_type": in.ProfileType}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DeleteInvoiceProfile(ctx context.Context, userID, id, version int64) error {
	if id < 1 || version < 1 {
		return conflict("invalid_invoice_profile", "发票抬头无效")
	}
	tx, err := s.invoiceCustomerTx(ctx, userID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner, current int64
	if err = tx.QueryRow(ctx, "SELECT user_id,version FROM invoice_profiles WHERE id=$1 AND user_id=$2 FOR UPDATE", id, userID).Scan(&owner, &current); err != nil {
		return err
	}
	if owner != userID {
		return conflict("invoice_profile_not_found", "发票抬头不存在")
	}
	if current != version {
		return conflict("stale_invoice_profile", "发票抬头已更新，请刷新后操作")
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invoice_applications WHERE profile_id=$1)", id).Scan(&used); err != nil {
		return err
	}
	if used {
		return conflict("invoice_profile_in_use", "已使用的发票抬头不能删除")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM invoice_profiles WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, userID, "invoice_profile.deleted", "invoice_profile", id, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListInvoiceAddresses(ctx context.Context, userID int64) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, `SELECT id,recipient,phone,province,city,region,address,postcode,is_default,version,created_at,updated_at FROM invoice_addresses WHERE user_id=$1 ORDER BY is_default DESC,id DESC`, userID))
}

func (s *Store) SaveInvoiceAddress(ctx context.Context, userID, id int64, in InvoiceAddressInput) (int64, error) {
	if err := normalizeInvoiceAddress(&in); err != nil {
		return 0, err
	}
	if id > 0 && in.ExpectedVersion < 1 || id == 0 && in.ExpectedVersion != 0 {
		return 0, conflict("invalid_invoice_address_version", "地址版本无效")
	}
	tx, err := s.invoiceCustomerTx(ctx, userID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if in.IsDefault {
		if _, err = tx.Exec(ctx, "UPDATE invoice_addresses SET is_default=false,version=version+1,updated_at=now() WHERE user_id=$1 AND id<>$2 AND is_default", userID, id); err != nil {
			return 0, err
		}
	}
	if id == 0 {
		if err = tx.QueryRow(ctx, `INSERT INTO invoice_addresses(user_id,recipient,phone,province,city,region,address,postcode,is_default) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, userID, in.Recipient, in.Phone, in.Province, in.City, in.Region, in.Address, in.Postcode, in.IsDefault).Scan(&id); err != nil {
			return 0, err
		}
	} else {
		var owner, version int64
		if err = tx.QueryRow(ctx, "SELECT user_id,version FROM invoice_addresses WHERE id=$1 AND user_id=$2 FOR UPDATE", id, userID).Scan(&owner, &version); err != nil {
			return 0, err
		}
		if owner != userID {
			return 0, conflict("invoice_address_not_found", "收货地址不存在")
		}
		if version != in.ExpectedVersion {
			return 0, conflict("stale_invoice_address", "收货地址已更新，请刷新后操作")
		}
		if _, err = tx.Exec(ctx, `UPDATE invoice_addresses SET recipient=$2,phone=$3,province=$4,city=$5,region=$6,address=$7,postcode=$8,is_default=$9,version=version+1,updated_at=now() WHERE id=$1`, id, in.Recipient, in.Phone, in.Province, in.City, in.Region, in.Address, in.Postcode, in.IsDefault); err != nil {
			return 0, err
		}
	}
	if err = Audit(ctx, tx, userID, "invoice_address.saved", "invoice_address", id, map[string]any{}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DeleteInvoiceAddress(ctx context.Context, userID, id, version int64) error {
	if id < 1 || version < 1 {
		return conflict("invalid_invoice_address", "收货地址无效")
	}
	tx, err := s.invoiceCustomerTx(ctx, userID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner, current int64
	if err = tx.QueryRow(ctx, "SELECT user_id,version FROM invoice_addresses WHERE id=$1 AND user_id=$2 FOR UPDATE", id, userID).Scan(&owner, &current); err != nil {
		return err
	}
	if owner != userID {
		return conflict("invoice_address_not_found", "收货地址不存在")
	}
	if current != version {
		return conflict("stale_invoice_address", "收货地址已更新，请刷新后操作")
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invoice_applications WHERE address_id=$1)", id).Scan(&used); err != nil {
		return err
	}
	if used {
		return conflict("invoice_address_in_use", "已使用的收货地址不能删除")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM invoice_addresses WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, userID, "invoice_address.deleted", "invoice_address", id, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateInvoiceApplication(ctx context.Context, userID int64, in InvoiceApplicationInput, key string) (InvoiceApplicationResult, error) {
	var out InvoiceApplicationResult
	if len(in.InvoiceIDs) < 1 || len(in.InvoiceIDs) > 20 || in.ProfileID < 1 || in.AddressID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_invoice_application", "抬头、地址或账单选择无效")
	}
	invoiceIDs := slices.Clone(in.InvoiceIDs)
	slices.Sort(invoiceIDs)
	if invoiceIDs[0] < 1 {
		return out, conflict("invalid_invoice_application", "账单编号无效")
	}
	for i := 1; i < len(invoiceIDs); i++ {
		if invoiceIDs[i] == invoiceIDs[i-1] {
			return out, conflict("invalid_invoice_application", "账单不能重复选择")
		}
	}
	tx, err := s.invoiceCustomerTx(ctx, userID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	in.InvoiceIDs = invoiceIDs
	canonical, _ := json.Marshal(in)
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	var savedHash string
	var saved []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM invoice_application_operations WHERE actor_id=$1 AND idempotency_key=$2", userID, key).Scan(&savedHash, &saved)
	if err == nil {
		if savedHash != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同发票申请")
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
	var owner int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM invoice_profiles WHERE id=$1 AND user_id=$2", in.ProfileID, userID).Scan(&owner); err != nil {
		return out, err
	}
	if owner != userID {
		return out, conflict("invoice_profile_not_found", "发票抬头不存在")
	}
	if err = tx.QueryRow(ctx, "SELECT user_id FROM invoice_addresses WHERE id=$1 AND user_id=$2", in.AddressID, userID).Scan(&owner); err != nil {
		return out, err
	}
	if owner != userID {
		return out, conflict("invoice_address_not_found", "收货地址不存在")
	}
	var total int64
	type item struct {
		id              int64
		no, description string
		amount          int64
	}
	items := make([]item, 0, len(invoiceIDs))
	for _, id := range invoiceIDs {
		var uid, amount int64
		var no string
		if err = tx.QueryRow(ctx, "SELECT user_id,invoice_no,amount_cents FROM invoices WHERE id=$1 AND user_id=$2 AND status='paid' AND invoice_kind<>'topup' AND amount_cents>0 AND NOT import_review_required AND legacy_source='' FOR UPDATE", id, userID).Scan(&uid, &no, &amount); err != nil {
			return out, err
		}
		if uid != userID {
			return out, conflict("invoice_not_owned", "只能申请本人账单")
		}
		var already bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invoice_application_items x JOIN invoice_applications a ON a.id=x.application_id WHERE x.source_invoice_id=$1 AND a.status IN ('pending','issued'))", id).Scan(&already); err != nil {
			return out, err
		}
		if already {
			return out, conflict("invoice_already_applied", "所选账单已有有效发票申请")
		}
		var description string
		if err = tx.QueryRow(ctx, "SELECT coalesce(string_agg(description,' · ' ORDER BY id),'账单 '||$1) FROM invoice_items WHERE invoice_id=$2", no, id).Scan(&description); err != nil {
			return out, err
		}
		items = append(items, item{id, no, description, amount})
		if amount > manualInvoiceLimit-total {
			return out, conflict("invalid_invoice_application", "开票总额超出范围")
		}
		total += amount
	}
	if total <= 0 {
		return out, conflict("invalid_invoice_application", "开票金额必须大于零")
	}
	var profileSnapshot, addressSnapshot []byte
	if err = tx.QueryRow(ctx, `SELECT jsonb_build_object('profile_type',profile_type,'title',title,'tax_number',tax_number,'bank_name',bank_name,'bank_account',bank_account,'company_address',company_address,'phone',phone) FROM invoice_profiles WHERE id=$1`, in.ProfileID).Scan(&profileSnapshot); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT jsonb_build_object('recipient',recipient,'phone',phone,'province',province,'city',city,'region',region,'address',address,'postcode',postcode) FROM invoice_addresses WHERE id=$1`, in.AddressID).Scan(&addressSnapshot); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO invoice_applications(user_id,profile_id,address_id,profile_snapshot,address_snapshot,amount_cents) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,status,version`, userID, in.ProfileID, in.AddressID, profileSnapshot, addressSnapshot, total).Scan(&out.ApplicationID, &out.Status, &out.Version); err != nil {
		return out, err
	}
	out.AmountCents = total
	for _, it := range items {
		if _, err = tx.Exec(ctx, "INSERT INTO invoice_application_items(application_id,source_invoice_id,invoice_no,description,amount_cents) VALUES($1,$2,$3,$4,$5)", out.ApplicationID, it.id, it.no, it.description, it.amount); err != nil {
			return out, err
		}
	}
	encoded, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, "INSERT INTO invoice_application_operations(actor_id,idempotency_key,application_id,action,request_hash,result) VALUES($1,$2,$3,'create',$4,$5)", userID, key, out.ApplicationID, hash, encoded); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, userID, "invoice_application.created", "invoice_application", out.ApplicationID, map[string]any{"amount_cents": total, "invoice_ids": invoiceIDs}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ReviewInvoiceApplication(ctx context.Context, actorID, id int64, in InvoiceApplicationReview, key string) (InvoiceApplicationResult, error) {
	var out InvoiceApplicationResult
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	in.ReviewNote = strings.TrimSpace(in.ReviewNote)
	in.DocumentNumber, in.Carrier, in.TrackingNumber = strings.TrimSpace(in.DocumentNumber), strings.TrimSpace(in.Carrier), strings.TrimSpace(in.TrackingNumber)
	if id < 1 || in.ExpectedVersion < 1 || (in.Status != "issued" && in.Status != "rejected") || !contentTextValid(in.ReviewNote, 500, true) || !contentTextValid(in.DocumentNumber, 120, false) || !contentTextValid(in.Carrier, 120, false) || !contentTextValid(in.TrackingNumber, 160, false) || (in.Status == "issued" && in.DocumentNumber == "") || (in.Status == "rejected" && in.ReviewNote == "") || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_invoice_review", "审核状态、版本或备注无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return out, err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'invoice_applications.view'=ANY(r.permissions) AND 'invoice_applications.manage'=ANY(r.permissions)),false) FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed); err != nil {
		return out, err
	}
	if !allowed {
		return out, conflict("permission_denied", "没有审核发票的权限")
	}
	if err = lockOperationKey(ctx, tx, "invoice-application", fmt.Sprintf("%d:%s", actorID, key)); err != nil {
		return out, err
	}
	var userID int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM invoice_applications WHERE id=$1", id).Scan(&userID); err != nil {
		return out, err
	}
	if err = LockAdminCustomer(ctx, tx, actorID, userID); err != nil {
		return out, err
	}
	canonical, _ := json.Marshal(struct {
		ID    int64
		Input InvoiceApplicationReview
	}{id, in})
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	var savedHash string
	var saved []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM invoice_application_operations WHERE actor_id=$1 AND idempotency_key=$2", actorID, key).Scan(&savedHash, &saved)
	if err == nil {
		if savedHash != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同审核操作")
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
	if err = tx.QueryRow(ctx, "SELECT user_id,status,amount_cents,version FROM invoice_applications WHERE id=$1 FOR UPDATE", id).Scan(&userID, &out.Status, &out.AmountCents, &out.Version); err != nil {
		return out, err
	}
	if err = LockAdminCustomer(ctx, tx, actorID, userID); err != nil {
		return out, err
	}
	// Recheck the source invoices after the customer scope lock and hold them
	// through the status transition so refund and application creation serialize.
	rows, err := tx.Query(ctx, "SELECT i.id FROM invoices i JOIN invoice_application_items x ON x.source_invoice_id=i.id WHERE x.application_id=$1 ORDER BY i.id FOR UPDATE OF i", id)
	if err != nil {
		return out, err
	}
	if _, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return out, err
	}
	if in.Status == "issued" {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT bool_and(i.user_id=$2 AND i.status='paid' AND i.amount_cents=x.amount_cents AND NOT i.import_review_required) FROM invoice_application_items x JOIN invoices i ON i.id=x.source_invoice_id WHERE x.application_id=$1`, id, userID).Scan(&valid); err != nil {
			return out, err
		}
		if !valid {
			return out, conflict("invoice_application_invalid", "来源账单已变化，不能登记开具")
		}
	}
	if out.Status != "pending" || out.Version != in.ExpectedVersion {
		return out, conflict("stale_invoice_application", "申请已更新，请刷新后操作")
	}
	if _, err = tx.Exec(ctx, "UPDATE invoice_applications SET status=$2,review_note=$3,document_number=$4,carrier=$5,tracking_number=$6,reviewer_id=$7,reviewed_at=now(),version=version+1,updated_at=now() WHERE id=$1", id, in.Status, in.ReviewNote, in.DocumentNumber, in.Carrier, in.TrackingNumber, actorID); err != nil {
		return out, err
	}
	out.ApplicationID = id
	out.Status = in.Status
	out.Version++
	encoded, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, "INSERT INTO invoice_application_operations(actor_id,idempotency_key,application_id,action,request_hash,result) VALUES($1,$2,$3,'review',$4,$5)", actorID, key, id, hash, encoded); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "invoice_application.reviewed", "invoice_application", id, map[string]any{"status": in.Status, "note": in.ReviewNote}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) invoiceManageTx(ctx context.Context, actorID, applicationID int64) (pgx.Tx, int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return nil, 0, err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'invoice_applications.view'=ANY(r.permissions) AND 'invoice_applications.manage'=ANY(r.permissions)),false)
 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed); err != nil || !allowed {
		if err == nil {
			err = conflict("permission_denied", "没有管理发票申请的权限")
		}
		return nil, 0, err
	}
	var customerID int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM invoice_applications WHERE id=$1", applicationID).Scan(&customerID); err != nil {
		return nil, 0, err
	}
	if err = LockAdminCustomer(ctx, tx, actorID, customerID); err != nil {
		return nil, 0, err
	}
	return tx, customerID, nil
}

// Recheck after scope, application or idempotency locks may have waited.
func invoiceDocumentAuthorize(ctx context.Context, tx pgx.Tx, actorID int64) error {
	if err := lockStorageCredential(ctx, tx, actorID); err != nil {
		return err
	}
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", actorID).Scan(&role, &roleID); err != nil {
		return conflict("permission_denied", "没有管理发票申请的权限")
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return conflict("permission_denied", "没有管理发票申请的权限")
	}
	var allowed bool
	err := tx.QueryRow(ctx, "SELECT active AND 'invoice_applications.view'=ANY(permissions) AND 'invoice_applications.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return conflict("permission_denied", "没有管理发票申请的权限")
	}
	return nil
}

func validateInvoiceDocument(file InvoiceApplicationDocument) error {
	name := strings.TrimSpace(file.Filename)
	if name == "" || !contentTextValid(name, 255, false) || strings.ContainsAny(name, "/\\\r\n\x00") || len(file.Content) == 0 || len(file.Content) > 20<<20 {
		return conflict("invalid_invoice_document", "电子发票文件无效")
	}
	if file.MIME != "application/pdf" && file.MIME != "application/xml" {
		return conflict("invalid_invoice_document", "电子发票文件类型不受支持")
	}
	return nil
}

func (s *Store) SaveInvoiceApplicationDocument(ctx context.Context, actorID, applicationID, adjustmentID int64, file InvoiceApplicationDocument) (int64, error) {
	if applicationID < 1 || adjustmentID < 0 {
		return 0, conflict("invalid_invoice_document", "发票申请或调整编号无效")
	}
	if err := validateInvoiceDocument(file); err != nil {
		return 0, err
	}
	tx, customerID, err := s.invoiceManageTx(ctx, actorID, applicationID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if adjustmentID > 0 {
		var owner int64
		if err = tx.QueryRow(ctx, "SELECT application_id FROM invoice_application_adjustments WHERE id=$1", adjustmentID).Scan(&owner); err != nil {
			return 0, err
		}
		if owner != applicationID {
			return 0, conflict("invalid_invoice_document", "调整记录不属于该申请")
		}
	}
	var applicationStatus string
	if err = tx.QueryRow(ctx, "SELECT status FROM invoice_applications WHERE id=$1 FOR UPDATE", applicationID).Scan(&applicationStatus); err != nil {
		return 0, err
	}
	if applicationStatus != "issued" {
		return 0, conflict("invoice_document_not_ready", "只有已开具申请可以登记电子发票文件")
	}
	if err = invoiceDocumentAuthorize(ctx, tx, actorID); err != nil {
		return 0, err
	}
	digest := sha256.Sum256(file.Content)
	hash := hex.EncodeToString(digest[:])
	var id int64
	if adjustmentID == 0 {
		err = tx.QueryRow(ctx, "SELECT id FROM invoice_application_documents WHERE application_id=$1 AND adjustment_id IS NULL FOR UPDATE", applicationID).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, "SELECT id FROM invoice_application_documents WHERE application_id=$1 AND adjustment_id=$2 FOR UPDATE", applicationID, adjustmentID).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO invoice_application_documents(application_id,adjustment_id,filename,mime_type,size_bytes,sha256,content,uploader_id)
 VALUES($1,NULLIF($2,0),$3,$4,$5,$6,$7,$8) RETURNING id`, applicationID, adjustmentID, strings.TrimSpace(file.Filename), file.MIME, len(file.Content), hash, file.Content, actorID).Scan(&id)
	} else if err == nil {
		var currentHash string
		if err = tx.QueryRow(ctx, "SELECT sha256 FROM invoice_application_documents WHERE id=$1", id).Scan(&currentHash); err != nil {
			return 0, err
		}
		if file.ExpectedSHA256 != currentHash {
			return 0, conflict("stale_invoice_document", "文件已存在或已被替换，请刷新后确认覆盖")
		}
		_, err = tx.Exec(ctx, `UPDATE invoice_application_documents SET filename=$2,mime_type=$3,size_bytes=$4,sha256=$5,content=$6,uploader_id=$7,created_at=now() WHERE id=$1`, id, strings.TrimSpace(file.Filename), file.MIME, len(file.Content), hash, file.Content, actorID)
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actorID, "invoice_application.document_uploaded", "invoice_application_document", id, map[string]any{"application_id": applicationID, "adjustment_id": adjustmentID, "customer_id": customerID, "filename": file.Filename, "sha256": hash}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) AdjustInvoiceApplication(ctx context.Context, actorID, applicationID int64, in InvoiceApplicationAdjustment, key string) (InvoiceApplicationResult, error) {
	var out InvoiceApplicationResult
	in.Action, in.DocumentNumber, in.Note, key = strings.ToLower(strings.TrimSpace(in.Action)), strings.TrimSpace(in.DocumentNumber), strings.TrimSpace(in.Note), strings.TrimSpace(key)
	if applicationID < 1 || (in.Action != "void" && in.Action != "red") || in.ExpectedVersion < 1 || in.Note == "" || !contentTextValid(in.Note, 500, true) || !contentTextValid(in.DocumentNumber, 120, false) || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_invoice_adjustment", "作废或红冲参数无效")
	}
	tx, customerID, err := s.invoiceManageTx(ctx, actorID, applicationID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "invoice-application", fmt.Sprintf("%d:%s", actorID, key)); err != nil {
		return out, err
	}
	canonical, _ := json.Marshal(struct {
		ID    int64
		Input InvoiceApplicationAdjustment
	}{applicationID, in})
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	var savedHash string
	var saved []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM invoice_application_operations WHERE actor_id=$1 AND idempotency_key=$2", actorID, key).Scan(&savedHash, &saved)
	if err == nil {
		if err = invoiceDocumentAuthorize(ctx, tx, actorID); err != nil {
			return out, err
		}
		if savedHash != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同发票调整")
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
	var status string
	if err = tx.QueryRow(ctx, "SELECT status,version FROM invoice_applications WHERE id=$1 FOR UPDATE", applicationID).Scan(&status, &out.Version); err != nil {
		return out, err
	}
	if status != "issued" || out.Version != in.ExpectedVersion {
		return out, conflict("stale_invoice_application", "只有已开具申请可以作废或红冲")
	}
	if err = invoiceDocumentAuthorize(ctx, tx, actorID); err != nil {
		return out, err
	}
	if in.Action == "red" && in.DocumentNumber == "" {
		return out, conflict("invalid_invoice_adjustment", "红冲必须填写红字发票编号")
	}
	if err = tx.QueryRow(ctx, `INSERT INTO invoice_application_adjustments(application_id,action,document_number,note,actor_id) VALUES($1,$2,$3,$4,$5) RETURNING id`, applicationID, in.Action, in.DocumentNumber, in.Note, actorID).Scan(&out.AdjustmentID); err != nil {
		if strings.Contains(err.Error(), "invoice_application_adjustments_application_id_action_key") {
			return out, conflict("invoice_adjustment_exists", "该申请已登记过此类调整")
		}
		return out, err
	}
	if _, err = tx.Exec(ctx, "UPDATE invoice_applications SET version=version+1,updated_at=now() WHERE id=$1", applicationID); err != nil {
		return out, err
	}
	out.ApplicationID, out.Status, out.AmountCents, out.Action = applicationID, status, 0, in.Action
	out.Provider, out.TaxService = "manual", "not_connected"
	if err = tx.QueryRow(ctx, "SELECT amount_cents,version FROM invoice_applications WHERE id=$1", applicationID).Scan(&out.AmountCents, &out.Version); err != nil {
		return out, err
	}
	encoded, _ := json.Marshal(out)
	if _, err = tx.Exec(ctx, "INSERT INTO invoice_application_operations(actor_id,idempotency_key,application_id,action,request_hash,result) VALUES($1,$2,$3,'adjust',$4,$5)", actorID, key, applicationID, hash, encoded); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "invoice_application.adjusted", "invoice_application", applicationID, map[string]any{"action": in.Action, "adjustment_id": out.AdjustmentID, "customer_id": customerID, "document_number": in.DocumentNumber}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func collectMaps(rows pgx.Rows, err error) ([]map[string]any, error) {
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err == nil && items == nil {
		items = []map[string]any{}
	}
	return items, err
}
