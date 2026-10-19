package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

// This fixture models independent resources and invoices so two customers'
// selections cannot accidentally pass by sharing a single hard-coded host.
type supplierConfigurableFixture struct {
	db                                 *store.Store
	h                                  http.Handler
	root, customer                     string
	uid, supplierID, pid               int64
	configuration                      store.SupplierCatalogConfiguration
	remote                             *httptest.Server
	creates, pays, renews, renewalPays atomic.Int64
	sourcePercent                      atomic.Bool
	calendarCycles                     atomic.Bool
	volumePricing                      atomic.Bool
	mu                                 sync.Mutex
	pending                            url.Values
	resources                          map[int64]*supplierConfigurableResource
	invoices                           map[int64]*supplierConfigurableInvoice
	quoteExtra                         int64
}

type supplierConfigurableResource struct {
	id, pid, localID, recurring, setup int64
	cycle, choice, quantity, password  string
	due                                time.Time
}

type supplierConfigurableInvoice struct {
	id, hostID, amount int64
	renewal, paid      bool
}

func supplierConfigurableSetup(t *testing.T) *supplierConfigurableFixture {
	t.Helper()
	return supplierConfigurableSetupWithImport(t, true)
}

func supplierConfigurableSetupWithImport(t *testing.T, doImport bool) *supplierConfigurableFixture {
	t.Helper()
	f := &supplierConfigurableFixture{resources: map[int64]*supplierConfigurableResource{}, invoices: map[int64]*supplierConfigurableInvoice{}}
	f.db, f.h = testServer(t)
	f.db.PurchaseFieldEncryptionKey = bytes.Repeat([]byte{4}, 32)
	f.remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/zjmf_api_login" && r.Header.Get("Authorization") != "Bearer configurable-private-jwt" {
			t.Error("missing configurable supplier authorization")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		respond := func(data any) { json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": data}) }
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"configurable-private-jwt"}`)
		case "/cart/all":
			respond(map[string]any{"products": []map[string]any{{"id": 3, "name": "香港", "products": []map[string]any{{"id": 7, "name": "Configurable VM 7", "stock_control": 1, "qty": 50}, {"id": 8, "name": "Configurable VM 8", "stock_control": 1, "qty": 50}}}}, "currency": "CNY"})
		case "/cart/get_product_config":
			pid, _ := strconv.ParseInt(r.URL.Query().Get("pid"), 10, 64)
			detail := supplierConfigurableDetail(pid)
			if f.calendarCycles.Load() {
				extend := func(rows []map[string]any) {
					row := rows[0]
					for _, cycle := range supplier.BillingCycles() {
						if cycle == "once" || cycle == "monthly" || cycle == "annually" {
							continue
						}
						n, _ := strconv.ParseFloat(row["monthly"].(string), 64)
						row[cycle] = fmt.Sprintf("%.2f", n*float64(supplier.BillingMonths(cycle)))
						setupKey := map[string]string{"semiannually": "ssetupfee", "biennially": "bsetupfee", "triennially": "tsetupfee", "quarterly": "qsetupfee", "fourly": "foursetupfee", "fively": "fivesetupfee", "sixly": "sixsetupfee", "sevenly": "sevensetupfee", "eightly": "eightsetupfee", "ninely": "ninesetupfee", "tenly": "tensetupfee"}[cycle]
						row[setupKey] = row["msetupfee"]
					}
				}
				extend(detail["product_pricings"].([]map[string]any))
				for _, g := range detail["config_groups"].([]map[string]any) {
					for _, o := range g["options"].([]map[string]any) {
						o["linkage_level"] = "0-192"
						for _, c := range o["sub"].([]map[string]any) {
							extend(c["pricings"].([]map[string]any))
						}
					}
				}
			}
			if f.volumePricing.Load() {
				groups := detail["config_groups"].([]map[string]any)
				option := groups[0]["options"].([]map[string]any)[1]
				option["option_type"], option["qty_minimum"], option["qty_maximum"] = 4, 1, 4
				sub := option["sub"].([]map[string]any)[0]
				sub["qty_minimum"], sub["qty_maximum"] = 1, 1
				other := map[string]any{"id": 604, "option_name": "GB", "qty_minimum": 2, "qty_maximum": 4, "pricings": []map[string]any{{"code": "CNY", "monthly": "1.00", "annually": "10.00", "msetupfee": "0.80", "asetupfee": "0.80"}}}
				option["sub"] = []map[string]any{sub, other}
			}
			if f.sourcePercent.Load() {
				product := detail["products"].(map[string]any)
				product["api_type"], product["upstream_pid"], product["upstream_price_type"], product["upstream_price_value"] = "zjmf_api", 99, "percent", "125.00"
			}
			respond(detail)
		case "/user_info":
			fmt.Fprint(w, `{"status":200,"user":{"currency":1}}`)
		case "/cart/clear":
			localID, _ := strconv.ParseInt(r.PostFormValue("downstream_id"), 10, 64)
			for _, resource := range f.resources {
				if resource.localID == localID {
					for _, invoice := range f.invoices {
						if invoice.hostID == resource.id && !invoice.renewal {
							respond(map[string]any{"hostid": []int64{resource.id}, "invoiceid": invoice.id})
							return
						}
					}
				}
			}
			f.pending = nil
			respond(map[string]any{})
		case "/cart/add_to_shop":
			r.ParseForm()
			f.pending = r.PostForm
			if len(r.PostFormValue("downstream_token")) != 32 || r.PostFormValue("customfield[702]") == "" {
				t.Error("missing downstream identity or required purchase field")
			}
			respond(map[string]any{})
		case "/cart/settle":
			r.ParseForm()
			for _, key := range []string{"pid", "billingcycle", "downstream_id"} {
				if r.PostFormValue("cart_data["+key+"]") != f.pending.Get(key) {
					t.Errorf("settlement changed %s", key)
				}
			}
			for _, key := range []string{"501", "502"} {
				if r.PostFormValue("cart_data[configoptions]["+key+"]") != f.pending.Get("configoption["+key+"]") {
					t.Errorf("settlement changed option %s", key)
				}
			}
			if r.PostFormValue("cart_data[customfield][702]") != f.pending.Get("customfield[702]") {
				t.Error("settlement changed private field")
			}
			cycle, choice := f.pending.Get("billingcycle"), f.pending.Get("configoption[501]")
			qty, _ := strconv.ParseInt(f.pending.Get("configoption[502]"), 10, 64)
			recurring, setup := int64(1000), int64(100)
			choiceCost, choiceSetup, quantityCost := int64(100), int64(20), int64(50)
			if choice == "602" {
				choiceCost, choiceSetup = 200, 30
			}
			if f.volumePricing.Load() && qty >= 2 {
				quantityCost = 100
			}
			factor := int64(supplier.BillingMonths(cycle))
			if cycle == "annually" {
				factor = 10
			}
			recurring *= factor
			choiceCost *= factor
			quantityCost *= factor
			recurring += choiceCost + quantityCost*qty
			setup += choiceSetup
			if qty > 0 {
				if f.volumePricing.Load() && qty >= 2 {
					setup += 80
				} else {
					setup += 40
				}
			}
			if f.sourcePercent.Load() {
				// Exercise an invoice that rounds each configured total upward.
				recurring, setup = (recurring*125+99)/100, (setup*125+99)/100
			}
			hostID, invoiceID := int64(100+len(f.resources)), int64(1000+len(f.invoices))
			pid, _ := strconv.ParseInt(f.pending.Get("pid"), 10, 64)
			localID, _ := strconv.ParseInt(f.pending.Get("downstream_id"), 10, 64)
			f.resources[hostID] = &supplierConfigurableResource{id: hostID, pid: pid, localID: localID, recurring: recurring, setup: setup, cycle: cycle, choice: choice, quantity: strconv.FormatInt(qty, 10), password: f.pending.Get("customfield[702]"), due: time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, supplier.BillingMonths(cycle), 0).Truncate(time.Second)}
			f.invoices[invoiceID] = &supplierConfigurableInvoice{id: invoiceID, hostID: hostID, amount: recurring + setup}
			f.creates.Add(1)
			respond(map[string]any{"hostid": []int64{hostID}, "invoiceid": invoiceID})
		case "/get_invoices_detail":
			id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			invoice := f.invoices[id]
			if invoice == nil {
				t.Error("unknown configurable invoice")
				w.WriteHeader(404)
				return
			}
			status, total, kind := "Unpaid", supplierConfigurableMoney(invoice.amount), "host"
			if invoice.paid {
				status, total = "Paid", "0.00"
			}
			if invoice.renewal {
				kind = "renew"
			}
			respond(map[string]any{"detail": map[string]any{"id": id, "subtotal": supplierConfigurableMoney(invoice.amount), "total": total, "credit": "0.00", "status": status}, "currency": map[string]any{"code": "CNY"}, "invoice_items": []map[string]any{{"type": kind, "rel_id": invoice.hostID, "amount": supplierConfigurableMoney(invoice.amount)}}})
		case "/apply_credit":
			id, _ := strconv.ParseInt(r.PostFormValue("invoiceid"), 10, 64)
			invoice := f.invoices[id]
			if invoice == nil || r.PostFormValue("enough") != "1" || r.PostFormValue("use_credit") != "1" {
				t.Error("unsafe configurable payment")
				w.WriteHeader(400)
				return
			}
			if !invoice.paid {
				if invoice.renewal {
					resource := f.resources[invoice.hostID]
					months := supplier.BillingMonths(resource.cycle)
					resource.due = resource.due.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, months, 0)
					f.renewalPays.Add(1)
				} else {
					f.pays.Add(1)
				}
			}
			invoice.paid = true
			fmt.Fprint(w, `{"status":1001}`)
		case "/host/header":
			id, _ := strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
			resource := f.resources[id]
			if resource == nil {
				t.Error("unknown configurable host")
				w.WriteHeader(404)
				return
			}
			respond(map[string]any{"host_data": map[string]any{"id": id, "productid": resource.pid, "domainstatus": "Active", "domain": fmt.Sprintf("configurable-%d.example", id), "nextduedate": resource.due.Unix()}})
		case "/host/renewpage", "/host/renew":
			var id int64
			var cycle string
			if r.Method == http.MethodGet {
				id, _ = strconv.ParseInt(r.URL.Query().Get("hostid"), 10, 64)
				cycle = r.URL.Query().Get("billingcycles")
			} else {
				id, _ = strconv.ParseInt(r.PostFormValue("hostid"), 10, 64)
				cycle = r.PostFormValue("billingcycles")
			}
			resource := f.resources[id]
			if resource == nil || resource.cycle != cycle {
				t.Error("renewal changed purchased cycle")
				w.WriteHeader(400)
				return
			}
			if r.URL.Path == "/host/renewpage" {
				respond(map[string]any{"host": map[string]any{"productid": resource.pid, "billingcycle": cycle, "amount": supplierConfigurableMoney(resource.recurring + f.quoteExtra), "nextduedate": resource.due.Unix()}, "currency": map[string]any{"code": "CNY"}})
			} else {
				invoiceID := int64(1000 + len(f.invoices))
				f.invoices[invoiceID] = &supplierConfigurableInvoice{id: invoiceID, hostID: id, amount: resource.recurring + f.quoteExtra, renewal: true}
				f.renews.Add(1)
				respond(map[string]any{"invoice_id": invoiceID})
			}
		default:
			t.Errorf("unexpected configurable endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.remote.Close)
	_, f.root = user(t, f.db, f.h, "configurable-catalog-root", "super_admin")
	f.uid, f.customer = user(t, f.db, f.h, "configurable-catalog-customer", "customer")
	w := request(f.h, "POST", "/api/v1/admin/suppliers", store.SupplierInput{Name: "Configurable upstream", Provider: supplier.ProviderZJMF, BaseURL: f.remote.URL, Username: "configurable-account", Password: "configurable-private-password", Active: enabled()}, f.root, "")
	expect(t, w, 201)
	f.supplierID = int64(data(t, w)["id"].(float64))
	if doImport {
		f.importProduct(t)
	}
	return f
}

func (f *supplierConfigurableFixture) importProduct(t *testing.T) {
	t.Helper()
	in := store.SupplierCatalogImportInput{ExpectedSupplierVersion: 1, PriceMultiplierBPS: 15000, Active: true, Items: []store.SupplierCatalogImportItem{{UpstreamProductID: 7}}}
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/suppliers/%d/import-preview", f.supplierID), in, f.root, "")
	expect(t, w, 200)
	var preview struct {
		Data store.SupplierCatalogImportPreview `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	in.Items[0].ExpectedUpstreamRevision = preview.Data.Items[0].Product.Revision
	w = request(f.h, "POST", fmt.Sprintf("/api/v1/admin/suppliers/%d/import-batch", f.supplierID), in, f.root, "configurable-import")
	expect(t, w, 201)
	f.pid = supplierCatalogImportResults(t, w).Items[0].ProductID
	f.loadConfiguration(t)
}

