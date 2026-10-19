package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestWriteReportJSONOnlyFormats(t *testing.T) {
	payload := []customerRanking{{11, "客户", 9007199254740993, 0, 9007199254740993}}
	for _, format := range []string{"", "json", "csv", "xml"} {
		t.Run("format="+format, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeReport(response, httptest.NewRequest("GET", "/?format="+format, nil), payload)
			if response.Header().Get("Content-Type") != "application/json; charset=utf-8" || response.Header().Get("Content-Disposition") != "" {
				t.Fatalf("report must return JSON without an attachment: %#v", response.Header())
			}
			if format != "" && format != "json" {
				expect(t, response, 400)
				if errorData(t, response)["code"] != "invalid_report_format" {
					t.Fatalf("removed or unknown report format accepted: %s", response.Body.String())
				}
				return
			}
			expect(t, response, 200)
			var envelope struct{ Data []customerRanking }
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || !reflect.DeepEqual(envelope.Data, payload) {
				t.Fatalf("JSON report result or integer precision changed: %s; err=%v", response.Body.String(), err)
			}
		})
	}
}

func TestReportEndpointsJSONOnlyFormatsAndPermissions(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "report-format-admin", "admin")
	_, customer := user(t, db, h, "report-format-customer", "customer")
	paths := []string{
		"income?year=2024&month=2",
		"new-clients?year=2024&month=2",
		"customer-ranking?year=2024&month=2",
		"product-income?year=2024&month=2",
		"sales?year=2024&month=2",
		"tickets?year=2024&month=2",
		"annual-cash?year=2024",
		"product-income/groups?year=2024&month=2",
		"sales/details?year=2024&month=2",
		"tickets/handlers?year=2024&month=2",
	}
	for _, report := range paths {
		t.Run(report, func(t *testing.T) {
			path := "/api/v1/admin/reports/" + report
			for _, format := range []string{"", "json", "csv", "xml"} {
				response := request(h, "GET", path+"&format="+format, nil, staff, "")
				if response.Header().Get("Content-Type") != "application/json; charset=utf-8" || response.Header().Get("Content-Disposition") != "" {
					t.Fatalf("format=%q produced a download: %#v", format, response.Header())
				}
				if format == "" || format == "json" {
					expect(t, response, 200)
					if !json.Valid(response.Body.Bytes()) {
						t.Fatalf("report did not return JSON: %s", response.Body.String())
					}
				} else {
					expect(t, response, 400)
					if errorData(t, response)["code"] != "invalid_report_format" {
						t.Fatalf("unsupported report format error: %s", response.Body.String())
					}
				}
			}
			expect(t, request(h, "GET", path+"&format=csv", nil, "", ""), 401)
			expect(t, request(h, "GET", path+"&format=csv", nil, customer, ""), 401)
		})
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'tickets.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"tickets", "tickets/handlers"} {
		expect(t, request(h, "GET", "/api/v1/admin/reports/"+path+"?year=2024&month=2&format=csv", nil, staff, ""), 403)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'reports.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	for _, report := range paths {
		response := request(h, "GET", "/api/v1/admin/reports/"+report+"&format=csv", nil, staff, "")
		expect(t, response, 403)
		if response.Header().Get("Content-Disposition") != "" {
			t.Fatalf("revoked report access offered a download: %s", report)
		}
	}
}
