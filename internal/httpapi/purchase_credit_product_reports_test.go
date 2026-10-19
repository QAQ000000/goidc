package httpapi

import (
	"fmt"
	"testing"
	"time"
)

// Purchase credit authorizes the invoice before any cash is received. These
// flows exercise the real payment/repayment APIs rather than fabricating the
// service invoice_items that purchase checkout does not create.
func TestProductIncomeCreditPurchasesUseSnapshotsAndLegacyAllocation(t *testing.T) {
	for _, snapshot := range []bool{true, false} {
		name := "legacy-no-pricing-snapshot"
		if snapshot {
			name = "checkout-selective-discounts"
		}
		t.Run(name, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			first, second := f.productID, product(t, f.db, 10)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := f.db.Pool.Exec(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			var category int64
			if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO product_categories(name,slug,sort_order,active) VALUES('Credit category','credit-category',1,true) RETURNING id`).Scan(&category); err != nil {
				t.Fatal(err)
			}
			exec(`UPDATE products SET category_id=$1 WHERE id=$2`, category, first)
			var order, invoice int64
			firstQuantity := int64(1)
			if snapshot {
				// Only the first product receives a group reduction and a coupon:
				// 2 * 3 - 2 group - 2 coupon = 2; the second retains 3 cents.
				firstQuantity = 2
				group := purchasePricingGroup(t, f, "Credit selective", 0)
				purchasePricingMembership(t, f, group)
				purchasePricingRule(t, f, group, "Credit first product", "percent", 50)
				expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{
					"code": "CREDIT-SELECTIVE", "discount_type": "percent", "value": 50,
					"applies_customer_group_ids": []int64{group}, "applies_product_ids": []int64{first},
				}, f.root, "credit-selective-promo"), 201)
				// Checkout reads the per-cycle price, not the legacy catalog
				// columns alone. Set both after all catalog/group fixtures.
				exec(`UPDATE products SET price_cents=3,setup_fee_cents=0 WHERE id=ANY($1::bigint[])`, []int64{first, second})
				exec(`UPDATE product_prices SET price_cents=3,setup_fee_cents=0 WHERE product_id=ANY($1::bigint[]) AND billing_cycle='monthly'`, []int64{first, second})
				_, firstItem := add(t, f.h, f.customer, first, 2)
				version, _ := add(t, f.h, f.customer, second, 1)
				cartResponse := request(f.h, "GET", "/api/v1/cart", nil, f.customer, "")
				expect(t, cartResponse, 200)
				var secondItem int64
				for _, raw := range data(t, cartResponse)["items"].([]any) {
					row := raw.(map[string]any)
					if row["product_id"] == float64(second) {
						secondItem = int64(row["id"].(float64))
					}
				}
				if secondItem == 0 {
					t.Fatal("second cart product missing")
				}
				applied := request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": version, "code": "CREDIT-SELECTIVE"}, f.customer, "")
				expect(t, applied, 200)
				version = int64(data(t, applied)["version"].(float64))
				quote, input := purchasePricingQuote(t, f, version, firstItem, secondItem)
				expectPurchasePricingAmounts(t, quote, 9, 2, 2, 5)
				checkout := request(f.h, "POST", "/api/v1/orders", input, f.customer, "credit-selective-order")
				expect(t, checkout, 201)
				result := data(t, checkout)
				expectPurchasePricingAmounts(t, result, 9, 2, 2, 5)
				order = int64(result["order_id"].(float64))
				invoice = int64(result["invoice_id"].(float64))
				var firstNet, secondNet int64
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT max(p.net_cents) FILTER(WHERE oi.product_id=$2),max(p.net_cents) FILTER(WHERE oi.product_id=$3) FROM order_items oi JOIN order_item_pricing p ON p.order_item_id=oi.id WHERE oi.order_id=$1`, order, first, second).Scan(&firstNet, &secondNet); err != nil || firstNet != 2 || secondNet != 3 {
					t.Fatal("checkout did not freeze selective net weights", firstNet, secondNet, err)
				}
			} else {
				// A legacy fixture has no frozen pricing. Preserve its existing
				// per-receipt gross allocation: each 1-cent receipt goes to B,
				// unlike a cumulative allocation that would eventually give A 1.
				if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('credit-legacy-order',$1,3,'credit-legacy-order','fixture') RETURNING id`, f.customerID).Scan(&order); err != nil {
					t.Fatal(err)
				}
				for n, pid := range []int64{first, second} {
					exec(`INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,$3,'monthly',1,$4,0,$4)`, order, pid, fmt.Sprintf("Legacy product %d", pid), n+1)
				}
				if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('credit-legacy-invoice',$1,$2,3) RETURNING id`, f.customerID, order).Scan(&invoice); err != nil {
					t.Fatal(err)
				}
			}
			var invoiceItems int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM invoice_items WHERE invoice_id=$1`, invoice).Scan(&invoiceItems); err != nil || invoiceItems != 0 {
				t.Fatal("purchase regression must not depend on service invoice_items", invoiceItems, err)
			}
			exec(`UPDATE credit_settings SET enabled=true`)
			exec(`UPDATE customer_credit_accounts SET limit_cents=20000 WHERE user_id=$1`, f.customerID)
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", invoice), map[string]any{"expected_version": 1}, f.customer, "credit-purchase-payment"), 201)
			exec(`UPDATE invoices SET paid_at='2024-01-05T12:00:00+08' WHERE id=$1`, invoice)
			for _, endpoint := range []string{"product-income", "sales/details"} {
				response := request(f.h, "GET", "/api/v1/admin/reports/"+endpoint+"?year=2024&month=1", nil, f.root, "")
				expect(t, response, 200)
				out := data(t, response)
				if endpoint == "product-income" && len(out["products"].([]any)) != 0 || endpoint == "sales/details" && out["total"] != float64(0) {
					t.Fatalf("credit authorization was counted as cash: %s", response.Body.String())
				}
			}
			assertPeriod := func(period string, firstAmount, secondAmount int64) {
				t.Helper()
				want := map[int64]int64{first: firstAmount, second: secondAmount}
				response := request(f.h, "GET", "/api/v1/admin/reports/product-income?"+period, nil, f.root, "")
				expect(t, response, 200)
				rows := data(t, response)["products"].([]any)
				if len(rows) != 2 {
					t.Fatalf("purchase credit products missing: %s", response.Body.String())
				}
				for _, raw := range rows {
					row := raw.(map[string]any)
					pid := int64(row["product_id"].(float64))
					amount, found := want[pid]
					quantity := int64(1)
					if pid == first {
						quantity = firstQuantity
					}
					if !found || row["new_order_amount_cents"] != float64(amount) || row["total_amount_cents"] != float64(amount) || row["new_quantity"] != float64(quantity) || row["new_order_count"] != float64(1) || row["renewal_amount_cents"] != float64(0) {
						t.Fatalf("cash allocation or purchase quantities changed in %s: %s", period, response.Body.String())
					}
				}
				response = request(f.h, "GET", "/api/v1/admin/reports/sales/details?"+period, nil, f.root, "")
				expect(t, response, 200)
				out := data(t, response)
				if out["total"] != float64(1) {
					t.Fatalf("sales receipt invoice missing: %s", response.Body.String())
				}
				row := out["items"].([]any)[0].(map[string]any)
				lines := row["items"].([]any)
				if row["amount_cents"] != float64(firstAmount+secondAmount) || len(lines) != 2 {
					t.Fatalf("sales cash total disagrees with products: %s", response.Body.String())
				}
				for _, raw := range lines {
					line := raw.(map[string]any)
					pid := int64(line["product_id"].(float64))
					if amount, found := want[pid]; !found || line["amount_cents"] != float64(amount) || line["credit"] != true {
						t.Fatalf("sales and product allocations diverged: %s", response.Body.String())
					}
				}
				for _, filter := range []string{fmt.Sprintf("product_id=%d", first), fmt.Sprintf("category_id=%d", category)} {
					response = request(f.h, "GET", "/api/v1/admin/reports/product-income/groups?"+period+"&"+filter, nil, f.root, "")
					expect(t, response, 200)
					out = data(t, response)
					summary := out["summary"].(map[string]any)
					if out["total"] != float64(1) || summary["total_amount_cents"] != float64(firstAmount) || summary["new_quantity"] != float64(firstQuantity) || summary["new_order_count"] != float64(1) {
						t.Fatalf("filter reallocated cash or duplicated receipt quantities: %s", response.Body.String())
					}
				}
			}
			for n, day := range []string{"2024-01-15", "2024-01-16", "2024-02-15"} {
				amount := int64(1)
				if snapshot && n == 2 {
					amount = 3
				}
				receipt := fmt.Sprintf("purchase-credit-receipt-%d", n)
				response := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/credit/consumptions/%d/repay", invoice), map[string]any{
					"amount_cents": amount, "expected_version": n + 1, "receipt": receipt, "received_at": day + "T12:00:00+08:00",
				}, f.root, receipt)
				expect(t, response, 201)
				if n < 2 {
					assertPeriod("year=2024&month=1", 0, int64(n+1))
				}
			}
			assertPeriod("year=2024&month=1", 0, 2)
			annualFirst, annualSecond := int64(0), int64(3)
			if snapshot {
				assertPeriod("year=2024&month=2", 2, 1)
				annualFirst, annualSecond = 2, 3
			} else {
				assertPeriod("year=2024&month=2", 0, 1)
			}
			// The product UI endpoint is monthly; exercise its shared read model
			// with a full-year window to verify cross-period conservation too.
			zone := time.FixedZone("Asia/Shanghai", 8*60*60)
			annual, err := f.db.ProductIncomeRows(t.Context(), time.Date(2024, 1, 1, 0, 0, 0, 0, zone), time.Date(2025, 1, 1, 0, 0, 0, 0, zone), f.staffID)
			if err != nil || len(annual) != 2 {
				t.Fatal("full-year product receipts missing", annual, err)
			}
			for _, row := range annual {
				amount, quantity := annualSecond, int64(1)
				if row.ProductID == first {
					amount, quantity = annualFirst, firstQuantity
				} else if row.ProductID != second {
					t.Fatal("unexpected annual product", row)
				}
				if row.NewOrderAmountCents != amount || row.NewOrderCount != 1 || row.NewQuantity != quantity {
					t.Fatal("full-year receipts duplicated money or quantity", annual)
				}
			}
		})
	}
}