func supplierConfigurableMoney(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func supplierConfigurableDetail(pid int64) map[string]any {
	pricing := func(month, year, setup string) []map[string]any {
		return []map[string]any{{"code": "CNY", "monthly": month, "annually": year, "msetupfee": setup, "asetupfee": setup}}
	}
	return map[string]any{
		"products":         map[string]any{"id": pid, "name": fmt.Sprintf("Configurable VM %d", pid), "description": "Imported configurable VM", "stock_control": 1, "qty": 50, "location_version": 1, "pay_type": map[string]any{"pay_type": "recurring"}},
		"product_pricings": pricing("10.00", "100.00", "1.00"),
		"config_groups": []map[string]any{{"options": []map[string]any{
			{"id": 501, "option_name": "地域", "option_type": 1, "sub": []map[string]any{{"id": 601, "option_name": "香港", "pricings": pricing("1.00", "10.00", "0.20")}, {"id": 602, "option_name": "日本", "pricings": pricing("2.00", "20.00", "0.30")}}},
			{"id": 502, "option_name": "数据盘", "option_type": 14, "qty_minimum": 0, "qty_maximum": 10, "qty_stage": 1, "sub": []map[string]any{{"id": 603, "option_name": "GB", "qty_minimum": 0, "qty_maximum": 10, "pricings": pricing("0.50", "5.00", "0.40")}}},
		}}},
		"customfields": []map[string]any{{"id": 702, "fieldname": "实例密钥", "fieldtype": "password", "required": 1}}, "advanced": []any{},
	}
}

func (f *supplierConfigurableFixture) loadConfiguration(t *testing.T) {
	t.Helper()
	var raw []byte
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT ci.configuration FROM supplier_catalog_imports ci JOIN supplier_product_bindings b ON b.id=ci.binding_id WHERE b.product_id=$1`, f.pid).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &f.configuration); err != nil {
		t.Fatal(err)
	}
}

func (f *supplierConfigurableFixture) selection(choice int64, quantity int64) ([]int64, map[int64]int64, map[string]string) {
	ids := []int64{}
	quantities := map[int64]int64{}
	fields := map[string]string{}
	for id, mapping := range f.configuration.Options {
		if mapping.OptionID == 501 && mapping.ChoiceID == choice {
			ids = append(ids, id)
		}
		if mapping.OptionID == 502 {
			ids = append(ids, id)
			quantities[id] = quantity
		}
	}
	for key, id := range f.configuration.Fields {
		if id == 702 {
			fields[key] = "private-customer-purchase-value"
		}
	}
	return ids, quantities, fields
}

func (f *supplierConfigurableFixture) purchase(t *testing.T, cycle string, choice, quantity int64, key string) (int64, int64) {
	t.Helper()
	ids, quantities, fields := f.selection(choice, quantity)
	version, _ := cart(t, f.h, f.customer)
	w := request(f.h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": f.pid, "quantity": 1, "billing_cycle": cycle, "option_ids": ids, "option_quantities": quantities, "purchase_fields": fields}, f.customer, "")
	expect(t, w, 200)
	version, item := cart(t, f.h, f.customer)
	w = request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, f.customer, key)
	expect(t, w, 201)
	orderID, invoiceID := int64(data(t, w)["order_id"].(float64)), int64(data(t, w)["invoice_id"].(float64))
	var sid int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", orderID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 100000}, f.root, key+"-credit"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, f.customer, key+"-pay"), 201)
	return sid, invoiceID
}

func (f *supplierConfigurableFixture) runNext(t *testing.T, action string) *store.ProvisionJob {
	t.Helper()
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatal("claim configurable job", err)
	}
	if job.Action != action {
		t.Fatalf("expected %s job, got %s", action, job.Action)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal("configurable job did not succeed", state, err)
	}
	return job
}

func TestSupplierConfigurableDeliverySelectionAndRenewal(t *testing.T) {
	f := supplierConfigurableSetup(t)
	monthly, monthlyInvoice := f.purchase(t, "monthly", 601, 0, "configurable-monthly")
	f.runNext(t, "create")
	annual, annualInvoice := f.purchase(t, "annually", 602, 2, "configurable-annual")
	f.runNext(t, "create")
	for _, tc := range []struct {
		sid, invoice, total, recurring, maximum, renewalMaximum int64
		cycle, choice, qty                                      string
	}{
		{monthly, monthlyInvoice, 1830, 1650, 1220, 1100, "monthly", "601", "0"},
		{annual, annualInvoice, 19755, 19500, 13170, 13000, "annually", "602", "2"},
	} {
		var amount, recurring, maximum, renewalMaximum, upstreamID int64
		var snapshot, cycle, status string
		err := f.db.Pool.QueryRow(t.Context(), `SELECT i.amount_cents,coalesce(s.recurring_price_cents,oi.price_cents),s.billing_cycle,s.status,s.upstream_id::bigint,s.delivery_snapshot::text,(s.delivery_snapshot->'selection'->>'maximum_cost_cents')::bigint,(s.delivery_snapshot->'selection'->>'renewal_maximum_cost_cents')::bigint FROM services s JOIN order_items oi ON oi.id=s.order_item_id JOIN invoices i ON i.id=$2 WHERE s.id=$1`, tc.sid, tc.invoice).Scan(&amount, &recurring, &cycle, &status, &upstreamID, &snapshot, &maximum, &renewalMaximum)
		if err != nil || amount != tc.total || recurring != tc.recurring || cycle != tc.cycle || status != "active" || maximum != tc.maximum || renewalMaximum != tc.renewalMaximum {
			t.Fatal("incorrect selected pricing/cycle/snapshot", amount, recurring, cycle, status, maximum, renewalMaximum, err)
		}
		if strings.Contains(snapshot, "private-customer-purchase-value") || !strings.Contains(snapshot, "encrypted") {
			t.Fatal("selected purchase fields not sealed")
		}
		f.mu.Lock()
		resource := f.resources[upstreamID]
		valid := resource != nil && resource.localID == tc.sid && resource.cycle == tc.cycle && resource.choice == tc.choice && resource.quantity == tc.qty && resource.password == "private-customer-purchase-value" && resource.recurring == tc.renewalMaximum && resource.recurring+resource.setup == tc.maximum
		f.mu.Unlock()
		if !valid {
			t.Fatal("upstream received another service's selected configuration")
		}
		for _, path := range []string{fmt.Sprintf("/api/v1/services/%d", tc.sid), fmt.Sprintf("/api/v1/admin/services/%d", tc.sid), fmt.Sprintf("/api/v1/invoices/%d", tc.invoice)} {
			cookie := f.customer
			if strings.Contains(path, "/admin/") {
				cookie = f.root
			}
			out := request(f.h, "GET", path, nil, cookie, "")
			expect(t, out, 200)
			if strings.Contains(out.Body.String(), "private-customer-purchase-value") || strings.Contains(out.Body.String(), "configurable-private-") {
				t.Fatal("selected secret leaked through projection")
			}
		}
	}
	if f.creates.Load() != 2 || f.pays.Load() != 2 {
		t.Fatal("incorrect upstream create/payment count")
	}

	// A selected annual cycle must survive manual renewal, despite the binding's
	// default monthly cycle and much lower default maximum supplier cost.
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", annual), map[string]any{}, f.customer, "configurable-annual-renew")
	expect(t, w, 201)
	renewalInvoice := int64(data(t, w)["invoice_id"].(float64))
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM invoices WHERE id=$1", renewalInvoice).Scan(&amount); err != nil || amount != 19500 {
		t.Fatal("annual renewal lost recurring selection", amount, err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", renewalInvoice), map[string]any{}, f.customer, "configurable-annual-renew-pay"), 201)
	f.runNext(t, "renew")
	if f.renews.Load() != 1 || f.renewalPays.Load() != 1 {
		t.Fatal("annual renewal not paid exactly once")
	}

	// Automatic renewal uses the same frozen selection and excludes setup fees.
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	f.mu.Lock()
	for _, resource := range f.resources {
		if resource.localID == monthly {
			resource.due = due
		}
	}
	f.mu.Unlock()
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=$2 WHERE id=$1", monthly, due); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 1 {
		t.Fatal("automatic selected renewal", n, err)
	}
	f.runNext(t, "renew")
	if f.renews.Load() != 2 || f.renewalPays.Load() != 2 {
		t.Fatal("automatic renewal was not paid once")
	}
	var autoAmount int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.amount_cents FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=$1 AND i.id<>$2 ORDER BY i.id DESC LIMIT 1`, monthly, monthlyInvoice).Scan(&autoAmount); err != nil || autoAmount != 1650 {
		t.Fatal("monthly auto renewal included setup or wrong cycle", autoAmount, err)
	}

	// Even one extra cent at the supplier exceeds this service's frozen cost.
	f.mu.Lock()
	f.quoteExtra = 1
	f.mu.Unlock()
	var invoicesBefore, invoicesAfter int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices").Scan(&invoicesBefore); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", annual), map[string]any{}, f.customer, "configurable-cost-increase"), 409)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices").Scan(&invoicesAfter); err != nil || invoicesAfter != invoicesBefore || f.renews.Load() != 2 || f.renewalPays.Load() != 2 {
		t.Fatal("cost ceiling failure created invoice or spent supplier balance", err)
	}
	t.Run("percent aggregate cost survives delivery", func(t *testing.T) {
		percent := supplierConfigurableSetupWithImport(t, false)
		percent.sourcePercent.Store(true)
		percent.importProduct(t)
		sid, invoiceID := percent.purchase(t, "monthly", 601, 1, "configurable-percent")
		percent.runNext(t, "create")
		var ceiling, localTotal int64
		if err := percent.db.Pool.QueryRow(t.Context(), `SELECT (s.delivery_snapshot->'selection'->>'maximum_cost_cents')::bigint,i.amount_cents FROM services s JOIN invoices i ON i.id=$2 WHERE s.id=$1`, sid, invoiceID).Scan(&ceiling, &localTotal); err != nil || ceiling != 1638 || localTotal != 2457 {
			t.Fatal("percent source cost and independent local component prices confused", ceiling, localTotal, err)
		}
		percent.mu.Lock()
		paidCost := int64(0)
		for _, invoice := range percent.invoices {
			if invoice.paid {
				paidCost += invoice.amount
			}
		}
		percent.mu.Unlock()
		if paidCost != 1638 || percent.creates.Load() != 1 || percent.pays.Load() != 1 {
			t.Fatal("percent cost ceiling rejected rounded upstream invoice", paidCost)
		}
	})
}

