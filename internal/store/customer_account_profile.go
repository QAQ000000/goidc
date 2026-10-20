package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type CustomerProfileInput struct {
	Gender              string         `json:"gender"`
	CompanyName         string         `json:"company_name"`
	Phone               string         `json:"phone"`
	QQ                  string         `json:"qq"`
	Country             string         `json:"country"`
	Province            string         `json:"province"`
	Address             string         `json:"address"`
	Postcode            string         `json:"postcode"`
	SourceChannel       string         `json:"source_channel"`
	PaymentMethod       string         `json:"payment_method"`
	Language            string         `json:"language"`
	AdminNote           string         `json:"admin_note"`
	ReceiveMarketing    *bool          `json:"receive_marketing"`
	CustomFields        map[string]any `json:"custom_fields"`
	ExpectedVersion     int64          `json:"expected_version"`
	CustomFieldsVersion int64          `json:"custom_fields_version"`
}

type CustomerCustomFieldDefinition struct {
	Type, Pattern string
	Choices       []string
	Required      bool
}

// Nil means retain existing values, but never bypass active required fields.
// Catalogue row locks prevent definition changes until the profile commits.
func validateCustomerCustomFields(ctx context.Context, tx pgx.Tx, values, previous map[string]any, expected int64) (map[string]any, error) {
	var version int64
	if err := tx.QueryRow(ctx, "SELECT version FROM customer_field_catalogue WHERE id=1 FOR SHARE").Scan(&version); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT field_key,field_type,validation_pattern,choices,required FROM customer_custom_fields WHERE active ORDER BY id`)
	if err != nil {
		return nil, err
	}
	allowed := map[string]CustomerCustomFieldDefinition{}
	for rows.Next() {
		var key string
		var def CustomerCustomFieldDefinition
		if err := rows.Scan(&key, &def.Type, &def.Pattern, &def.Choices, &def.Required); err != nil {
			rows.Close()
			return nil, err
		}
		allowed[key] = def
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if (values != nil || len(allowed) > 0 || expected > 0) && expected != version {
		return nil, &Conflict{Code: "stale_customer_fields", Message: "客户字段定义已变化，请刷新后重试"}
	}
	return ValidateCustomerCustomFieldValues(allowed, values, previous)
}

// Shared value semantics; callers choose which definitions may be submitted.
func ValidateCustomerCustomFieldValues(allowed map[string]CustomerCustomFieldDefinition, values, previous map[string]any) (map[string]any, error) {
	out := map[string]any{}
	// Retain values for definitions that have been disabled or deleted.
	for key, value := range previous {
		if _, active := allowed[key]; !active || values == nil {
			out[key] = value
		}
	}
	for key, value := range values {
		def, ok := allowed[key]
		if !ok {
			old, exists := previous[key]
			if !exists || !reflect.DeepEqual(old, value) {
				return nil, fmt.Errorf("invalid_custom_field")
			}
			continue
		}
		if value == nil {
			if def.Required {
				return nil, fmt.Errorf("invalid_custom_field")
			}
			out[key] = nil
			continue
		}
		text := fmt.Sprint(value)
		empty := false
		switch def.Type {
		case "text", "textarea", "date", "select":
			str, ok := value.(string)
			if !ok || strings.ContainsRune(str, 0) || utf8.RuneCountInString(str) > 5000 {
				return nil, fmt.Errorf("invalid_custom_field")
			}
			empty = strings.TrimSpace(str) == ""
			if empty && def.Required {
				return nil, fmt.Errorf("invalid_custom_field")
			}
			if !empty && def.Type == "date" {
				if _, err := time.Parse("2006-01-02", str); err != nil {
					return nil, fmt.Errorf("invalid_custom_field")
				}
			}
		case "number":
			n, ok := value.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, fmt.Errorf("invalid_custom_field")
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("invalid_custom_field")
			}
		}
		if !empty && def.Type == "select" {
			found := false
			for _, choice := range def.Choices {
				if text == choice {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("invalid_custom_field")
			}
		}
		if !empty && def.Pattern != "" {
			matched, e := regexp.MatchString(def.Pattern, text)
			if e != nil || !matched {
				return nil, fmt.Errorf("invalid_custom_field")
			}
		}
		out[key] = value
	}
	// Nil retains active values, which must still match the current definition.
	if values == nil && len(previous) > 0 {
		retained := map[string]any{}
		for key, value := range previous {
			if _, ok := allowed[key]; ok {
				retained[key] = value
			}
		}
		if len(retained) > 0 {
			return ValidateCustomerCustomFieldValues(allowed, retained, previous)
		}
	}
	for key, def := range allowed {
		if def.Required {
			value, ok := out[key]
			if !ok || value == nil {
				return nil, fmt.Errorf("invalid_custom_field")
			}
			if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("invalid_custom_field")
			}
		}
	}
	return out, nil
}

func (p *CustomerProfileInput) Valid() bool {
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

func saveCustomerProfile(ctx context.Context, tx pgx.Tx, id int64, p CustomerProfileInput) error {
	marketing := true
	if p.ReceiveMarketing != nil {
		marketing = *p.ReceiveMarketing
	}
	var previous map[string]any
	var previousRaw []byte
	err := tx.QueryRow(ctx, "SELECT custom_fields FROM customer_profiles WHERE customer_id=$1 FOR UPDATE", id).Scan(&previousRaw)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if len(previousRaw) > 0 {
		if err = json.Unmarshal(previousRaw, &previous); err != nil {
			return err
		}
	}
	custom, err := validateCustomerCustomFields(ctx, tx, p.CustomFields, previous, p.CustomFieldsVersion)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(custom)
	if err != nil {
		return err
	}
	if p.ExpectedVersion > 0 {
		var current int64
		if err = tx.QueryRow(ctx, "SELECT version FROM customer_profiles WHERE customer_id=$1 FOR UPDATE", id).Scan(&current); err != nil && err != pgx.ErrNoRows {
			return err
		}
		if err == nil && current != p.ExpectedVersion {
			return fmt.Errorf("stale_customer_profile")
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO customer_profiles(customer_id,gender,company_name,phone,qq,country,province,address,postcode,source_channel,payment_method,language,admin_note,receive_marketing,custom_fields,version)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	 ON CONFLICT(customer_id) DO UPDATE SET gender=EXCLUDED.gender,company_name=EXCLUDED.company_name,phone=EXCLUDED.phone,qq=EXCLUDED.qq,country=EXCLUDED.country,province=EXCLUDED.province,address=EXCLUDED.address,postcode=EXCLUDED.postcode,source_channel=EXCLUDED.source_channel,payment_method=EXCLUDED.payment_method,language=EXCLUDED.language,admin_note=EXCLUDED.admin_note,receive_marketing=EXCLUDED.receive_marketing,custom_fields=EXCLUDED.custom_fields,version=customer_profiles.version+1,updated_at=now()`,
		id, p.Gender, p.CompanyName, p.Phone, p.QQ, p.Country, p.Province, p.Address, p.Postcode, p.SourceChannel, p.PaymentMethod, p.Language, p.AdminNote, marketing, raw, 1)
	return err
}

