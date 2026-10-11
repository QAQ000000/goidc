package httpapi

import (
	"net/http"
	"testing"
)

func TestSchedulerSettingsContract(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "scheduler-settings-root", "super_admin")
	_, staff := user(t, db, h, "scheduler-settings-staff", "admin")
	_, customer := user(t, db, h, "scheduler-settings-customer", "customer")
	path := "/api/v1/admin/scheduler-settings"
	in := map[string]any{"expected_version": 1, "enabled": false, "settings": map[string]any{"expiry_days": []int{}, "billing_grace_days": 3}}
	for _, cookie := range []string{staff, customer} {
		if w := request(h, http.MethodPut, path, in, cookie, "scheduler-denied"); w.Code != 403 {
			t.Fatalf("permission %d %s", w.Code, w.Body.String())
		}
	}
	for _, cfg := range []map[string]any{{"expiry_days": []int{1, 1}, "billing_grace_days": 0}, {"expiry_days": []int{0}, "billing_grace_days": 0}, {"expiry_days": []int{1}, "billing_grace_days": -1}, {"expiry_days": []any{1.5}, "billing_grace_days": 0}, {"expiry_days": nil, "billing_grace_days": 0}, {"expiry_days": []int{1}, "billing_grace_days": 0, "other": true}} {
		bad := map[string]any{"expected_version": 1, "enabled": true, "settings": cfg}
		w := request(h, http.MethodPut, path, bad, root, "scheduler-invalid")
		if w.Code != 400 {
			t.Fatalf("malformed %d %s", w.Code, w.Body.String())
		}
	}
	w := request(h, http.MethodPut, path, in, root, "scheduler-save-key")
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	w = request(h, http.MethodPut, path, in, root, "scheduler-save-key")
	if w.Code != 200 || data(t, w)["replayed"] != true {
		t.Fatalf("replay %d %s", w.Code, w.Body.String())
	}
	w = request(h, http.MethodPut, path, in, root, "scheduler-new-key")
	if w.Code != 409 {
		t.Fatalf("version %d", w.Code)
	}
	in["enabled"] = true
	w = request(h, http.MethodPut, path, in, root, "scheduler-save-key")
	if w.Code != 409 {
		t.Fatalf("key mismatch %d", w.Code)
	}

	for _, run := range []func() (int, error){func() (int, error) { return db.EnqueueServiceExpiryReminders(t.Context(), 100) }, func() (int, error) { return db.EnqueueBillingNotifications(t.Context(), 100) }} {
		n, err := run()
		if err != nil || n != 0 {
			t.Fatalf("disabled %d %v", n, err)
		}
	}
}
