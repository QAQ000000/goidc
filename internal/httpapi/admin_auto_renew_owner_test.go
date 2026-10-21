package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func adminAutoRenewOwnerFixture(t *testing.T) serviceTransferFixture {
	t.Helper()
	f := newServiceTransferFixture(t, false)
	contentRole(t, f.db, f.actor, "services.view", "customers.view", "services.transfer", "services.manage")
	return f
}

func adminAutoRenewOwnerBody(customer, generation int64, enabled bool) map[string]any {
	return map[string]any{"enabled": enabled, "expected_customer_id": customer, "expected_owner_generation": generation}
}

func assertAdminAutoRenewOwnerRejected(t *testing.T, f serviceTransferFixture, body map[string]any) {
	t.Helper()
	before := serviceTransferFacts(t, f)
	w := request(f.h, "PATCH", adminRenewalPath(f.sid)+"/auto-renew", body, f.staff, "")
	expect(t, w, http.StatusConflict)
	if errorData(t, w)["code"] != "service_owner_changed" {
		t.Fatal("stale automatic renewal intent did not identify its changed owner", w.Body.String())
	}
	serviceTransferUnchanged(t, f, before)
}

func TestAdminAutoRenewOwnerGuardStrictInputAndAudience(t *testing.T) {
	f := adminAutoRenewOwnerFixture(t)
	before := serviceTransferFacts(t, f)
	for name, body := range map[string]map[string]any{
		"missing_enabled":     {"expected_customer_id": f.uid, "expected_owner_generation": 0},
		"missing_customer":    {"enabled": true, "expected_owner_generation": 0},
		"missing_generation":  {"enabled": true, "expected_customer_id": f.uid},
		"zero_customer":       adminAutoRenewOwnerBody(0, 0, true),
		"negative_customer":   adminAutoRenewOwnerBody(-1, 0, true),
		"negative_generation": adminAutoRenewOwnerBody(f.uid, -1, true),
		"null_customer":       {"enabled": true, "expected_customer_id": nil, "expected_owner_generation": 0},
		"null_generation":     {"enabled": true, "expected_customer_id": f.uid, "expected_owner_generation": nil},
		"string_generation":   {"enabled": true, "expected_customer_id": f.uid, "expected_owner_generation": "0"},
		"fraction_generation": {"enabled": true, "expected_customer_id": f.uid, "expected_owner_generation": 0.5},
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, request(f.h, "PATCH", adminRenewalPath(f.sid)+"/auto-renew", body, f.staff, ""), http.StatusBadRequest)
			serviceTransferUnchanged(t, f, before)
		})
	}
	body := adminAutoRenewOwnerBody(f.uid, 0, true)
	// The customer setting retains its own ownership check; administrator-only
	// expectations cannot be supplied through the customer audience.
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", f.sid), body, f.customer, ""), http.StatusBadRequest)
	serviceTransferUnchanged(t, f, before)
	contentRole(t, f.db, f.actor, "services.view", "customers.view", "services.transfer")
	expect(t, request(f.h, "PATCH", adminRenewalPath(f.sid)+"/auto-renew", body, f.staff, ""), http.StatusForbidden)
	serviceTransferUnchanged(t, f, before)
}

