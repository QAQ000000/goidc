package store

import "testing"

func TestPurchaseFieldValidation(t *testing.T) {
	fields := []PurchaseField{
		{Key: "host", Label: "主机名", Type: "text", Required: true, Active: true, MaxLength: 20, Pattern: `[a-z][a-z0-9-]+`},
		{Key: "purpose", Label: "用途", Type: "textarea", Active: true, MaxLength: 20},
		{Key: "region", Label: "地区", Type: "select", Required: true, Active: true, MaxLength: 20, Choices: []string{"上海", "广州"}},
		{Key: "accepted", Label: "确认", Type: "checkbox", Required: true, Active: true, MaxLength: 20},
		{Key: "url", Label: "网址", Type: "url", Active: true, MaxLength: 255},
	}
	values := map[string]string{"host": " web-01 ", "purpose": "第一行\n第二行", "region": "上海", "accepted": "true", "url": "https://example.test/path?q=x"}
	v, s, err := ValidatePurchaseFieldValues(fields, values)
	if err != nil || v["host"] != "web-01" || len(s) != 5 {
		t.Fatal("valid input", v, s, err)
	}
	for key, invalid := range map[string]string{"host": "bad_host", "purpose": "\x00", "region": "北京", "accepted": "false", "url": "javascript:alert(1)"} {
		x := map[string]string{}
		for k, v := range values {
			x[k] = v
		}
		x[key] = invalid
		if _, _, err = ValidatePurchaseFieldValues(fields, x); err == nil {
			t.Fatal("invalid value accepted", key)
		}
	}
	x := map[string]string{}
	for k, v := range values {
		x[k] = v
	}
	x["unknown"] = "value"
	if _, _, err = ValidatePurchaseFieldValues(fields, x); err == nil {
		t.Fatal("unknown field accepted")
	}
	fields[0].Active = false
	if _, _, err = ValidatePurchaseFieldValues(fields, values); err == nil {
		t.Fatal("inactive field accepted")
	}
}

func TestPurchaseFieldDefinitionAndUnicodeLimits(t *testing.T) {
	for _, f := range []PurchaseField{
		{Key: "bad-key", Label: "名称", Type: "text"},
		{Key: "x", Label: "名称", Type: "password"},
		{Key: "x", Label: "名称", Type: "text", Pattern: "(?=a)"},
		{Key: "x", Label: "名称", Type: "select", Choices: []string{"a", "a"}},
		{Key: "x", Label: "名称", Type: "select"},
		{Key: "x", Label: "名称", Type: "checkbox", MaxLength: 1},
	} {
		if normalizePurchaseField(&f) == nil {
			t.Fatal("invalid field definition accepted", f)
		}
	}
	field := PurchaseField{Key: "x", Label: "名称", Type: "text", Active: true, MaxLength: 2}
	if _, _, err := ValidatePurchaseFieldValues([]PurchaseField{field}, map[string]string{"x": "中文"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidatePurchaseFieldValues([]PurchaseField{field}, map[string]string{"x": "中文字"}); err == nil {
		t.Fatal("unicode limit bypassed")
	}
}
