package store

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type PurchaseField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Active      bool     `json:"active"`
	ShowInvoice bool     `json:"show_invoice"`
	MaxLength   int      `json:"max_length"`
	Pattern     string   `json:"pattern"`
	Choices     []string `json:"choices"`
	SortOrder   int      `json:"sort_order"`
}
type PurchaseFieldSnapshot struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Value       string `json:"value"`
	ShowInvoice bool   `json:"show_invoice"`
}
type PurchaseFieldsInput struct {
	ExpectedRevision int64           `json:"expected_revision"`
	Fields           []PurchaseField `json:"fields"`
}

var purchaseFieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func fieldPattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(`\A(?:` + pattern + `)\z`)
}
func normalizePurchaseField(f *PurchaseField) error {
	f.Key = strings.TrimSpace(f.Key)
	f.Label = strings.TrimSpace(f.Label)
	f.Description = strings.TrimSpace(f.Description)
	if f.MaxLength == 0 {
		f.MaxLength = 255
	}
	if !purchaseFieldKey.MatchString(f.Key) || !contentTextValid(f.Label, 120, false) || !contentTextValid(f.Description, 500, true) || f.MaxLength < 1 || f.MaxLength > 2048 || len(f.Pattern) > 500 || f.SortOrder < 0 || f.SortOrder > 1000000 {
		return conflict("invalid_purchase_fields", "购买字段的名称、标识、长度或排序无效")
	}
	switch f.Type {
	case "text", "textarea", "select", "checkbox", "url":
	default:
		return conflict("invalid_purchase_fields", "购买字段类型无效")
	}
	if f.Type == "checkbox" && f.MaxLength < 4 {
		return conflict("invalid_purchase_fields", "勾选字段长度不能小于4")
	}
	if f.Pattern != "" {
		if f.Type == "checkbox" || f.Type == "select" {
			return conflict("invalid_purchase_fields", "下拉和勾选字段不使用正则规则")
		}
		if _, err := fieldPattern(f.Pattern); err != nil {
			return conflict("invalid_purchase_fields", "字段格式规则不是有效的 RE2 正则表达式")
		}
	}
	if f.Choices == nil {
		f.Choices = []string{}
	}
	if f.Type != "select" && len(f.Choices) > 0 {
		return conflict("invalid_purchase_fields", "只有下拉字段可配置选项")
	}
	if len(f.Choices) > 50 || f.Type == "select" && len(f.Choices) == 0 {
		return conflict("invalid_purchase_fields", "下拉字段需要 1-50 个选项")
	}
	seen := map[string]bool{}
	for i, value := range f.Choices {
		value = strings.TrimSpace(value)
		if !contentTextValid(value, 120, false) || utf8.RuneCountInString(value) > f.MaxLength || seen[value] {
			return conflict("invalid_purchase_fields", "下拉选项为空、重复或过长")
		}
		seen[value] = true
		f.Choices[i] = value
	}
	return nil
}

func (s *Store) SavePurchaseFields(ctx context.Context, actor, product int64, in PurchaseFieldsInput) error {
	if len(in.Fields) > 20 {
		return conflict("invalid_purchase_fields", "每个商品最多20个购买字段")
	}
	seen := map[string]bool{}
	for i := range in.Fields {
		f := &in.Fields[i]
		if err := normalizePurchaseField(f); err != nil {
			return err
		}
		if seen[f.Key] {
			return conflict("invalid_purchase_fields", "购买字段标识重复")
		}
		seen[f.Key] = true
	}
	tx, err := s.configurationTx(ctx, actor, product, &in.ExpectedRevision, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "DELETE FROM product_purchase_fields WHERE product_id=$1", product); err != nil {
		return err
	}
	for _, f := range in.Fields {
		if _, err = tx.Exec(ctx, `INSERT INTO product_purchase_fields(product_id,field_key,label,field_type,description,required,active,show_invoice,max_length,validation_pattern,choices,sort_order) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, product, f.Key, f.Label, f.Type, f.Description, f.Required, f.Active, f.ShowInvoice, f.MaxLength, f.Pattern, f.Choices, f.SortOrder); err != nil {
			return err
		}
	}
	return configurationCommit(ctx, tx, actor, product, product, "product.purchase_fields.updated")
}

// CatalogReadLock is held by purchase callers. Rules, version and snapshots share
// a catalog snapshot; catalog writers cannot alter definitions during validation.
func ValidatePurchaseFields(ctx context.Context, tx pgx.Tx, product int64, values map[string]string) (map[string]string, []PurchaseFieldSnapshot, error) {
	if len(values) > 20 {
		return nil, nil, conflict("invalid_purchase_fields", "购买信息字段过多")
	}
	rows, err := tx.Query(ctx, `SELECT field_key,label,field_type,description,required,active,show_invoice,max_length,validation_pattern,choices,sort_order FROM product_purchase_fields WHERE product_id=$1 ORDER BY sort_order,field_key`, product)
	if err != nil {
		return nil, nil, err
	}
	fields, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PurchaseField, error) {
		var f PurchaseField
		err := row.Scan(&f.Key, &f.Label, &f.Type, &f.Description, &f.Required, &f.Active, &f.ShowInvoice, &f.MaxLength, &f.Pattern, &f.Choices, &f.SortOrder)
		return f, err
	})
	if err != nil {
		return nil, nil, err
	}
	return ValidatePurchaseFieldValues(fields, values)
}
func ValidatePurchaseFieldValues(fields []PurchaseField, values map[string]string) (map[string]string, []PurchaseFieldSnapshot, error) {
	canonical := map[string]string{}
	snapshots := []PurchaseFieldSnapshot{}
	known := map[string]bool{}
	for _, f := range fields {
		if !f.Active {
			continue
		}
		known[f.Key] = true
		value := strings.TrimSpace(values[f.Key])
		if f.Type == "checkbox" && value == "false" {
			value = ""
		}
		if value == "" {
			if f.Required {
				return nil, nil, conflict("purchase_field_required", "请填写："+f.Label)
			}
			continue
		}
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > f.MaxLength {
			return nil, nil, conflict("invalid_purchase_fields", f.Label+" 超过允许长度")
		}
		for _, r := range value {
			if unicode.IsControl(r) && !(f.Type == "textarea" && (r == '\n' || r == '\t' || r == '\r')) {
				return nil, nil, conflict("invalid_purchase_fields", f.Label+" 包含无效字符")
			}
		}
		switch f.Type {
		case "select":
			if !containsString(f.Choices, value) {
				return nil, nil, conflict("invalid_purchase_fields", f.Label+" 的选项无效")
			}
		case "checkbox":
			if value != "true" {
				return nil, nil, conflict("invalid_purchase_fields", f.Label+" 必须是勾选值")
			}
		case "url":
			u, e := url.Parse(value)
			if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return nil, nil, conflict("invalid_purchase_fields", f.Label+" 必须是 HTTP 或 HTTPS 网址")
			}
		}
		if f.Pattern != "" {
			re, e := fieldPattern(f.Pattern)
			if e != nil || !re.MatchString(value) {
				return nil, nil, conflict("invalid_purchase_fields", f.Label+" 不符合格式规则")
			}
		}
		canonical[f.Key] = value
		snapshots = append(snapshots, PurchaseFieldSnapshot{Key: f.Key, Label: f.Label, Type: f.Type, Value: value, ShowInvoice: f.ShowInvoice})
	}
	for key := range values {
		if !known[key] {
			return nil, nil, conflict("invalid_purchase_fields", "存在已停用或未知的购买字段，请重新确认")
		}
	}
	return canonical, snapshots, nil
}
