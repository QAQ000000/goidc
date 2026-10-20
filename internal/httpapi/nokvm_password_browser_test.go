package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/provision"
)

func TestNOKVMPasswordBrowserFixture(t *testing.T) {
	ready, finish := browserFixturePaths(t)
	f, remote, previous := passwordFixture(t, "browser")
	before := f.powerInvariant(t)
	readerID, _ := user(t, f.db, f.h, "password-browser-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: "http://localhost:3099", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
	defer server.Close()
	stop := startBrowserProvisionWorker(t, f.db, nil)
	defer stop()
	writeBrowserJSON(t, ready, map[string]any{"api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "admin": "nokvm-root", "reader": "password-browser-reader"})
	waitBrowserFinish(t, ready, finish, 20*time.Minute)
	stop()
	if remote.resets.Load() != 2 || f.powerInvariant(t) != before {
		t.Fatal("browser password invariants", remote.resets.Load())
	}
	var sealed []byte
	var completed, pending int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT credentials_encrypted,
 (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='reset_password' AND status='succeeded'),
 (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND request_credentials_encrypted IS NOT NULL)
 FROM services WHERE id=$1`, f.sid).Scan(&sealed, &completed, &pending); err != nil || completed != 2 || pending != 0 {
		t.Fatal(completed, pending, err)
	}
	current, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
	if err != nil || current.Password != "fixture-browser-Unknown123!" || current.VNCPassword != previous.VNCPassword {
		t.Fatal("browser credential confirmation", err)
	}
	var raw string
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT string_agg(payload::text,'') FROM audit_events)||(SELECT string_agg(result::text,'') FROM provision_jobs)`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytesContainPassword(raw, "fixture-browser-New123!", "fixture-browser-Unknown123!", previous.Password) {
		t.Fatal("browser leaked secret")
	}
}

func bytesContainPassword(raw string, passwords ...string) bool {
	for _, password := range passwords {
		if strings.Contains(raw, password) {
			return true
		}
	}
	return false
}
