package store

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckoutInputValidationBeforeDatabase(t *testing.T) {
	// A nil pool ensures malformed input cannot acquire a connection or mutate
	// cart/receipt state before it is classified as an input error.
	db := &Store{}
	for _, test := range []struct {
		name, key, code string
		input           CheckoutInput
	}{
		{name: "missing key", code: "invalid_checkout", input: CheckoutInput{CartVersion: 1, ItemIDs: []int64{1}}},
		{name: "oversized key", key: strings.Repeat("a", 129), code: "invalid_checkout", input: CheckoutInput{CartVersion: 1, ItemIDs: []int64{1}}},
		{name: "invalid version", key: "valid-checkout-key", code: "invalid_checkout", input: CheckoutInput{ItemIDs: []int64{1}}},
		{name: "empty selection", key: "valid-checkout-key", code: "invalid_checkout", input: CheckoutInput{CartVersion: 1}},
		{name: "oversized selection", key: "valid-checkout-key", code: "invalid_checkout", input: CheckoutInput{CartVersion: 1, ItemIDs: make([]int64, 101)}},
		{name: "invalid ID", key: "valid-checkout-key", code: "invalid_selection", input: CheckoutInput{CartVersion: 1, ItemIDs: []int64{1, 0}}},
		{name: "duplicate ID", key: "valid-checkout-key", code: "invalid_selection", input: CheckoutInput{CartVersion: 1, ItemIDs: []int64{2, 1, 2}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := db.Checkout(t.Context(), 1, test.key, test.input)
			assertInputValidation(t, err, test.code)
		})
	}
}

func TestContactInputValidationBeforeDatabase(t *testing.T) {
	db := &Store{}
	for _, test := range []struct {
		name  string
		input ContactInput
	}{
		{name: "missing email"},
		{name: "invalid email", input: ContactInput{Email: "invalid"}},
		{name: "display name in email", input: ContactInput{Email: "Name <person@example.test>"}},
		{name: "invalid status", input: ContactInput{Email: "person@example.test", Status: "unknown"}},
		{name: "invalid sex", input: ContactInput{Email: "person@example.test", Sex: 3}},
		{name: "oversized field", input: ContactInput{Email: "person@example.test", Username: strings.Repeat("a", 121)}},
		{name: "empty permission", input: ContactInput{Email: "person@example.test", Permissions: []string{" "}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := db.CreateCustomerContact(t.Context(), 1, 1, false, "", test.input)
			assertInputValidation(t, err, "invalid_contact")
		})
	}
}

func TestContactInputNormalizationRetained(t *testing.T) {
	in := ContactInput{Email: " PERSON@EXAMPLE.TEST ", Username: " Name ", Permissions: []string{" invoices.view "}}
	if err := normalizeContactInput(&in); err != nil {
		t.Fatal(err)
	}
	if in.Email != "person@example.test" || in.Username != "Name" || in.Status != "inactive" || len(in.Permissions) != 1 || in.Permissions[0] != "invoices.view" {
		t.Fatalf("normalization changed: %+v", in)
	}
}

func assertInputValidation(t *testing.T, err error, code string) {
	t.Helper()
	var invalid *ValidationError
	var stateConflict *Conflict
	if !errors.As(err, &invalid) || invalid.Code != code || invalid.Message == "" || errors.As(err, &stateConflict) {
		t.Fatalf("expected input validation %q, got %T: %v", code, err, err)
	}
}
