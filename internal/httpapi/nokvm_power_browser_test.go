package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

func TestNOKVMPowerBrowserFixture(t *testing.T) {
	ready, finish := browserFixturePaths(t)
	f, _, calls := powerFixture(t, `{"code":0}`)
	f.action(t, "sync")
	before := f.powerInvariant(t)
	readerID, _ := user(t, f.db, f.h, "power-browser-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: "http://localhost:3099", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
	defer server.Close()
	stop := startBrowserProvisionWorker(t, f.db, nil)
	defer stop()
	writeBrowserJSON(t, ready, map[string]any{"api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "reader": "power-browser-reader"})
	waitBrowserFinish(t, ready, finish, 20*time.Minute)
	stop()
	if calls.Load() != 5 || f.powerInvariant(t) != before {
		t.Fatal("browser power invariant", calls.Load())
	}
	var unfinished int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND status<>'succeeded'", f.sid).Scan(&unfinished); err != nil || unfinished != 0 {
		t.Fatal(unfinished, err)
	}
}
