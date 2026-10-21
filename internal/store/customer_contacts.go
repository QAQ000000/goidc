package store

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
)

const contactColumns = `c.id,c.customer_id,c.username,c.sex,c.avatar,c.company_name,c.email,c.wechat_id,
c.country,c.province,c.city,c.region,c.address1,c.address2,c.postcode,c.phone_number,c.permissions,
c.general_emails,c.invoice_emails,c.product_emails,c.support_emails,c.status,c.version,c.created_at,c.updated_at`

type ContactInput struct {
	Username      string   `json:"username"`
	Sex           int16    `json:"sex"`
	Avatar        string   `json:"avatar"`
	CompanyName   string   `json:"company_name"`
	Email         string   `json:"email"`
	WechatID      string   `json:"wechat_id"`
	Country       string   `json:"country"`
	Province      string   `json:"province"`
	City          string   `json:"city"`
	Region        string   `json:"region"`
	Address1      string   `json:"address1"`
	Address2      string   `json:"address2"`
	Postcode      string   `json:"postcode"`
	PhoneNumber   string   `json:"phone_number"`
	Permissions   []string `json:"permissions"`
	GeneralEmails bool     `json:"general_emails"`
	InvoiceEmails bool     `json:"invoice_emails"`
	ProductEmails bool     `json:"product_emails"`
	SupportEmails bool     `json:"support_emails"`
	Status        string   `json:"status"`
}

type ContactPatch struct {
	ExpectedVersion *int64    `json:"expected_version"`
	Username        *string   `json:"username"`
	Sex             *int16    `json:"sex"`
	Avatar          *string   `json:"avatar"`
	CompanyName     *string   `json:"company_name"`
	Email           *string   `json:"email"`
	WechatID        *string   `json:"wechat_id"`
	Country         *string   `json:"country"`
	Province        *string   `json:"province"`
	City            *string   `json:"city"`
	Region          *string   `json:"region"`
	Address1        *string   `json:"address1"`
	Address2        *string   `json:"address2"`
	Postcode        *string   `json:"postcode"`
	PhoneNumber     *string   `json:"phone_number"`
	Permissions     *[]string `json:"permissions"`
	GeneralEmails   *bool     `json:"general_emails"`
	InvoiceEmails   *bool     `json:"invoice_emails"`
	ProductEmails   *bool     `json:"product_emails"`
	SupportEmails   *bool     `json:"support_emails"`
	Status          *string   `json:"status"`
}

func normalizeContactInput(input *ContactInput) error {
	if input.Permissions == nil {
		input.Permissions = []string{}
	}
	input.Username = strings.TrimSpace(input.Username)
	input.Avatar = strings.TrimSpace(input.Avatar)
	input.CompanyName = strings.TrimSpace(input.CompanyName)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.WechatID = strings.TrimSpace(input.WechatID)
	input.Country = strings.TrimSpace(input.Country)
	input.Province = strings.TrimSpace(input.Province)
	input.City = strings.TrimSpace(input.City)
	input.Region = strings.TrimSpace(input.Region)
	input.Address1 = strings.TrimSpace(input.Address1)
	input.Address2 = strings.TrimSpace(input.Address2)
	input.Postcode = strings.TrimSpace(input.Postcode)
	input.PhoneNumber = strings.TrimSpace(input.PhoneNumber)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if input.Status == "" {
		input.Status = "inactive"
	}
	if input.Email == "" || len(input.Email) > 254 {
		return invalidInput("invalid_contact", "联系人邮箱无效")
	}
	parsed, err := mail.ParseAddress(input.Email)
	if err != nil || parsed.Address != input.Email {
		return invalidInput("invalid_contact", "联系人邮箱无效")
	}
	if input.Sex < 0 || input.Sex > 2 || input.Status != "active" && input.Status != "inactive" && input.Status != "disabled" {
		return invalidInput("invalid_contact", "联系人字段无效")
	}
	if len(input.Username) > 120 || len(input.CompanyName) > 160 || len(input.PhoneNumber) > 80 || len(input.Permissions) > 50 {
		return invalidInput("invalid_contact", "联系人字段长度无效")
	}
	for i, permission := range input.Permissions {
		input.Permissions[i] = strings.TrimSpace(permission)
		if input.Permissions[i] == "" || len(input.Permissions[i]) > 80 {
			return invalidInput("invalid_contact", "联系人权限无效")
		}
	}
	return nil
}

