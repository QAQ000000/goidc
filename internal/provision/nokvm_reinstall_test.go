package provision

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNOKVMReinstallProtocol(t *testing.T) {
	for _, mode := range []string{"accepted", "rejected", "unknown", "duplicate", "busy", "off", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if len(q) != 3 || q.Get("signature") != Signature("fixture-token", q.Get("time"), q.Get("random")) {
					t.Error("unsigned reinstall")
				}
				if strings.Contains(r.URL.Path, "run_status") {
					state := 1
					if mode == "busy" {
						state = 4
					}
					if mode == "off" {
						state = 2
					}
					fmt.Fprintf(w, `{"code":0,"data":{"code":%d}}`, state)
					return
				}
				if r.Method != "GET" || r.URL.Path != "/api/virtual_reset_system/42/9" || r.ContentLength != 0 {
					t.Error("reinstall protocol", r.Method, r.URL.Path)
				}
				calls.Add(1)
				switch mode {
				case "rejected":
					fmt.Fprint(w, `{"code":8,"message":"supplier-secret"}`)
				case "unknown":
					fmt.Fprint(w, `{"message":"supplier-secret"}`)
				case "duplicate":
					fmt.Fprint(w, `{"code":8,"CODE":0}`)
				default:
					fmt.Fprint(w, `{"code":0}`)
				}
			}))
			defer remote.Close()
			n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "fixture-token"})
			template := Template{ID: 9, Name: "Windows Server", Group: "Windows", Login: "administrator"}
			if mode == "invalid" {
				template.ID = 0
			}
			result, err := n.Execute(t.Context(), Request{Action: "reinstall", UpstreamID: "42", Template: template})
			var failure *Error
			unknown := mode != "rejected" && mode != "busy" && mode != "invalid"
			if !errors.As(err, &failure) || failure.Unknown != unknown || strings.Contains(err.Error(), "supplier-secret") {
				t.Fatal("reinstall outcome", err)
			}
			accepted := mode == "accepted" || mode == "off"
			if result.CommandAccepted != accepted {
				t.Fatal("reinstall acceptance", result)
			}
			want := int32(1)
			if mode == "busy" || mode == "invalid" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatal("reinstall calls", calls.Load())
			}
		})
	}
}

func TestNOKVMTemplateCatalogueValidation(t *testing.T) {
	resources := Resources{AreaID: 1, TemplateID: 7, Core: 2, Memory: 2048, Login: "root"}
	valid := Template{ID: 9, Name: "Debian 13", Group: "Linux", Login: "root"}
	if (Delivery{Resources: resources}).Validate() != nil || (Delivery{Resources: resources, Templates: []Template{valid}}).Validate() != nil {
		t.Fatal("valid catalogue")
	}
	for _, template := range []Template{{ID: 0, Name: "Debian", Login: "root"}, {ID: 9, Name: "", Login: "root"}, {ID: 9, Name: "Debian\n", Login: "root"}, {ID: 9, Name: "Debian", Login: "guest"}} {
		if template.Validate() == nil {
			t.Fatal("invalid template accepted")
		}
	}
	if (Delivery{Resources: resources, Templates: []Template{valid, valid}}).Validate() == nil {
		t.Fatal("duplicate ID accepted")
	}
	templates := make([]Template, 101)
	if (Delivery{Resources: resources, Templates: templates}).Validate() == nil {
		t.Fatal("unbounded catalogue")
	}
}