func TestSupplierConfigurableSelectionFailsClosed(t *testing.T) {
	t.Run("invalid cart selection", func(t *testing.T) {
		f := supplierConfigurableSetup(t)
		ids, quantities, fields := f.selection(601, 0)
		for _, kind := range []string{"missing password", "missing option", "invalid quantity", "unmapped option"} {
			version, _ := cart(t, f.h, f.customer)
			body := map[string]any{"version": version, "product_id": f.pid, "quantity": 1, "billing_cycle": "monthly", "option_ids": ids, "option_quantities": quantities, "purchase_fields": fields}
			switch kind {
			case "missing password":
				body["purchase_fields"] = map[string]string{}
			case "missing option":
				body["option_ids"] = []int64{}
			case "invalid quantity":
				q := map[int64]int64{}
				for id := range quantities {
					q[id] = 11
				}
				body["option_quantities"] = q
			case "unmapped option":
				body["option_ids"] = []int64{999999}
			}
			expect(t, request(f.h, "POST", "/api/v1/cart/items", body, f.customer, ""), 409)
		}
		if f.creates.Load() != 0 || f.pays.Load() != 0 {
			t.Fatal("invalid customer selection contacted supplier write endpoints")
		}
	})
	for _, kind := range []string{"missing selection", "swapped ciphertext", "wrong cycle", "raised cost ceiling"} {
		t.Run(kind, func(t *testing.T) {
			f := supplierConfigurableSetup(t)
			first, _ := f.purchase(t, "monthly", 601, 0, "configurable-corrupt-first")
			second, _ := f.purchase(t, "annually", 602, 2, "configurable-corrupt-second")
			var firstRaw, secondRaw []byte
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT delivery_snapshot FROM services WHERE id=$1", first).Scan(&firstRaw); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT delivery_snapshot FROM services WHERE id=$1", second).Scan(&secondRaw); err != nil {
				t.Fatal(err)
			}
			var snapshot, other map[string]any
			if err := json.Unmarshal(firstRaw, &snapshot); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(secondRaw, &other); err != nil {
				t.Fatal(err)
			}
			selected := snapshot["selection"].(map[string]any)
			switch kind {
			case "missing selection":
				delete(snapshot, "selection")
			case "swapped ciphertext":
				selected["encrypted"] = other["selection"].(map[string]any)["encrypted"]
			case "wrong cycle":
				selected["cycle"] = "annually"
			case "raised cost ceiling":
				selected["maximum_cost_cents"] = int64(99999)
			}
			corrupted, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if _, err = tx.Exec(t.Context(), "ALTER TABLE services DISABLE TRIGGER services_delivery_snapshot_immutable"); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), "UPDATE services SET delivery_snapshot=$2 WHERE id=$1", first, corrupted); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), "ALTER TABLE services ENABLE TRIGGER services_delivery_snapshot_immutable"); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil || job == nil {
				t.Fatal(err)
			}
			if job.ServiceID != first {
				t.Fatal("unexpected corruption fixture job")
			}
			if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal("invalid selection was not safely finalized", err)
			}
			var status string
			var lease *int64
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT j.status,a.running_job_id FROM provision_jobs j JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1`, job.ID, f.supplierID).Scan(&status, &lease); err != nil || status != "failed" || lease != nil || f.creates.Load() != 0 || f.pays.Load() != 0 {
				t.Fatal("invalid selection spent upstream funds or retained lease", status, lease, err)
			}
		})
	}
}

func TestSupplierCalendarImportCheckoutAndRenewal(t *testing.T) {
	f := supplierConfigurableSetupWithImport(t, false)
	f.calendarCycles.Store(true)
	f.importProduct(t)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 5000000}, f.root, "calendar-starting-balance"), 201)
	if len(f.configuration.Product.Prices) != 13 || len(f.configuration.Product.Options) != 2 {
		t.Fatal("calendar/root configuration was lost")
	}
	// All calendar cycles pass the real catalog and option-price constraints.
	var priceCount int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_prices WHERE product_id=$1", f.pid).Scan(&priceCount); err != nil || priceCount != 13 {
		t.Fatal(priceCount, err)
	}
	for _, cycle := range []string{"semiannually", "biennially", "tenly"} {
		t.Run(cycle, func(t *testing.T) {
			sid, _ := f.purchase(t, cycle, 602, 2, "calendar-"+cycle)
			f.runNext(t, "create")
			var oldDue time.Time
			var remoteID int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at,upstream_id::bigint FROM services WHERE id=$1", sid).Scan(&oldDue, &remoteID); err != nil {
				t.Fatal(err)
			}
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, f.customer, "calendar-renew-"+cycle)
			expect(t, w, 201)
			invoice := int64(data(t, w)["invoice_id"].(float64))
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "calendar-pay-"+cycle), 201)
			f.runNext(t, "renew")
			var after time.Time
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&after); err != nil || !after.Equal(oldDue.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, supplier.BillingMonths(cycle), 0)) {
				t.Fatal("wrong renewed calendar", oldDue, after, err)
			}
			// Automatic renewal must sync the full purchased calendar period.
			due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			f.mu.Lock()
			f.resources[remoteID].due = due
			f.mu.Unlock()
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=$2 WHERE id=$1", sid, due); err != nil {
				t.Fatal(err)
			}
			if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			f.runNext(t, "renew")
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&after); err != nil || !after.Equal(due.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, supplier.BillingMonths(cycle), 0)) {
				t.Fatal("wrong auto renewal calendar", after, err)
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/pricing", f.pid), map[string]any{"billing_cycle": cycle, "price_cents": 2000, "setup_fee_cents": 100, "active": true}, f.root, ""), 200)
		})
	}
	var native int64
	if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO products(name,slug,provision_mode,price_cents) VALUES('Native','calendar-native','manual',0) RETURNING id").Scan(&native); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/pricing", native), map[string]any{"billing_cycle": "semiannually", "price_cents": 100, "active": true}, f.root, ""), 400)
	_, err := f.db.SaveProduct(t.Context(), 3, 0, store.ProductInput{Name: "Native", Slug: "calendar-native-2", Active: enabled(), ProvisionMode: "manual", BillingCycle: "semiannually"})
	if err == nil {
		t.Fatal("native product unexpectedly accepted expanded supplier cycles")
	}
}

func TestSupplierVolumeImportCheckoutAndRenewal(t *testing.T) {
	f := supplierConfigurableSetupWithImport(t, false)
	f.volumePricing.Store(true)
	f.importProduct(t)
	for _, tc := range []struct{ qty, total, recurring int64 }{{1, 1965, 1725}, {2, 2250, 1950}, {4, 2550, 2250}} {
		t.Run(fmt.Sprint(tc.qty), func(t *testing.T) {
			sid, invoice := f.purchase(t, "monthly", 601, tc.qty, fmt.Sprintf("volume-%d", tc.qty))
			f.runNext(t, "create")
			var amount int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM invoices WHERE id=$1", invoice).Scan(&amount); err != nil || amount != tc.total {
				t.Fatal("volume differs from supplier formula", amount, tc, err)
			}
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, f.customer, fmt.Sprintf("volume-renew-%d", tc.qty))
			expect(t, w, 201)
			renewal := int64(data(t, w)["invoice_id"].(float64))
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM invoices WHERE id=$1", renewal).Scan(&amount); err != nil || amount != tc.recurring {
				t.Fatal("volume recurring differs", amount, tc, err)
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", renewal), map[string]any{}, f.customer, fmt.Sprintf("volume-pay-%d", tc.qty)), 201)
			f.runNext(t, "renew")
		})
	}
}