// Contacts are notification recipients, never alternate login principals.
// All public Store entries require the real HTTP credential, including replay.
func beginContactAuthority(ctx context.Context, tx pgx.Tx, actor, customer int64, admin, write bool) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return conflict("unauthorized", "需要有效登录凭据")
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	if admin {
		permission := "customers.view"
		if write {
			permission = "customers.manage"
		}
		if err := RequireAdministratorPermission(ctx, tx, actor, permission); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return conflict("permission_denied", "管理员权限已失效")
			}
			return err
		}
		if err := recheckContactCredential(ctx, tx, actor); err != nil {
			return err
		}
		if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
			return err
		}
	} else {
		if actor != customer {
			return pgx.ErrNoRows
		}
		// Prelock a bound administrator before either session. Personal administrator
		// password rotation locks account -> session; reversing that order deadlocks.
		if !credential.APIKey {
			binding, err := readImpersonationBinding(ctx, tx, credential.Hash)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				if binding.CustomerID != customer {
					return conflict("unauthorized", "代登录权限已失效")
				}
				if err = RequireAdministratorPermission(ctx, tx, binding.ActorID, "customers.manage"); err != nil {
					return conflict("unauthorized", "代登录权限已失效")
				}
				adminCtx := WithStorageCredential(ctx, binding.AdminHash, false)
				if err = recheckContactCredential(adminCtx, tx, binding.ActorID); err != nil {
					return err
				}
				if err = LockAdminCustomer(ctx, tx, binding.ActorID, customer); err != nil {
					return err
				}
			}
		}
	}
	var id int64
	query := "SELECT id FROM users WHERE id=$1 AND role='customer' FOR SHARE"
	if !admin {
		query = "SELECT id FROM users WHERE id=$1 AND role='customer' AND status='active' FOR SHARE"
	}
	if err := tx.QueryRow(ctx, query, customer).Scan(&id); err != nil {
		if !admin && errors.Is(err, pgx.ErrNoRows) {
			return conflict("unauthorized", "客户登录已失效")
		}
		return err
	}
	return recheckContactCredential(ctx, tx, actor)
}
func recheckContactCredential(ctx context.Context, tx pgx.Tx, actor int64) error {
	// A locking SELECT can start before expiry and finish after the deadline.
	// Re-evaluate clock_timestamp in a fresh statement after every wait.
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	return lockStorageCredential(ctx, tx, actor)
}
func commitContact(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := recheckContactCredential(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ListCustomerContacts(ctx context.Context, actor, customer int64, admin bool) ([]map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = beginContactAuthority(ctx, tx, actor, customer, admin, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT "+contactColumns+" FROM customer_contacts c WHERE c.customer_id=$1 ORDER BY c.id DESC", customer)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if err = commitContact(ctx, tx, actor); err != nil {
		return nil, err
	}
	if items == nil {
		items = []map[string]any{}
	}
	return items, nil
}
func contactReceipt(ctx context.Context, tx pgx.Tx, actor, customer int64, operation, key string, input any) (string, map[string]any, bool, error) {
	out := map[string]any{}
	if key == "" {
		return "", out, false, nil
	}
	if len(key) < 8 || len(key) > 128 {
		return "", nil, false, conflict("invalid_idempotency_key", "请求键长度无效")
	}
	hash := accountBusinessHash(input)
	replay, err := ReplayOperation(ctx, tx, actor, fmt.Sprintf("customer_contact.%s:%d", operation, customer), key, hash, &out)
	if err == nil && replay {
		out["replayed"] = true
	}
	return hash, out, replay, err
}
func saveContactReceipt(ctx context.Context, tx pgx.Tx, actor, customer int64, operation, key, hash string, out map[string]any) error {
	if key == "" {
		return nil
	}
	return SaveOperation(ctx, tx, actor, fmt.Sprintf("customer_contact.%s:%d", operation, customer), key, hash, out)
}
func ensureContactEmail(ctx context.Context, tx pgx.Tx, email string, contactID int64) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=lower($1)) OR EXISTS(SELECT 1 FROM customer_contacts WHERE lower(email)=lower($1) AND id<>$2)`, email, contactID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return conflict("contact_email_exists", "该邮箱已被使用")
	}
	return nil
}
func (s *Store) CreateCustomerContact(ctx context.Context, actor, customer int64, admin bool, key string, in ContactInput) (map[string]any, bool, error) {
	if err := normalizeContactInput(&in); err != nil {
		return nil, false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if err = beginContactAuthority(ctx, tx, actor, customer, admin, true); err != nil {
		return nil, false, err
	}
	hash, out, replay, err := contactReceipt(ctx, tx, actor, customer, "create", key, in)
	if err != nil {
		return nil, false, err
	}
	if replay {
		return out, true, commitContact(ctx, tx, actor)
	}
	if err = ensureContactEmail(ctx, tx, in.Email, 0); err != nil {
		return nil, false, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO customer_contacts(customer_id,username,sex,avatar,company_name,email,wechat_id,country,province,city,region,address1,address2,postcode,phone_number,permissions,general_emails,invoice_emails,product_emails,support_emails,status)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) RETURNING id`, customer, in.Username, in.Sex, in.Avatar, in.CompanyName, in.Email, in.WechatID, in.Country, in.Province, in.City, in.Region, in.Address1, in.Address2, in.Postcode, in.PhoneNumber, in.Permissions, in.GeneralEmails, in.InvoiceEmails, in.ProductEmails, in.SupportEmails, in.Status).Scan(&id)
	if err != nil {
		return nil, false, err
	}
	if err = Audit(ctx, tx, actor, "customer_contact.created", "customer_contact", id, map[string]any{"customer_id": customer, "email": in.Email}); err != nil {
		return nil, false, err
	}
	out = map[string]any{"id": id, "customer_id": customer, "version": int64(1), "replayed": false}
	if err = saveContactReceipt(ctx, tx, actor, customer, "create", key, hash, out); err != nil {
		return nil, false, err
	}
	return out, false, commitContact(ctx, tx, actor)
}
func normalizeContactPatch(in *ContactPatch) error {
	if in.ExpectedVersion != nil && *in.ExpectedVersion < 1 {
		return conflict("invalid_contact_version", "联系人版本无效")
	}
	if in.Email != nil {
		*in.Email = strings.ToLower(strings.TrimSpace(*in.Email))
		parsed, err := mail.ParseAddress(*in.Email)
		if err != nil || parsed.Address != *in.Email || len(*in.Email) > 254 {
			return conflict("invalid_contact", "联系人邮箱无效")
		}
	}
	for _, field := range []*string{in.Username, in.CompanyName, in.PhoneNumber, in.Avatar, in.WechatID, in.Country, in.Province, in.City, in.Region, in.Address1, in.Address2, in.Postcode} {
		if field != nil {
			*field = strings.TrimSpace(*field)
		}
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "inactive" && *in.Status != "disabled" {
		return conflict("invalid_contact", "联系人状态无效")
	}
	if in.Sex != nil && (*in.Sex < 0 || *in.Sex > 2) {
		return conflict("invalid_contact", "联系人性别无效")
	}
	if in.Username != nil && len(*in.Username) > 120 || in.CompanyName != nil && len(*in.CompanyName) > 160 || in.PhoneNumber != nil && len(*in.PhoneNumber) > 80 {
		return conflict("invalid_contact", "联系人字段长度无效")
	}
	if in.Permissions != nil {
		if len(*in.Permissions) > 50 {
			return conflict("invalid_contact", "联系人权限无效")
		}
		for i, v := range *in.Permissions {
			v = strings.TrimSpace(v)
			if v == "" || len(v) > 80 {
				return conflict("invalid_contact", "联系人权限无效")
			}
			(*in.Permissions)[i] = v
		}
	}
	return nil
}
func (s *Store) UpdateCustomerContact(ctx context.Context, actor, customer, id int64, admin bool, key string, in ContactPatch) (map[string]any, error) {
	if err := normalizeContactPatch(&in); err != nil {
		var invalid *Conflict
		if errors.As(err, &invalid) {
			return nil, &AccountBusinessError{Status: 400, Code: invalid.Code, Message: invalid.Message}
		}
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = beginContactAuthority(ctx, tx, actor, customer, admin, true); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("update.%d", id)
	hash, out, replay, err := contactReceipt(ctx, tx, actor, customer, operation, key, in)
	if err != nil {
		return nil, err
	}
	if replay {
		return out, commitContact(ctx, tx, actor)
	}
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_contacts WHERE id=$1 AND customer_id=$2 FOR UPDATE", id, customer).Scan(&version); err != nil {
		return nil, err
	}
	if err = recheckContactCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion != version {
		return nil, conflict("version_conflict", "联系人已变更，请刷新后重试")
	}
	if in.Email != nil {
		if err = ensureContactEmail(ctx, tx, *in.Email, id); err != nil {
			return nil, err
		}
	}
	err = tx.QueryRow(ctx, `UPDATE customer_contacts SET username=COALESCE($3,username),sex=COALESCE($4,sex),avatar=COALESCE($5,avatar),company_name=COALESCE($6,company_name),email=COALESCE($7,email),wechat_id=COALESCE($8,wechat_id),country=COALESCE($9,country),province=COALESCE($10,province),city=COALESCE($11,city),region=COALESCE($12,region),address1=COALESCE($13,address1),address2=COALESCE($14,address2),postcode=COALESCE($15,postcode),phone_number=COALESCE($16,phone_number),permissions=COALESCE($17,permissions),general_emails=COALESCE($18,general_emails),invoice_emails=COALESCE($19,invoice_emails),product_emails=COALESCE($20,product_emails),support_emails=COALESCE($21,support_emails),status=COALESCE($22,status),version=version+1,updated_at=now() WHERE id=$1 AND customer_id=$2 RETURNING version`, id, customer, in.Username, in.Sex, in.Avatar, in.CompanyName, in.Email, in.WechatID, in.Country, in.Province, in.City, in.Region, in.Address1, in.Address2, in.Postcode, in.PhoneNumber, in.Permissions, in.GeneralEmails, in.InvoiceEmails, in.ProductEmails, in.SupportEmails, in.Status).Scan(&version)
	if err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "customer_contact.updated", "customer_contact", id, map[string]any{"customer_id": customer, "version": version}); err != nil {
		return nil, err
	}
	out = map[string]any{"id": id, "customer_id": customer, "version": version, "replayed": false}
	if err = saveContactReceipt(ctx, tx, actor, customer, operation, key, hash, out); err != nil {
		return nil, err
	}
	return out, commitContact(ctx, tx, actor)
}

