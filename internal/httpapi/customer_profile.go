package httpapi

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type customerProfileInput struct {
	Gender           string `json:"gender"`
	CompanyName      string `json:"company_name"`
	Phone            string `json:"phone"`
	QQ               string `json:"qq"`
	Country          string `json:"country"`
	Province         string `json:"province"`
	Address          string `json:"address"`
	Postcode         string `json:"postcode"`
	SourceChannel    string `json:"source_channel"`
	PaymentMethod    string `json:"payment_method"`
	Language         string `json:"language"`
	AdminNote        string `json:"admin_note"`
	ReceiveMarketing *bool  `json:"receive_marketing"`
}

func (p *customerProfileInput) valid() bool {
	for _, field := range []struct {
		value *string
		limit int
	}{
		{&p.CompanyName, 200}, {&p.Phone, 80}, {&p.QQ, 40}, {&p.Country, 100}, {&p.Province, 100},
		{&p.Address, 500}, {&p.Postcode, 40}, {&p.SourceChannel, 200}, {&p.AdminNote, 4000},
	} {
		*field.value = strings.TrimSpace(*field.value)
		if utf8.RuneCountInString(*field.value) > field.limit || strings.ContainsRune(*field.value, 0) {
			return false
		}
	}
	if p.Gender == "" {
		p.Gender = "unknown"
	}
	if p.PaymentMethod == "" {
		p.PaymentMethod = "bank"
	}
	if p.Language == "" {
		p.Language = "zh_CN"
	}
	return (p.Gender == "unknown" || p.Gender == "male" || p.Gender == "female") &&
		(p.PaymentMethod == "bank" || p.PaymentMethod == "alipay" || p.PaymentMethod == "wechat") &&
		(p.Language == "zh_CN" || p.Language == "en_US")
}

func saveCustomerProfile(ctx context.Context, tx pgx.Tx, id int64, p customerProfileInput) error {
	marketing := true
	if p.ReceiveMarketing != nil {
		marketing = *p.ReceiveMarketing
	}
	_, err := tx.Exec(ctx, `INSERT INTO customer_profiles(customer_id,gender,company_name,phone,qq,country,province,address,postcode,source_channel,payment_method,language,admin_note,receive_marketing)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT(customer_id) DO UPDATE SET gender=EXCLUDED.gender,company_name=EXCLUDED.company_name,phone=EXCLUDED.phone,qq=EXCLUDED.qq,country=EXCLUDED.country,province=EXCLUDED.province,address=EXCLUDED.address,postcode=EXCLUDED.postcode,source_channel=EXCLUDED.source_channel,payment_method=EXCLUDED.payment_method,language=EXCLUDED.language,admin_note=EXCLUDED.admin_note,receive_marketing=EXCLUDED.receive_marketing,updated_at=now()`,
		id, p.Gender, p.CompanyName, p.Phone, p.QQ, p.Country, p.Province, p.Address, p.Postcode, p.SourceChannel, p.PaymentMethod, p.Language, p.AdminNote, marketing)
	return err
}

// Serialize permission checks with staff/role mutations, then verify the request's credential.
func lockCustomerManager(ctx context.Context, tx pgx.Tx, actor int64) (super, group, sales bool, err error) {
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return
	}
	if err = store.LockFileCredential(ctx, tx, actor); err != nil {
		return
	}
	var manage bool
	err = tx.QueryRow(ctx, `SELECT u.status='active' AND u.role='super_admin',
 COALESCE(u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND r.active AND 'customers.manage'=ANY(r.permissions))),false),
 COALESCE(u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND r.active AND 'customer_groups.manage'=ANY(r.permissions))),false),u.sales_enabled
 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, actor).Scan(&super, &manage, &group, &sales)
	if err == nil && !manage {
		err = &store.Conflict{Code: "permission_denied", Message: "没有管理客户的权限"}
	}
	return
}