func currentCustomerAccountProfile(ctx context.Context, tx pgx.Tx, id int64) (CustomerProfileInput, error) {
	var p CustomerProfileInput
	var raw []byte
	// Old or independently-created accounts can lack a profile row.
	if _, err := tx.Exec(ctx, "INSERT INTO customer_profiles(customer_id,company_name,phone) SELECT $1,COALESCE((SELECT company_name FROM customer_contacts WHERE customer_id=$1 AND company_name<>'' ORDER BY id LIMIT 1),''),COALESCE((SELECT phone_number FROM customer_contacts WHERE customer_id=$1 AND phone_number<>'' ORDER BY id LIMIT 1),'') ON CONFLICT DO NOTHING", id); err != nil {
		return p, err
	}
	err := tx.QueryRow(ctx, `SELECT gender,company_name,phone,qq,country,province,address,postcode,source_channel,payment_method,language,admin_note,receive_marketing,custom_fields,version FROM customer_profiles WHERE customer_id=$1 FOR UPDATE`, id).Scan(&p.Gender, &p.CompanyName, &p.Phone, &p.QQ, &p.Country, &p.Province, &p.Address, &p.Postcode, &p.SourceChannel, &p.PaymentMethod, &p.Language, &p.AdminNote, &p.ReceiveMarketing, &raw, &p.ExpectedVersion)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p.CustomFields)
	return p, err
}
