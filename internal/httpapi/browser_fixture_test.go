package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in browser acceptance uses the same isolated schema and real HTTP API.
// The external runner closes the fixture by creating its unique finish file.
func TestLegacyReviewBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "review-super", func(db *store.Store, h http.Handler) { reviewFixture(t, db, h) })
}

func TestAdminAccessBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "access-super", func(db *store.Store, h http.Handler) {
		_, cookie := user(t, db, h, "access-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view','tickets.view'] WHERE name='access-reader-role'"); err != nil {
			t.Fatal(err)
		}
		_, customer := user(t, db, h, "access-customer", "customer")
		w := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "权限验收工单", "body": "客户请求", "priority": "normal"}, customer, "")
		expect(t, w, 201)
		expect(t, request(h, "GET", "/api/v1/admin/invoices", nil, cookie, ""), 200)
	})
}

func TestTicketScopeBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "scope-super", func(db *store.Store, h http.Handler) {
		staffID, staff := user(t, db, h, "scope-support", "admin")
		_, customer := user(t, db, h, "scope-customer", "customer")
		var technical, finance int64
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('技术支持','technical') RETURNING id").Scan(&technical); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('财务支持','finance') RETURNING id").Scan(&finance); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET ticket_scope='departments',ticket_unassigned=false WHERE id=$1", staffID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), "INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, technical); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['tickets.view','tickets.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
			t.Fatal(err)
		}
		for _, department := range []int64{technical, finance} {
			w := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": map[int64]string{technical: "服务器网络问题", finance: "付款凭证核验"}[department], "body": "请核查此请求", "priority": "normal", "department_id": department}, customer, "")
			expect(t, w, 201)
		}
		own := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "客服本人的客户工单", "body": "客户视角的请求", "priority": "normal", "department_id": technical}, staff, "")
		expect(t, own, 201)
	})
}

func TestCustomerSalesBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "sales-super", func(db *store.Store, h http.Handler) {
		first, _ := user(t, db, h, "sales-east", "admin")
		second, _ := user(t, db, h, "sales-west", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned' WHERE role='admin'"); err != nil {
			t.Fatal(err)
		}
		for i, owner := range []*int64{&first, &second, nil} {
			id, _ := user(t, db, h, []string{"customer-east", "customer-west", "customer-unassigned"}[i], "customer")
			if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", id, owner); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestSiteSettingsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "settings-super", func(db *store.Store, h http.Handler) {
		user(t, db, h, "settings-staff", "admin")
		user(t, db, h, "settings-customer", "customer")
	})
}

func TestCompiledAppsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "compiled-apps-super", func(db *store.Store, h http.Handler) {
		seedCompiledApps(t, db)
		readerID, _ := user(t, db, h, "apps-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
			t.Fatal(err)
		}
	})
}

func adminBrowserFixture(t *testing.T, username string, setup func(*store.Store, http.Handler)) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); err == nil {
			t.Fatal("browser fixture requires new ready and finish paths")
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	db, h := testServer(t)
	setup(db, h)
	user(t, db, h, username, "super_admin")
	origin := os.Getenv("ZJMFGO_BROWSER_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3089"
	}
	fixtureConfig := config.Config{PublicOrigin: origin, PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}
	if gatewayOrigin := os.Getenv("ZJMFGO_BROWSER_EPAY_API_URL"); gatewayOrigin != "" {
		fixtureConfig.ProvisionEncryptionKey = []byte("01234567890123456789012345678901")
		fixtureConfig.EpayAPIURL = gatewayOrigin
		fixtureConfig.EpayPID = "10001"
		fixtureConfig.EpayKey = "integration-epay-secret"
		fixtureConfig.EpayTypes = []string{"alipay", "wxpay"}
	}
	server := httptest.NewServer(New(db, fixtureConfig, slog.Default()).Handler())
	defer server.Close()
	var schema string
	if err := db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]string{"api_origin": server.URL, "username": username, "schema": schema})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("isolated browser fixture ready")
	limit := 15 * time.Minute
	if raw := os.Getenv("ZJMFGO_BROWSER_TIMEOUT_SECONDS"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 60 {
			limit = time.Duration(seconds) * time.Second
		}
	}
	if testDeadline, ok := t.Deadline(); ok {
		limit = min(limit, time.Until(testDeadline)-30*time.Second)
	}
	if limit <= 0 {
		t.Fatal("insufficient time for browser acceptance")
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if _, err := os.Stat(finish); err == nil {
				return
			}
		case <-deadline.C:
			t.Fatal("browser fixture was not closed before its cleanup deadline")
		}
	}
}
