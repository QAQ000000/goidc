package store

import (
	"errors"
	"net/url"
	"testing"
)

func TestAffiliateCustomerRoleValidation(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want string
		filters          url.Values
		invalid          bool
	}{
		{"default", "attributions", "either", url.Values{"customer_id": {"1"}}, false},
		{"global-default", "attributions", "either", nil, false},
		{"promoter", "attributions", "promoter", url.Values{"customer_id": {"1"}, "customer_role": {"promoter"}}, false},
		{"referred", "attributions", "referred", url.Values{"customer_id": {"1"}, "customer_role": {"referred"}}, false},
		{"either", "attributions", "either", url.Values{"customer_id": {"1"}, "customer_role": {"either"}}, false},
		{"empty", "attributions", "", url.Values{"customer_id": {"1"}, "customer_role": {""}}, true},
		{"duplicate", "attributions", "", url.Values{"customer_id": {"1"}, "customer_role": {"either", "either"}}, true},
		{"unknown", "attributions", "", url.Values{"customer_id": {"1"}, "customer_role": {"owner"}}, true},
		{"whitespace", "attributions", "", url.Values{"customer_id": {"1"}, "customer_role": {" either"}}, true},
		{"global-role", "attributions", "", url.Values{"customer_role": {"either"}}, true},
		{"account-role", "accounts", "", url.Values{"customer_id": {"1"}, "customer_role": {"promoter"}}, true},
		{"withdrawal-role", "withdrawals", "", url.Values{"customer_id": {"1"}, "customer_role": {"promoter"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := affiliateCustomerRole(tc.kind, tc.filters)
			if tc.invalid {
				var failure *AccountBusinessError
				if !errors.As(err, &failure) || failure.Status != 400 || failure.Code != "invalid_customer_role" {
					t.Fatalf("invalid role accepted: %q %v", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("role=%q error=%v want=%q", got, err, tc.want)
			}
		})
	}
}