func TestAdminAutoRenewOwnerGuardInitialGenerationAndLegacyCompatibility(t *testing.T) {
	f := adminAutoRenewOwnerFixture(t)
	w := request(f.h, "GET", adminRenewalPath(f.sid), nil, f.staff, "")
	expect(t, w, http.StatusOK)
	if data(t, w)["user_id"] != float64(f.uid) || data(t, w)["owner_generation"] != float64(0) {
		t.Fatal("service read omitted its initial ownership period", w.Body.String())
	}
	before := serviceTransferPreservedFacts(t, f)
	path := adminRenewalPath(f.sid) + "/auto-renew"
	w = request(f.h, "PATCH", path, adminAutoRenewOwnerBody(f.uid, 0, true), f.staff, "")
	expect(t, w, http.StatusOK)
	if data(t, w)["service_id"] != float64(f.sid) || data(t, w)["auto_renew"] != true {
		t.Fatal("guarded automatic renewal setting did not confirm its target", w.Body.String())
	}
	var enabled bool
	var audits int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT auto_renew,
 (SELECT count(*) FROM audit_events WHERE action='service.auto_renew.updated' AND entity_type='service' AND entity_id=s.id::text)
 FROM services s WHERE id=$1`, f.sid).Scan(&enabled, &audits); err != nil || !enabled || audits != 1 {
		t.Fatal("guarded setting did not atomically update and audit", enabled, audits, err)
	}
	if after := serviceTransferPreservedFacts(t, f); after != before {
		t.Fatal("automatic renewal setting changed historical billing or resources", before, after)
	}
	// Existing clients can still send only enabled. No fictional replay key or
	// service-version CAS is required by either form of the setting endpoint.
	expect(t, request(f.h, "PATCH", path, map[string]any{"enabled": false}, f.staff, ""), http.StatusOK)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", f.sid), map[string]any{"enabled": true}, f.customer, ""), http.StatusOK)
}

func TestAdminAutoRenewOwnerGuardRejectsRealTransferAndRoundTrip(t *testing.T) {
	f := adminAutoRenewOwnerFixture(t)
	oldIntent := adminAutoRenewOwnerBody(f.uid, 0, true)
	// This is the same read performed before the UI dispatches its PATCH.
	expect(t, request(f.h, "GET", adminRenewalPath(f.sid), nil, f.staff, ""), http.StatusOK)
	forward := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
	serviceTransferApply(t, f, forward, "auto-renew-owner-forward")
	assertAdminAutoRenewOwnerRejected(t, f, oldIntent)
	assertAdminAutoRenewOwnerRejected(t, f, adminAutoRenewOwnerBody(f.target, 0, true))

	backward := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.target, f.uid, 1))
	serviceTransferApply(t, f, backward, "auto-renew-owner-backward")
	assertAdminAutoRenewOwnerRejected(t, f, oldIntent)
	assertAdminAutoRenewOwnerRejected(t, f, adminAutoRenewOwnerBody(f.uid, 1, true))
	expect(t, request(f.h, "PATCH", adminRenewalPath(f.sid)+"/auto-renew", adminAutoRenewOwnerBody(f.uid, 2, true), f.staff, ""), http.StatusOK)
}

func TestAdminAutoRenewOwnerGuardRechecksAfterRealTransferLockWait(t *testing.T) {
	f := adminAutoRenewOwnerFixture(t)
	body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
	gate, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(741098553)"); err != nil {
		t.Fatal(err)
	}
	// Stop the real transfer after it has updated ownership but before commit,
	// while its service row lock still protects that uncommitted ownership.
	if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_auto_renew_owner_transfer() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='service.ownership.transferred' THEN PERFORM pg_advisory_xact_lock(741098553); END IF; RETURN NEW; END $$;
 CREATE TRIGGER wait_auto_renew_owner_transfer BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_auto_renew_owner_transfer()`); err != nil {
		t.Fatal(err)
	}
	transferDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		transferDone <- request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, "auto-renew-owner-wait-transfer")
	}()
	gatePID := gate.Conn().PgConn().PID()
	waitUploadBlockedBy(t, f.db, gatePID)
	var transferPID uint32
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid))", gatePID).Scan(&transferPID); err != nil {
		t.Fatal(err)
	}
	autoDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		autoDone <- request(f.h, "PATCH", adminRenewalPath(f.sid)+"/auto-renew", adminAutoRenewOwnerBody(f.uid, 0, true), f.staff, "")
	}()
	waitUploadBlockedBy(t, f.db, transferPID)
	if err = gate.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, transferDone), http.StatusCreated)
	w := awaitBalanceResponse(t, autoDone)
	expect(t, w, http.StatusConflict)
	if errorData(t, w)["code"] != "service_owner_changed" {
		t.Fatal("waiting setting authorized an obsolete customer", w.Body.String())
	}
	var owner, generation, autoAudits, transfers int64
	var enabled bool
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT user_id,service_ownership_generation(id),auto_renew,
 (SELECT count(*) FROM audit_events WHERE action='service.auto_renew.updated' AND entity_type='service' AND entity_id=s.id::text),
 (SELECT count(*) FROM service_ownership_transfers WHERE service_id=s.id)
 FROM services s WHERE id=$1`, f.sid).Scan(&owner, &generation, &enabled, &autoAudits, &transfers); err != nil || owner != f.target || generation != 1 || enabled || autoAudits != 0 || transfers != 1 {
		t.Fatal("waiting old intent changed the recipient setting or emitted a success audit", owner, generation, enabled, autoAudits, transfers, err)
	}
}