type ContactDeleteInput struct {
	ExpectedVersion *int64 `json:"expected_version"`
}

func (s *Store) DeleteCustomerContact(ctx context.Context, actor, customer, id int64, admin bool, key string, in ContactDeleteInput) (map[string]any, error) {
	if in.ExpectedVersion != nil && *in.ExpectedVersion < 1 {
		return nil, &AccountBusinessError{Status: 400, Code: "invalid_contact_version", Message: "联系人版本无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = beginContactAuthority(ctx, tx, actor, customer, admin, true); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("delete.%d", id)
	hash, out, replay, err := contactReceipt(ctx, tx, actor, customer, operation, key, in)
	if err != nil {
		return nil, err
	}
	if replay {
		return out, commitContact(ctx, tx, actor)
	}
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_contacts WHERE id=$1 AND customer_id=$2 FOR UPDATE", id, customer).Scan(&version); err != nil {
		return nil, err
	}
	if err = recheckContactCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion != version {
		return nil, conflict("version_conflict", "联系人已变更，请刷新后重试")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM customer_contacts WHERE id=$1 AND customer_id=$2", id, customer); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "customer_contact.deleted", "customer_contact", id, map[string]any{"customer_id": customer, "version": version}); err != nil {
		return nil, err
	}
	out = map[string]any{"id": id, "customer_id": customer, "deleted": true, "replayed": false}
	if err = saveContactReceipt(ctx, tx, actor, customer, operation, key, hash, out); err != nil {
		return nil, err
	}
	return out, commitContact(ctx, tx, actor)
}
