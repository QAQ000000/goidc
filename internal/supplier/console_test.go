package supplier

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupplierConsoleProtocol(t *testing.T) {
	var commands int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"fixture-jwt"}`)
		case "/host/header":
			if r.URL.Query().Get("host_id") != "42" || r.URL.Query().Get("source") != "API" {
				t.Error("incorrect host header identity")
			}
			fmt.Fprint(w, `{"status":200,"data":{"host_data":{"id":42,"productid":7,"domainstatus":"Active"},"cloud_os":[{"id":17,"name":"Debian 13","group":"Linux"}]}}`)
		case "/provision/default", "/provision/custom/42":
			if r.Method != "POST" || r.PostFormValue("id") != "42" || r.PostFormValue("is_api") != "1" || r.Header.Get("Authorization") != "Bearer fixture-jwt" {
				t.Error("unsigned console command")
			}
			switch r.PostFormValue("func") {
			case "vnc":
				fmt.Fprint(w, `{"status":200,"data":{"url":"https://console.example/novnc?token=fixture"}}`)
			case "reinstall":
				if r.PostFormValue("os") != "17" || r.PostFormValue("format_data_disk") != "0" {
					t.Error("invalid reinstall mapping")
				}
				commands++
				fmt.Fprint(w, `{"status":200}`)
			case "exitRescue":
				if r.URL.Path != "/provision/custom/42" {
					t.Error("wrong rescue exit path")
				}
				commands++
				fmt.Fprint(w, `{"status":200}`)
			default:
				t.Error("unexpected command")
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	a, err := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "fixture", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	console := a.(ConsoleAdapter)
	items, err := console.Templates(t.Context(), 42)
	if err != nil || len(items) != 1 || items[0].ID != 17 {
		t.Fatal("invalid template parsing", err)
	}
	u, err := console.VNC(t.Context(), 42)
	if err != nil || u != "https://console.example/novnc?token=fixture" {
		t.Fatal("invalid console URL", err)
	}
	_, err = a.Execute(t.Context(), ActionRequest{Action: "exit_rescue_system", UpstreamID: 42})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Execute(t.Context(), ActionRequest{Action: "reinstall", UpstreamID: 42, TemplateID: 17})
	var failure *Error
	if !errors.As(err, &failure) || !failure.Unknown || failure.Code != "reinstall_confirmation_pending" || commands != 2 {
		t.Fatal("reinstall treated as completed", err)
	}
	raw, _ := json.Marshal(ActionRequest{Password: "fixture-private-password"})
	if strings.Contains(string(raw), "fixture-private-password") {
		t.Fatal("action secret serialized")
	}
}

func TestSupplierConsoleURLValidation(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "//console.example/", "https://user:pass@console.example/", "http://console.example/", "https://console.example/\n", "https://console.example/\\evil"} {
		if ValidConsoleURL(raw) {
			t.Fatalf("accepted unsafe URL %q", raw)
		}
	}
	for _, raw := range []string{"https://console.example/novnc?token=x", "http://127.0.0.1:9000/vnc"} {
		if !ValidConsoleURL(raw) {
			t.Fatalf("rejected safe URL %q", raw)
		}
	}
}
