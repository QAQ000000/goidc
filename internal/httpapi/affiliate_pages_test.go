package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

func TestAdminAffiliatePagesSearchStatusCountAndOffset(t *testing.T) {
	db, h, _, _, _, _, _, _ := setupAffiliateFixture(t)
	_, admin := user(t, db, h, "affiliate-page-admin", "super_admin")
	for i := 0; i < 3; i++ {
		_, owner := user(t, db, h, fmt.Sprintf("page-owner-%d", i), "customer")
		expect(t, request(h, "POST", "/api/v1/affiliate/apply", nil, owner, fmt.Sprintf("page-apply-%d", i)), 201)
	}
	for _, tc := range []struct {
		query       string
		total, rows int
	}{
		{"search=page-owner&limit=2", 3, 2},
		{"search=page-owner&limit=2&offset=2", 3, 1},
		{"search=page-owner&limit=2&offset=100", 3, 0},
		{"search=page-owner&status=approved", 0, 0},
		{"search=page-owner-1&status=pending", 1, 1},
		{"search=%25", 0, 0},
	} {
		response := request(h, "GET", "/api/v1/admin/affiliate/accounts/page?"+tc.query, nil, admin, "")
		expect(t, response, 200)
		result := data(t, response)
		if int(result["total"].(float64)) != tc.total || len(result["items"].([]any)) != tc.rows {
			t.Fatalf("query=%s data=%v", tc.query, result)
		}
	}
	_, customer := user(t, db, h, "affiliate-page-customer", "customer")
	for _, kind := range []string{"accounts", "attributions", "withdrawals"} {
		expect(t, request(h, http.MethodGet, "/api/v1/admin/affiliate/"+kind+"/page", nil, customer, ""), 403)
		response := request(h, http.MethodGet, "/api/v1/admin/affiliate/"+kind+"/page?search=missing", nil, admin, "")
		expect(t, response, 200)
		result := data(t, response)
		if result["total"].(float64) != 0 || len(result["items"].([]any)) != 0 {
			t.Fatalf("empty %s=%v", kind, result)
		}
	}
}
