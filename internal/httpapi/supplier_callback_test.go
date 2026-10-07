package httpapi

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/provision"
)

func supplierCallbackToken(serviceID int64) string {
	key := []byte("01234567890123456789012345678901")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("supplier-service:" + strconv.FormatInt(serviceID, 10)))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func supplierCallbackSign(id, token, randStr string) string {
	payload, _ := json.Marshal(map[string]string{"id": id, "rand_str": randStr, "token": token})
	sum := md5.Sum(payload)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func supplierCallbackRequest(h http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSupplierHostSyncCallback(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm-callback")
	sid, _ := f.purchase(t, f.pid, "supplier-callback")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	id, token, randStr := strconv.FormatInt(sid, 10), supplierCallbackToken(sid), "callback-rand"
	values := url.Values{
		"id": {id}, "host_id": {"42"}, "domain": {"callback.vm.example"}, "domainstatus": {"Active"},
		"username": {"root"}, "password": {"callback-secret"}, "dedicatedip": {"198.51.100.20"},
		"assignedips": {"198.51.100.21"}, "port": {"22"}, "os": {"Ubuntu 24.04"}, "os_url": {"ubuntu"},
		"nextduedate": {"1900000000"}, "rand_str": {randStr},
	}
	values.Set("signature", supplierCallbackSign(id, token, randStr))
	if got := supplierCallbackRequest(f.h, "/api/host/sync", values).Code; got != http.StatusOK {
		t.Fatalf("callback status=%d", got)
	}
	var status, domain, username, resource string
	var sealed []byte
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,domain,username,resource_info::text,credentials_encrypted FROM services WHERE id=$1`, sid).Scan(&status, &domain, &username, &resource, &sealed); err != nil {
		t.Fatal(err)
	}
	if status != "active" || domain != "callback.vm.example" || username != "root" || !strings.Contains(resource, "198.51.100.20") {
		t.Fatalf("callback not applied status=%s domain=%s username=%s resource=%s", status, domain, username, resource)
	}
	credentials, err := provision.Unseal(f.db.ProvisionEncryptionKey, sid, sealed)
	if err != nil || credentials.Password != "callback-secret" {
		t.Fatalf("callback credentials unavailable: %v", err)
	}
	values.Set("signature", "BAD")
	if got := supplierCallbackRequest(f.h, "/api/host/sync", values).Code; got != http.StatusBadRequest {
		t.Fatalf("bad callback status=%d", got)
	}
}
