package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNOKVMNATWireProtocolAndMembership(t *testing.T) {
	for _, kind := range []string{"acl", "web"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := natPath(kind)
			var calls []string
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if len(q) != 3 || q.Get("signature") != Signature("nat-fixture-token", q.Get("time"), q.Get("random")) {
					t.Error("unsigned supplier request")
				}
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path == path+"/42" && r.Method == "GET" {
					if kind == "acl" {
						fmt.Fprint(w, `{"code":0,"data":[{"id":9007199254740993,"name":"SSH","type":"tcp","exterior_port":21022,"interior_port":22,"supplier_secret":"hidden"}]}`)
					} else {
						fmt.Fprint(w, `{"code":0,"data":[{"id":9007199254740994,"domain":"example.test","exterior_port":80,"interior_port":8080,"supplier_secret":"hidden"}]}`)
					}
					return
				}
				if r.Method == "POST" {
					r.ParseForm()
					want := url.Values{"virtual": {"42"}, "interior_port": {"8081"}}
					if kind == "acl" {
						want.Set("name", "SSH new")
						want.Set("type", "tcp")
					} else {
						want.Set("domain", "new.example.test")
						want.Set("exterior_port", "80")
						want.Set("exterior_type", "http")
						want.Set("interior_type", "http")
					}
					if r.PostForm.Encode() != want.Encode() || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Errorf("wrong NAT form: %s", r.PostForm.Encode())
					}
				} else if r.ContentLength > 0 {
					t.Error("delete has a form")
				}
				fmt.Fprint(w, `{"code":0,"message":"supplier-secret"}`)
			}))
			defer remote.Close()
			n, err := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "nat-fixture-token"})
			if err != nil {
				t.Fatal(err)
			}
			items, err := n.ListNAT(t.Context(), kind, "42")
			if err != nil || len(items) != 1 || !ValidResourceID(items[0].ID) {
				t.Fatal(items, err)
			}
			raw, _ := json.Marshal(items)
			if strings.Contains(string(raw), "hidden") {
				t.Fatal("supplier field leaked")
			}
			request := NATRequest{Kind: kind, Operation: "create", InteriorPort: 8081, ProfileID: "fixture", UpstreamID: "42", Limit: 2}
			if kind == "acl" {
				request.Name = "SSH new"
				request.Type = "tcp"
			} else {
				request.Domain = "new.example.test"
				request.ExteriorPort = 80
			}
			result, err := n.ExecuteNAT(t.Context(), request)
			var failure *Error
			if !errors.As(err, &failure) || !failure.Unknown || failure.Code != "nat_confirmation_pending" || !result.CommandAccepted {
				t.Fatal(result, err)
			}
			request = NATRequest{Kind: kind, Operation: "delete", PointID: items[0].ID, Point: &items[0], ProfileID: "fixture", UpstreamID: "42", Limit: 2}
			result, err = n.ExecuteNAT(t.Context(), request)
			if !errors.As(err, &failure) || !failure.Unknown || !result.CommandAccepted {
				t.Fatal(result, err)
			}
			request.PointID = "99"
			request.Point = &NATItem{ID: "99"}
			_, err = n.ExecuteNAT(t.Context(), request)
			if !errors.As(err, &failure) || failure.Unknown || failure.Code != "nat_point_changed" {
				t.Fatal("foreign point dispatched", err)
			}
			want := []string{"GET " + path + "/42", "GET " + path + "/42", "POST " + path + "/", "GET " + path + "/42", "DELETE " + path + "/" + items[0].ID, "GET " + path + "/42"}
			if strings.Join(calls, "|") != strings.Join(want, "|") {
				t.Fatal(calls)
			}
		})
	}
}

func TestNOKVMNATRejectsAmbiguousListsAndInputs(t *testing.T) {
	for _, payload := range []string{`{"code":0,"data":null}`, `{"code":0,"data":{}}`, `{"code":0,"data":[{"id":1,"ID":2,"name":"one","type":"tcp","exterior_port":80,"interior_port":22}]}`, `{"code":0,"data":[{"id":1,"name":"one","type":"tcp","exterior_port":80,"interior_port":22},{"id":1,"name":"two","type":"udp","exterior_port":81,"interior_port":23}]}`, `{"code":0,"data":[{"id":0,"name":"one","type":"tcp","exterior_port":80,"interior_port":22}]}`, `{"code":0,"data":[{"id":9223372036854775808,"name":"one","type":"tcp","exterior_port":80,"interior_port":22}]}`} {
		var writes atomic.Int32
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				writes.Add(1)
			}
			fmt.Fprint(w, payload)
		}))
		n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "secret"})
		_, err := n.ExecuteNAT(t.Context(), NATRequest{Kind: "acl", Operation: "create", Name: "test", Type: "tcp", InteriorPort: 22, ProfileID: "fixture", UpstreamID: "42", Limit: 2})
		var failure *Error
		if !errors.As(err, &failure) || failure.Unknown || writes.Load() != 0 {
			t.Fatal("invalid directory authorized write", err)
		}
		remote.Close()
	}
	for _, domain := range []string{"user:pass@example.com", "example.com:80", "example.com/path", "a..example.com", "-bad.example.com", "example.com\n"} {
		if ValidNATDomain(domain) {
			t.Fatal("invalid domain", domain)
		}
	}
	for _, port := range []int{0, 65536, -1} {
		if ValidNATRequest(NATRequest{Kind: "web", Operation: "create", Domain: "example.com", ExteriorPort: 80, InteriorPort: port}) {
			t.Fatal("invalid port", port)
		}
	}
}
