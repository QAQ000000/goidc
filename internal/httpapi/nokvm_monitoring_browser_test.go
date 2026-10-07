package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

func TestNOKVMMonitoringBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("use fresh fixture paths")
		}
	}
	var reads atomic.Int32
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "monitoring") {
			reads.Add(1)
			rows := []map[string]any{}
			for i := 0; i < 72; i++ {
				at := time.Now().Add(time.Duration(-71+i) * 10 * time.Minute).UTC().Format(time.RFC3339)
				disk := map[string]any{"vda": []int{10 + i*2, 3 + i}, "vdb": []int{5 + i, 2 + i}}
				if i%7 == 0 {
					delete(disk, "vdb")
				}
				encoded, _ := json.Marshal(disk)
				rows = append(rows, map[string]any{"CreatedAt": at, "Cpu": fmt.Sprintf(`{"value":%d}`, 10+i%60), "Network": fmt.Sprintf(`{"in":%d,"out":%d}`, i*50+100, i*30+20), "Disk": string(encoded)})
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": rows})
			return
		}
		if strings.Contains(r.URL.Path, "run_status") {
			fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
			return
		}
		if r.URL.Path == "/api/virtual/42" {
			fmt.Fprint(w, `{"code":0,"data":{"name":"monitoring-vm","flow":{"code":0,"data":{"in":2147483648,"out":1073741824}},"flow_limit":10}}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"monitoring-vm","public_ip":[{"ip":"192.0.2.42"}]}}`)
	}, true)
	f.process(t)
	before := f.powerInvariant(t)
	readerID, _ := user(t, f.db, f.h, "monitoring-browser-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: "http://localhost:3099", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
	defer server.Close()
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "reader": "monitoring-browser-reader"})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("monitoring browser fixture ready")
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser fixture timeout")
		case <-tick.C:
			if _, err := os.Stat(finish); err != nil {
				continue
			}
			if reads.Load() < 4 || f.powerInvariant(t) != before {
				t.Fatal("monitor reads/finance invariant", reads.Load())
			}
			var jobs int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1", f.sid).Scan(&jobs); err != nil || jobs != 1 {
				t.Fatal("monitor created jobs", jobs, err)
			}
			return
		}
	}
}
