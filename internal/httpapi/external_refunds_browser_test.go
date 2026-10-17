package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Run only with the isolated browser ready/finish environment. Credentials are
// generated through the common test login helper; no deployment data is read.
func TestExternalRefundBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "refund-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		_, requester := externalRefundActor(t, db, h, "refund-requester", []string{"invoices.view", "external_refunds.view", "external_refunds.create"})
		_, reviewer := externalRefundActor(t, db, h, "refund-reviewer", []string{"invoices.view", "external_refunds.view", "external_refunds.review"})
		externalRefundActor(t, db, h, "refund-reader", []string{"invoices.view", "external_refunds.view"})
		externalRefundActor(t, db, h, "refund-denied", []string{"invoices.view"})
		for i := 0; i < 5; i++ {
			invoice, in := externalRefundPaidInvoice(t, db, h, f.root, f.customer, f.uid, fmt.Sprintf("browser-external-%d", i))
			// First three paid bills are empty for create/cancel/responsive acceptance;
			// remaining bills provide independent-review and rejection scenarios.
			if i == 4 {
				// Real rejected history exercises paging without fabricating rows.
				for n := 0; n < 26; n++ {
					in["provider_refund_id"] = fmt.Sprintf("browser-history-%d", n)
					w := request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice), in, requester, fmt.Sprintf("browser-history-create-%d", n))
					expect(t, w, 201)
					refund := data(t, w)["refund"].(map[string]any)
					expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds/%.0f/reject", invoice, refund["id"]), map[string]any{"expected_version": 1, "note": "隔离分页历史：凭证待补正"}, reviewer, fmt.Sprintf("browser-history-reject-%d", n)), 201)
				}
				in["provider_refund_id"] = "browser-history-current"
			}
			if i >= 3 {
				expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice), in, requester, fmt.Sprintf("browser-refund-%d", i)), 201)
			}
			t.Logf("external refund browser invoice=%d scenario=%d", invoice, i)
		}
	})
}
