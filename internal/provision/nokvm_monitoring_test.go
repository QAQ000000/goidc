package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNOKVMMonitoringProtocolAndValidation(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "virtual_monitoring") {
			fmt.Fprint(w, `{"code":0,"data":[{"CreatedAt":"2026-10-06 12:00:00","Cpu":"{\"value\":42.5}","Network":"{\"in\":12.5,\"out\":4}","Disk":"{\"vda\":[12,3],\"vdb\":[null,8]}"},{"CreatedAt":"2026-10-06 13:00:00","Cpu":"null","Network":"{\"in\":0,\"out\":0}","Disk":"{}"}]}`)
			return
		}
		if r.URL.Path == "/api/virtual/42" {
			fmt.Fprint(w, `{"code":0,"data":{"flow":{"code":0,"data":{"in":2147483648,"out":1073741824}},"flow_limit":10}}`)
			return
		}
		fmt.Fprint(w, `{"code":0}`)
	}))
	defer remote.Close()
	n, err := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "fixture-token"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := n.Monitoring(t.Context(), "42", mustTime("2026-10-06T04:00:00Z"), mustTime("2026-10-06T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) != 2 || result.Samples[0].CPU == nil || *result.Samples[0].CPU != 42.5 || result.Samples[1].CPU != nil || result.Usage == nil || result.Usage.Total != "3221225472" || result.Usage.Limit == nil || *result.Usage.Limit != fmt.Sprint(10*(1<<30)) {
		t.Fatalf("monitoring result %#v", result)
	}
	if result.Samples[0].Disks["vdb"].Read != nil || result.Samples[0].Disks["vdb"].Write == nil {
		t.Fatal("missing values fabricated")
	}
	for _, caseValue := range []struct{ name, id, start, end string }{
		{"invalid id", "0", "2026-10-06T12:00:00Z", "2026-10-06T14:00:00Z"}, {"reverse", "42", "2026-10-06T14:00:00Z", "2026-10-06T12:00:00Z"}, {"too long", "42", "2026-10-01T00:00:00Z", "2026-10-10T00:00:00Z"},
	} {
		_, err = n.Monitoring(t.Context(), caseValue.id, mustTime(caseValue.start), mustTime(caseValue.end))
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != "invalid_monitoring_range" {
			t.Fatalf("%s: %v", caseValue.name, err)
		}
	}
}

func mustTime(value string) (outTime time.Time) { outTime, _ = time.Parse(time.RFC3339, value); return }

func TestNOKVMMonitoringAmbiguityMissingUsageAndTimezones(t *testing.T) {
	at := "2026-10-06T04:00:00Z"
	sample := map[string]any{"CreatedAt": at, "Cpu": `{"value":7}`, "Disk": `{"vda":[0,2]}`, "Network": `{"in":0,"out":12}`}
	for _, mode := range []string{"duplicate_outer", "duplicate_inner", "negative", "disk_length", "timestamp", "conflicting", "identical", "empty", "missing", "usage_overflow", "usage_invalid", "usage_failure"} {
		t.Run(mode, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "monitoring") {
					point := map[string]any{}
					for k, v := range sample {
						point[k] = v
					}
					switch mode {
					case "duplicate_outer":
						fmt.Fprint(w, `{"code":0,"CODE":8,"data":[]}`)
						return
					case "duplicate_inner":
						point["Cpu"] = `{"value":1,"VALUE":7}`
					case "negative":
						point["Network"] = `{"in":-1,"out":2}`
					case "disk_length":
						point["Disk"] = `{"vda":[1]}`
					case "timestamp":
						point["CreatedAt"] = "supplier-private-secret"
					case "missing":
						point["Cpu"] = nil
						point["Disk"] = nil
						point["Network"] = nil
					}
					points := []map[string]any{point}
					if mode == "empty" {
						points = []map[string]any{}
					}
					if mode == "identical" {
						points = append(points, point)
					}
					if mode == "conflicting" {
						points = append(points, map[string]any{"CreatedAt": at, "Cpu": `{"value":8}`})
					}
					json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": points})
					return
				}
				switch mode {
				case "usage_overflow":
					fmt.Fprint(w, `{"code":0,"data":{"flow":{"code":0,"data":{"in":9223372036854775807,"out":1}},"flow_limit":0}}`)
				case "usage_invalid":
					fmt.Fprint(w, `{"code":0,"data":{"flow":{"code":0,"data":{"in":-1,"out":2}},"flow_limit":0}}`)
				case "usage_failure":
					fmt.Fprint(w, `{"code":8,"message":"supplier-private-secret"}`)
				default:
					fmt.Fprint(w, `{"code":0,"data":{"flow":{"code":0,"data":{"in":0,"out":0}},"flow_limit":0}}`)
				}
			}))
			defer remote.Close()
			n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "token"})
			result, err := n.Monitoring(t.Context(), "42", mustTime("2026-10-06T03:00:00Z"), mustTime("2026-10-06T05:00:00Z"))
			invalid := mode == "duplicate_outer" || mode == "duplicate_inner" || mode == "negative" || mode == "disk_length" || mode == "timestamp" || mode == "conflicting"
			if (err != nil) != invalid {
				t.Fatal("validation outcome", mode, err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "supplier-private-secret") {
					t.Fatal("remote secret leaked")
				}
				return
			}
			if mode == "identical" && len(result.Samples) != 1 {
				t.Fatal("duplicate sample retained")
			}
			if mode == "empty" && len(result.Samples) != 0 {
				t.Fatal("empty data fabricated")
			}
			if mode == "missing" && (result.Samples[0].CPU != nil || result.Samples[0].NetworkIn != nil || len(result.Samples[0].Disks) != 0) {
				t.Fatal("missing measurements fabricated")
			}
			unavailable := strings.HasPrefix(mode, "usage_")
			if result.UsageAvailable == unavailable {
				t.Fatal("usage availability", mode)
			}
		})
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":0,"data":[]}`) }))
	defer remote.Close()
	for _, zone := range []string{"UTC", "Asia/Shanghai", "not-a-timezone"} {
		n, err := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "token", MonitoringTimezone: zone})
		if zone == "not-a-timezone" {
			if err == nil {
				t.Fatal("invalid timezone accepted")
			}
			continue
		}
		at, err := parseMonitoringTime("2026-10-06 12:00:00", n.monitoringLocation)
		want := 12
		if zone == "Asia/Shanghai" {
			want = 4
		}
		if err != nil || at.Hour() != want {
			t.Fatal("timezone interpretation", zone, at, err)
		}
	}
}
