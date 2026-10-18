package httpapi

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func reportCSVRecords(t *testing.T, content []byte) [][]string {
	t.Helper()
	bom := []byte{0xef, 0xbb, 0xbf}
	if !bytes.HasPrefix(content, bom) || !utf8.Valid(content) {
		t.Fatalf("CSV must contain a UTF-8 BOM and valid UTF-8: %q", content)
	}
	records, err := csv.NewReader(bytes.NewReader(content[len(bom):])).ReadAll()
	if err != nil {
		t.Fatalf("invalid CSV: %v", err)
	}
	return records
}

func TestReportCSVReportSchemasAndIntegerPrecision(t *testing.T) {
	const amount int64 = 9007199254740993
	for _, tc := range []struct {
		name string
		data any
		want [][]string
	}{
		{"income", map[string]any{
			"months": []incomeMonth{{"2024-02", amount, 1}, {"2024-01", 1, 1}},
			"total":  map[string]any{"income_cents": amount + 1, "invoice_count": 1},
		}, [][]string{{"月份", "收入（分）", "账单数"}, {"2024-02", "9007199254740993", "1"}, {"2024-01", "1", "1"}, {"合计", "9007199254740994", "1"}}},
		{"daily", map[string]any{
			"ticket_metrics_available": false,
			"days":                     []dailyClientReport{{Date: "2024-02-01", NewClients: 1, NewOrders: 2, PaidOrders: 3, CancellationRequests: 4}},
		}, [][]string{{"日期", "新客户", "新订单", "已支付订单", "新工单", "工单回复", "取消申请"}, {"2024-02-01", "1", "2", "3", "无权限", "无权限", "4"}}},
		{"ranking", []customerRanking{{42, "测试,\"客户\"\n第二行", amount, amount + 3, -3}},
			[][]string{{"客户ID", "客户", "收入（分）", "支出（分）", "净额（分）"}, {"42", "测试,\"客户\"\n第二行", "9007199254740993", "9007199254740996", "-3"}}},
		{"products", map[string]any{"products": []productIncome{{ProductID: 17, Name: "商品", NewOrderAmountCents: amount, NewOrderCount: 2, RenewalAmountCents: 7, RenewalOrderCount: 3, TotalAmountCents: amount + 7}}},
			[][]string{{"产品ID", "产品", "新购收入（分）", "新购订单数", "续费收入（分）", "续费账单数", "合计（分）"}, {"17", "商品", "9007199254740993", "2", "7", "3", "9007199254741000"}}},
		{"sales", map[string]any{"items": []salesReportItem{{9, "=HYPERLINK(\"https://example.test\")", 2, amount}}},
			[][]string{{"销售ID", "销售", "订单数", "收入（分）"}, {"9", "'=HYPERLINK(\"https://example.test\")", "2", "9007199254740993"}}},
		{"tickets", map[string]any{"items": []ticketReportItem{{"2024-02-01", 5, 2}}},
			[][]string{{"日期", "工单创建量", "当前待处理量"}, {"2024-02-01", "5", "2"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, err := encodeReportCSV(tc.name, tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if got := reportCSVRecords(t, content); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("export fields/values changed:\n got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestReportCSVEmptyAndMalformedReports(t *testing.T) {
	for _, tc := range []struct {
		name string
		data any
		rows int
	}{
		{"income", map[string]any{"months": []incomeMonth{}, "total": map[string]any{"income_cents": 0, "invoice_count": 0}}, 2},
		{"daily", map[string]any{"days": []dailyClientReport{}}, 1},
		{"ranking", []customerRanking{}, 1},
		{"products", map[string]any{"products": []productIncome{}}, 1},
		{"sales", map[string]any{"items": []salesReportItem{}}, 1},
		{"tickets", map[string]any{"items": []ticketReportItem{}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, err := encodeReportCSV(tc.name, tc.data)
			if err != nil {
				t.Fatal(err)
			}
			records := reportCSVRecords(t, content)
			if len(records) != tc.rows || len(records[0]) == 0 {
				t.Fatalf("empty export must retain headers and an income total: %#v", records)
			}
			if tc.name == "income" && !reflect.DeepEqual(records[1], []string{"合计", "0", "0"}) {
				t.Fatalf("empty income total: %#v", records)
			}
		})
	}
	for _, tc := range []struct {
		name string
		data any
	}{
		{"unknown", []any{}},
		{"income", []any{}},
		{"daily", map[string]any{}},
		{"ranking", map[string]any{}},
		{"ranking", []any{"invalid row"}},
		{"ranking", []any{make(chan int)}},
	} {
		if _, err := encodeReportCSV(tc.name, tc.data); err == nil {
			t.Fatalf("malformed %s export was accepted: %#v", tc.name, tc.data)
		}
	}
}

func TestReportCSVFormulaInjectionAndNumericCells(t *testing.T) {
	for _, input := range []string{"=1+1", "+1+1", "-1+1", "@SUM(1)", "  =1", " \t+1", "\ttext", "\rtext", "\ntext", " \r\n@SUM(1)"} {
		if got := reportCSVCell(input); got != "'"+input {
			t.Errorf("spreadsheet formula/control prefix must be quoted: input=%q got=%q", input, got)
		}
	}
	for _, input := range []string{"", "客户甲", "00123", "a=b", "'quoted", "two,columns"} {
		if got := reportCSVCell(input); got != input {
			t.Errorf("plain text changed: input=%q got=%q", input, got)
		}
	}
	for _, tc := range []struct {
		input any
		want  string
	}{{nil, ""}, {json.Number("9007199254740993"), "9007199254740993"}, {json.Number("-9007199254740993"), "-9007199254740993"}, {int64(-42), "-42"}} {
		if got := reportCSVCell(tc.input); got != tc.want {
			t.Errorf("numeric cells must remain exact numbers: got=%q want=%q", got, tc.want)
		}
	}
}

func TestWriteReportFormatsAndDownloadHeaders(t *testing.T) {
	payload := []customerRanking{{11, "客户", 9007199254740993, 0, 9007199254740993}}
	for _, format := range []string{"", "json", "csv", "xml"} {
		t.Run("format="+format, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeReport(response, httptest.NewRequest("GET", "/?format="+format, nil), "ranking", payload)
			if format == "xml" {
				expect(t, response, 400)
				if errorData(t, response)["code"] != "invalid_report_format" || response.Header().Get("Content-Disposition") != "" {
					t.Fatalf("invalid format was advertised as a download: %s", response.Body.String())
				}
				return
			}
			expect(t, response, 200)
			if format == "csv" {
				if response.Header().Get("Content-Type") != "text/csv; charset=utf-8" || response.Header().Get("Content-Disposition") != `attachment; filename="report-ranking.csv"` || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("unsafe/incomplete download headers: %#v", response.Header())
				}
				records := reportCSVRecords(t, response.Body.Bytes())
				if len(records) != 2 || records[1][2] != "9007199254740993" {
					t.Fatalf("CSV amount lost precision: %#v", records)
				}
				return
			}
			if response.Header().Get("Content-Type") != "application/json; charset=utf-8" || response.Header().Get("Content-Disposition") != "" {
				t.Fatalf("JSON response headers changed: %#v", response.Header())
			}
			var envelope struct{ Data []customerRanking }
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || !reflect.DeepEqual(envelope.Data, payload) {
				t.Fatalf("JSON result changed: %s; err=%v", response.Body.String(), err)
			}
		})
	}
	response := httptest.NewRecorder()
	writeReport(response, httptest.NewRequest("GET", "/?format=csv", nil), "unknown", payload)
	expect(t, response, 500)
	if errorData(t, response)["code"] != "report_export_failed" || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("failed export must return a JSON error: %s", response.Body.String())
	}
}

func TestReportCSVEndpointsEnforcePermissionsAndScope(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "csv-scope-staff", "admin")
	allowed, customer := user(t, db, h, "csv-scope-owned", "customer")
	denied, _ := user(t, db, h, "csv-scope-foreign", "customer")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE users SET customer_scope='owned',customer_ownerless=false,ticket_scope='departments',display_name='+VISIBLE-SALES' WHERE id=$1", staffID)
	exec("UPDATE users SET sales_owner_id=$1,display_name='=VISIBLE-CUSTOMER' WHERE id=$2", staffID, allowed)
	exec("UPDATE users SET created_at='2024-02-01T12:00:00+08' WHERE id=ANY($1)", []int64{allowed, denied})
	var visible, hidden int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('CSV visible','csv-visible') RETURNING id").Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('CSV hidden','csv-hidden') RETURNING id").Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, visible)
	var visibleProduct int64
	for n, fixture := range []struct{ uid, amount, department int64 }{{allowed, 100, visible}, {denied, 999, hidden}} {
		pid := product(t, db, 10)
		name := "foreign-product-secret"
		if n == 0 {
			visibleProduct, name = pid, "=VISIBLE-PRODUCT"
		}
		var order, ticket int64
		key := fmt.Sprintf("csv-scope-order-%d", n)
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash,created_at)
VALUES($1,$2,'paid',$3,$1,'fixture','2024-02-01T12:00:00+08') RETURNING id`, key, fixture.uid, fixture.amount).Scan(&order); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,$3,'monthly',1,$4,0,$4)`, order, pid, name, fixture.amount)
		exec(`INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at)
VALUES($1,$2,$3,'paid',$4,'2024-02-01T12:00:00+08')`, key, fixture.uid, order, fixture.amount)
		exec(`INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after) VALUES($1,'adjustment',$2,$2)`, fixture.uid, fixture.amount)
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,subject,body,department_id,created_at)
VALUES($1,$2,'CSV test','test',$3,'2024-02-01T12:00:00+08') RETURNING id`, key, fixture.uid, fixture.department).Scan(&ticket); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO ticket_replies(ticket_id,author_id,body,created_at) VALUES($1,$2,'test','2024-02-01T12:00:00+08')`, ticket, fixture.uid)
	}
	reports := []struct {
		path, name string
		rows       int
		first      []string
	}{
		{"income", "income", 3, []string{"2024-02", "100", "1"}},
		{"new-clients", "daily", 30, []string{"2024-02-01", "1", "1", "1", "1", "1", "0"}},
		{"customer-ranking", "ranking", 2, []string{fmt.Sprint(allowed), "'=VISIBLE-CUSTOMER", "100", "0", "100"}},
		{"product-income", "products", 2, []string{fmt.Sprint(visibleProduct), "'=VISIBLE-PRODUCT", "100", "1", "0", "0", "100"}},
		{"sales", "sales", 2, []string{fmt.Sprint(staffID), "'+VISIBLE-SALES", "1", "100"}},
		{"tickets", "tickets", 30, []string{"2024-02-01", "1", "1"}},
	}
	for _, report := range reports {
		t.Run(report.path, func(t *testing.T) {
			path := "/api/v1/admin/reports/" + report.path + "?year=2024&month=2&format=csv"
			expect(t, request(h, "GET", path, nil, "", ""), 401)
			expect(t, request(h, "GET", path, nil, customer, ""), 403)
			response := request(h, "GET", path, nil, staff, "")
			expect(t, response, 200)
			if response.Header().Get("Content-Disposition") != fmt.Sprintf(`attachment; filename="report-%s.csv"`, report.name) {
				t.Fatalf("missing report download headers: %#v", response.Header())
			}
			records := reportCSVRecords(t, response.Body.Bytes())
			if len(records) != report.rows || !reflect.DeepEqual(records[1], report.first) || strings.Contains(response.Body.String(), "foreign-product-secret") {
				t.Fatalf("export did not preserve JSON report scope/values: %#v", records)
			}
			if report.name == "income" && !reflect.DeepEqual(records[2], []string{"合计", "100", "1"}) {
				t.Fatalf("export total ignored scope: %#v", records)
			}
		})
	}
	// The same authenticated session must lose ticket data on its next export.
	exec("UPDATE admin_roles SET permissions=array_remove(permissions,'tickets.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	response := request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2&format=csv", nil, staff, "")
	expect(t, response, 403)
	if errorData(t, response)["code"] != "permission_denied" || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("ticket export bypassed revoked capability: %s", response.Body.String())
	}
	response = request(h, "GET", "/api/v1/admin/reports/new-clients?year=2024&month=2&format=csv", nil, staff, "")
	expect(t, response, 200)
	for _, row := range reportCSVRecords(t, response.Body.Bytes())[1:] {
		if row[4] != "无权限" || row[5] != "无权限" {
			t.Fatalf("daily export disguised unavailable ticket metrics as zero: %#v", row)
		}
	}
	exec("UPDATE admin_roles SET permissions=array_remove(permissions,'reports.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	for _, report := range reports {
		response := request(h, "GET", "/api/v1/admin/reports/"+report.path+"?year=2024&month=2&format=csv", nil, staff, "")
		expect(t, response, 403)
		if response.Header().Get("Content-Disposition") != "" {
			t.Fatalf("revoked report access still offered a download: %s", report.path)
		}
	}
}
