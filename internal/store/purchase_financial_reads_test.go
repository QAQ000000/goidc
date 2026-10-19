package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

type financialPurchaseFixture struct {
	order, invoice  int64
	products, lines []int64
}

// These fixtures intentionally contain both snapshotted and pre-migration
// purchases: report reads must preserve the old contract without a backfill.
func financialPurchase(t *testing.T, db *Store, customer int64, key string, paidAt time.Time, gross, groupDiscount, promoDiscount []int64, paid int64) financialPurchaseFixture {
	t.Helper()
	f := financialPurchaseFixture{}
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var total int64
	for _, value := range gross {
		total += value
	}
	contractTotal := paid
	if groupDiscount != nil {
		contractTotal = total
		for index := range gross {
			contractTotal -= groupDiscount[index] + promoDiscount[index]
		}
	}
	if err := tx.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,discount_cents,idempotency_key,request_hash)
VALUES($1,$2,'paid',$3,$4,$1,'financial-fixture') RETURNING id`, key, customer, contractTotal, total-contractTotal).Scan(&f.order); err != nil {
		t.Fatal(err)
	}
	for index, amount := range gross {
		name := fmt.Sprintf("%s-product-%d", key, index)
		var product, line int64
		if err := tx.QueryRow(t.Context(), `INSERT INTO products(name,slug,price_cents) VALUES($1,$1,1000) RETURNING id`, name).Scan(&product); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,$3,'monthly',1,$4,0,$4) RETURNING id`, f.order, product, name, amount).Scan(&line); err != nil {
			t.Fatal(err)
		}
		f.products = append(f.products, product)
		f.lines = append(f.lines, line)
		if groupDiscount != nil {
			net := amount - groupDiscount[index] - promoDiscount[index]
			if _, err := tx.Exec(t.Context(), `INSERT INTO order_item_pricing(order_item_id,gross_cents,group_discount_cents,promo_discount_cents,net_cents,snapshot)
VALUES($1,$2,$3,$4,$5,'{"fixture":true}')`, line, amount, groupDiscount[index], promoDiscount[index], net); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at)
VALUES($1,$2,$3,'paid',$4,$5) RETURNING id`, key, customer, f.order, paid, paidAt).Scan(&f.invoice); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

type financialSalesResult struct {
	Items []struct {
		InvoiceID int64 `json:"invoice_id"`
		Amount    int64 `json:"amount_cents"`
		Lines     []struct {
			ProductID int64 `json:"product_id"`
			Amount    int64 `json:"amount_cents"`
		} `json:"items"`
	} `json:"items"`
	Total int64 `json:"total"`
}

func financialSalesRead(t *testing.T, db *Store, actor int64, start, end time.Time, product *int64) financialSalesResult {
	t.Helper()
	raw, err := db.SalesReportDetails(t.Context(), actor, SalesReportDetailFilter{Start: start, End: end, Product: product, Limit: 100}, end)
	if err != nil {
		t.Fatal(err)
	}
	var result financialSalesResult
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPurchaseFinancialReadsUseLinePricingSnapshots(t *testing.T) {
	db := isolatedTestStore(t)
	actor, customer := topUpFixture(t, db)
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for index, tc := range []struct {
		name                      string
		gross, group, promo, want []int64
		paid                      int64
	}{
		// Product A alone receives the coupon; B keeps its complete net amount.
		{"limited-promo", []int64{101, 200, 10}, []int64{51, 0, 10}, []int64{25, 0, 0}, []int64{25, 200, 0}, 225},
		{"invoice-adjustment", []int64{101, 200, 10}, []int64{51, 0, 10}, []int64{25, 0, 0}, []int64{24, 200, 0}, 224},
		{"invoice-increase", []int64{100, 200}, []int64{20, 0}, []int64{0, 0}, []int64{85, 215}, 300},
		{"legacy-proportion", []int64{101, 200, 0}, nil, nil, []int64{75, 150, 0}, 225},
		{"free-order", []int64{1, 2}, []int64{1, 2}, []int64{0, 0}, []int64{0, 0}, 0},
		{"zero-net-and-penny", []int64{1, 1, 1}, []int64{1, 0, 0}, []int64{0, 0, 0}, []int64{0, 0, 1}, 1},
		// numeric intermediates preserve cents beyond bigint multiplication range.
		{"bigint-legacy", []int64{4000000000000000000, 4000000000000000000}, nil, nil, []int64{3999999999999999999, 4000000000000000000}, 7999999999999999999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2024, time.Month(index+1), 1, 0, 0, 0, 0, location)
			end := start.AddDate(0, 1, 0)
			f := financialPurchase(t, db, customer, tc.name, start.Add(time.Hour), tc.gross, tc.group, tc.promo, tc.paid)
			products, metadata, err := db.ProductIncomeCategoryData(t.Context(), start, end, actor)
			if err != nil {
				t.Fatal(err)
			}
			if len(products) != len(tc.want) || len(metadata) != len(tc.want) {
				t.Fatalf("report rows=%d metadata=%d want=%d", len(products), len(metadata), len(tc.want))
			}
			byProduct := map[int64]int64{}
			for _, p := range products {
				byProduct[p.ProductID] = p.NewOrderAmountCents
				if p.NewOrderCount != 1 || p.NewQuantity != 1 {
					t.Fatalf("quantity changed: %+v", p)
				}
			}
			sales := financialSalesRead(t, db, actor, start, end, nil)
			if len(sales.Items) != 1 || sales.Items[0].InvoiceID != f.invoice || sales.Items[0].Amount != tc.paid {
				t.Fatalf("sales receipts: %+v", sales)
			}
			salesByProduct := map[int64]int64{}
			for _, line := range sales.Items[0].Lines {
				salesByProduct[line.ProductID] = line.Amount
			}
			tx, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			affiliate, err := affiliateInvoiceItems(t.Context(), tx, f.invoice)
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(affiliate) != len(tc.want) {
				t.Fatalf("affiliate lines=%d want=%d", len(affiliate), len(tc.want))
			}
			var allocated int64
			for n, product := range f.products {
				want := tc.want[n]
				if byProduct[product] != want || salesByProduct[product] != want || affiliate[n].ProductID != product || affiliate[n].BasisCents != want {
					t.Fatalf("product %d: income=%d sales=%d affiliate=%+v want=%d", product, byProduct[product], salesByProduct[product], affiliate[n], want)
				}
				allocated += want
				// Filtering a single product cannot redistribute the invoice discount.
				filtered := financialSalesRead(t, db, actor, start, end, &product)
				if len(filtered.Items) != 1 || len(filtered.Items[0].Lines) != 1 || filtered.Items[0].Amount != want {
					t.Fatalf("filter reallocates product %d: %+v want=%d", product, filtered, want)
				}
			}
			if allocated != tc.paid {
				t.Fatalf("lost cents: %d want=%d", allocated, tc.paid)
			}
		})
	}
	// Reads still obey customer scope after moving out of the HTTP layer.
	var role int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO admin_roles(name,permissions) VALUES('Financial scope reader',ARRAY['reports.view']) RETURNING id`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET role='admin',admin_role_id=$2,customer_scope='owned',customer_ownerless=false WHERE id=$1`, actor, role); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, location)
	rows, err := db.ProductIncomeRows(t.Context(), start, start.AddDate(1, 0, 0), actor)
	if err != nil || len(rows) != 0 {
		t.Fatalf("product scope: %+v %v", rows, err)
	}
	if hidden := financialSalesRead(t, db, actor, start, start.AddDate(1, 0, 0), nil); hidden.Total != 0 {
		t.Fatalf("sales scope: %+v", hidden)
	}
}

func TestPurchaseFinancialSalesCreditCumulativeNetAndLegacy(t *testing.T) {
	db := isolatedTestStore(t)
	actor, customer := topUpFixture(t, db)
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for index, snapshot := range []bool{true, false} {
		name := fmt.Sprintf("credit-line-pricing-%t", snapshot)
		t.Run(name, func(t *testing.T) {
			start := time.Date(2024, time.Month(1+index*3), 1, 0, 0, 0, 0, location)
			var group, promo []int64
			if snapshot {
				group = []int64{0, 1}
				promo = []int64{0, 0}
			}
			f := financialPurchase(t, db, customer, name, start.Add(time.Hour), []int64{1, 3}, group, promo, 3)
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,status,expected_version,idempotency_key) VALUES($1,$2,3,0,'repaid',1,$3)`, f.invoice, customer, name); err != nil {
				t.Fatal(err)
			}
			for n, amount := range []int64{1, 2} {
				key := fmt.Sprintf("%s-receipt-%d", name, n)
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after)
VALUES($1,$2,$3,$4,$5,$6,$5,$7,$8,$9)`, f.invoice, customer, actor, amount, key, start.AddDate(0, n, 1), n+1, n+2, 2*(1-n)); err != nil {
					t.Fatal(err)
				}
			}
			first := financialSalesRead(t, db, actor, start, start.AddDate(0, 1, 0), nil)
			second := financialSalesRead(t, db, actor, start.AddDate(0, 1, 0), start.AddDate(0, 2, 0), nil)
			all := financialSalesRead(t, db, actor, start, start.AddDate(0, 2, 0), nil)
			for _, result := range []financialSalesResult{first, second, all} {
				if len(result.Items) != 1 || len(result.Items[0].Lines) != 2 {
					t.Fatalf("credit rows: %+v", result)
				}
			}
			if first.Items[0].Amount != 1 || second.Items[0].Amount != 2 || all.Items[0].Amount != 3 {
				t.Fatalf("credit receipts: first=%+v second=%+v all=%+v", first, second, all)
			}
			wantFirst, wantSecond, wantAll := []int64{0, 1}, []int64{1, 1}, []int64{1, 2}
			if !snapshot {
				// Historical orders retain the original independent receipt/gross basis.
				wantFirst, wantSecond, wantAll = []int64{0, 1}, []int64{0, 2}, []int64{0, 3}
			}
			for n, product := range f.products {
				for i, result := range []financialSalesResult{first, second, all} {
					want := [][]int64{wantFirst, wantSecond, wantAll}[i][n]
					found := false
					for _, line := range result.Items[0].Lines {
						if line.ProductID == product {
							found = true
							if line.Amount != want {
								t.Fatalf("snapshot=%t period=%d product=%d amount=%d want=%d", snapshot, i, product, line.Amount, want)
							}
						}
					}
					if !found {
						t.Fatalf("missing product %d", product)
					}
				}
			}
		})
	}
}
