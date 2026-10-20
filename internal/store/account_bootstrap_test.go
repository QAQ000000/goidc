package store

import "testing"

func TestAdministratorBootstrapUsesSeparateTableWithoutCustomerCart(t *testing.T) {
	db := isolatedTestStore(t)
	if err := db.Bootstrap(t.Context(), "separate-bootstrap", "bootstrap@example.test", "bootstrap-integration-password"); err != nil {
		t.Fatal(err)
	}
	var administrators, customers, carts, principals int
	if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM administrators WHERE role='super_admin'),(SELECT count(*) FROM users),(SELECT count(*) FROM carts),(SELECT count(*) FROM account_principals WHERE kind='administrator')").Scan(&administrators, &customers, &carts, &principals); err != nil || administrators != 1 || customers != 0 || carts != 0 || principals != 1 {
		t.Fatalf("bootstrap admin=%d customer=%d cart=%d principal=%d err=%v", administrators, customers, carts, principals, err)
	}
	if err := db.Bootstrap(t.Context(), "second-bootstrap", "second@example.test", "bootstrap-integration-password"); err == nil {
		t.Fatal("bootstrap overwrote existing administrator")
	}
}
