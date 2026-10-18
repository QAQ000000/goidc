package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
)

var configurationFixtureID atomic.Int64

func configurationFixture(t *testing.T) channel.Descriptor {
	t.Helper()
	id := fmt.Sprintf("configuration_fixture_%d", configurationFixtureID.Add(1))
	min, max := 1.0, 10.0
	desc := channel.Descriptor{ID: id, Name: "Configuration fixture", Category: channel.OAuth, Fields: []channel.Field{
		{Key: "switch", Type: channel.Boolean, Required: true},
		{Key: "limit", Type: channel.Number, Integer: true, Min: &min, Max: &max, Default: 3},
		{Key: "mode", Type: channel.Select, Options: []string{"a", "b"}, Default: "b"},
		{Key: "modes", Type: channel.MultiSelect, Options: []string{"a", "b"}, Default: []string{"a"}},
		{Key: "notes", Type: channel.Textarea},
		{Key: "client_secret", Type: channel.Text, Secret: true, Required: true},
		{Key: "private_key", Type: channel.Textarea, Secret: true},
	}}
	if err := channel.RegisterFactory(desc, func(any) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	got, _ := channel.Lookup(desc.Category, desc.ID)
	return got
}

func TestChannelConfigurationNamedCredentialsAndRollback(t *testing.T) {
	desc := configurationFixture(t)
	db, h := testServer(t)
	_, admin := user(t, db, h, "named-channel-admin", "super_admin")
	created := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"category": desc.Category, "implementation_id": desc.ID, "display_name": "Named credentials", "enabled": true, "schema_version": 1,
		"config": map[string]any{"switch": false}, "secure_config": map[string]string{"client_secret": "first-secret-sentinel", "private_key": "second-secret-sentinel"},
	}, admin, "")
	expect(t, created, http.StatusCreated)
	item := data(t, created)
	id := item["id"].(string)
	path := "/api/v1/admin/channel-instances/" + id
	if item["configuration_status"] != "valid" || item["secure_fields"].(map[string]any)["client_secret"] != true || item["config"].(map[string]any)["mode"] != "b" {
		t.Fatalf("wrong instance metadata: %s", created.Body.String())
	}
	if bytes.Contains(created.Body.Bytes(), []byte("secret-sentinel")) {
		t.Fatal("credential echoed")
	}
	var fields, single []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT secret_fields,secret_ciphertext FROM channel_instances WHERE id=$1", id).Scan(&fields, &single); err != nil {
		t.Fatal(err)
	}
	if len(single) != 0 || bytes.Contains(fields, []byte("secret-sentinel")) {
		t.Fatal("credentials not isolated in ciphertext")
	}
	config, impl, err := db.RuntimeChannelInstance(t.Context(), id, channel.OAuth)
	if err != nil || impl != desc.ID || config.Secrets["client_secret"] != "first-secret-sentinel" || config.Secrets["private_key"] != "second-secret-sentinel" || config.Secret != "" {
		t.Fatal("named credentials failed runtime resolution")
	}
	updated := request(h, http.MethodPut, path, map[string]any{"display_name": "Rotated", "enabled": true, "config": map[string]any{"switch": false}, "secure_config": map[string]string{"private_key": "rotated-secret-sentinel"}, "expected_version": 1, "expected_schema_version": 1, "schema_version": 1}, admin, "")
	expect(t, updated, http.StatusOK)
	config, _, err = db.RuntimeChannelInstance(t.Context(), id, channel.OAuth)
	if err != nil || config.Secrets["client_secret"] != "first-secret-sentinel" || config.Secrets["private_key"] != "rotated-secret-sentinel" {
		t.Fatal("field replacement changed retained secret")
	}
	clear := request(h, http.MethodPut, path, map[string]any{"display_name": "Cleared optional", "enabled": true, "clear_secure_fields": []string{"private_key"}, "expected_version": 2}, admin, "")
	expect(t, clear, http.StatusOK)
	if data(t, clear)["secure_fields"].(map[string]any)["private_key"] != false {
		t.Fatal("optional secret was not independently cleared")
	}
	rejected := request(h, http.MethodPut, path, map[string]any{"display_name": "Bad clear", "enabled": true, "clear_secure_fields": []string{"client_secret"}, "expected_version": 3}, admin, "")
	expect(t, rejected, http.StatusConflict)
	if _, err = db.Pool.Exec(t.Context(), `ALTER TABLE audit_events ADD CONSTRAINT reject_channel_update CHECK (action <> 'channel.instance_updated') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	failed := request(h, http.MethodPut, path, map[string]any{"display_name": "Must roll back", "enabled": false, "secure_config": map[string]string{"client_secret": "failed-secret-sentinel"}, "expected_version": 3}, admin, "")
	expect(t, failed, http.StatusConflict)
	var version int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT version FROM channel_instances WHERE id=$1", id).Scan(&version); err != nil || version != 3 {
		t.Fatal("failed audit changed version")
	}
	config, _, err = db.RuntimeChannelInstance(t.Context(), id, channel.OAuth)
	if err != nil || config.Secrets["client_secret"] != "first-secret-sentinel" {
		t.Fatal("failed audit changed credentials")
	}
	// A ciphertext copied across named fields must fail authentication.
	if _, err = db.Pool.Exec(t.Context(), `UPDATE channel_instances SET secret_fields=jsonb_set(secret_fields,'{private_key}',secret_fields->'client_secret') WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.RuntimeChannelInstance(t.Context(), id, channel.OAuth); err == nil {
		t.Fatal("field-swapped ciphertext decrypted")
	}
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,secret_fields) SELECT 'copied-credentials',category,implementation_id,'Copied credentials',true,config,$2 FROM channel_instances WHERE id=$1`, id, fields); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.RuntimeChannelInstance(t.Context(), "copied-credentials", channel.OAuth); err == nil {
		t.Fatal("instance-swapped ciphertext decrypted")
	}
}

func TestChannelConfigurationRejectsPollutionAndMissingContracts(t *testing.T) {
	desc := configurationFixture(t)
	db, h := testServer(t)
	adminID, admin := user(t, db, h, "schema-channel-admin", "super_admin")
	for _, tc := range []struct {
		name   string
		config map[string]any
	}{
		{"secret", map[string]any{"switch": false, "client_secret": "must-not-persist"}},
		{"unknown", map[string]any{"switch": false, "unexpected": "must-not-persist"}},
		{"boolean", map[string]any{"switch": "false"}},
		{"enum", map[string]any{"switch": false, "mode": "wrong"}},
		{"fraction", map[string]any{"switch": false, "limit": 1.5}},
		{"range", map[string]any{"switch": false, "limit": 11}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{"category": desc.Category, "implementation_id": desc.ID, "display_name": tc.name, "enabled": false, "config": tc.config}, admin, "")
			expect(t, resp, http.StatusConflict)
			if bytes.Contains(resp.Body.Bytes(), []byte("must-not-persist")) {
				t.Fatal("invalid value echoed")
			}
		})
	}
	for _, impl := range []string{"zjmf", "nokvm"} {
		resp := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{"category": "provision", "implementation_id": impl, "display_name": "No parallel configuration", "enabled": false, "config": map[string]any{}}, admin, "")
		expect(t, resp, http.StatusConflict)
	}
	created := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{"category": desc.Category, "implementation_id": desc.ID, "display_name": "Schema fixture", "enabled": false, "config": map[string]any{"switch": false}}, admin, "")
	expect(t, created, http.StatusCreated)
	id := data(t, created)["id"].(string)
	path := "/api/v1/admin/channel-instances/" + id
	missingEnabled := request(h, http.MethodPut, path, map[string]any{"expected_version": 1}, admin, "")
	expect(t, missingEnabled, http.StatusBadRequest)
	wrongSchema := request(h, http.MethodPut, path, map[string]any{"enabled": false, "display_name": "Stale schema", "expected_version": 1, "expected_schema_version": 2}, admin, "")
	expect(t, wrongSchema, http.StatusConflict)
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM channel_instances WHERE display_name IN ('secret','unknown','boolean','enum','fraction','range','No parallel configuration')").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid configuration persisted")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET role='customer' WHERE id=$1", adminID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodGet, "/api/v1/admin/channel-instances", nil, admin, ""), http.StatusForbidden)
	expect(t, request(h, http.MethodPut, path, map[string]any{"enabled": false, "expected_version": 1}, admin, ""), http.StatusForbidden)
}

func TestChannelConfigurationMissingAndIncompatibleInstancesRemainManageable(t *testing.T) {
	desc := configurationFixture(t)
	db, h := testServer(t)
	_, admin := user(t, db, h, "orphan-channel-admin", "super_admin")
	for _, tc := range []struct {
		id, implementation, status string
		schema                     int
	}{
		{"orphan-configuration", "removed_implementation", "implementation_missing", 1},
		{"incompatible-configuration", desc.ID, "schema_incompatible", 2},
	} {
		t.Run(tc.status, func(t *testing.T) {
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,schema_version) VALUES($1,'oauth',$2,$1,true,'{"old_secret":"hidden-config-sentinel"}',$3)`, tc.id, tc.implementation, tc.schema); err != nil {
				t.Fatal(err)
			}
			list := request(h, http.MethodGet, "/api/v1/admin/channel-instances?category=oauth", nil, admin, "")
			expect(t, list, http.StatusOK)
			if bytes.Contains(list.Body.Bytes(), []byte("hidden-config-sentinel")) {
				t.Fatal("uninterpretable configuration echoed")
			}
			found := false
			for _, row := range listData(t, list) {
				if row["id"] == tc.id {
					found = true
					if row["configuration_status"] != tc.status || len(row["config"].(map[string]any)) != 0 {
						t.Fatal("wrong orphan status")
					}
				}
			}
			if !found {
				t.Fatal("orphan instance hidden")
			}
			if _, _, err := db.RuntimeChannelInstance(t.Context(), tc.id, channel.OAuth); err == nil {
				t.Fatal("unavailable implementation executed")
			}
			disabled := request(h, http.MethodPut, "/api/v1/admin/channel-instances/"+tc.id, map[string]any{"enabled": false, "expected_version": 1}, admin, "")
			expect(t, disabled, http.StatusOK)
			deleted := request(h, http.MethodDelete, "/api/v1/admin/channel-instances/"+tc.id, map[string]any{"expected_version": 2}, admin, "")
			expect(t, deleted, http.StatusOK)
		})
	}
	// Existing pollution is reported by field name and filtered on reads.
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config) VALUES('polluted-configuration','oauth',$1,'Polluted',false,'{"switch":false,"client_secret":"hidden-config-sentinel","old_key":"hidden-config-sentinel"}')`, desc.ID); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetChannelInstance(t.Context(), "polluted-configuration")
	if err != nil || row.ConfigurationStatus != "invalid" || len(row.ValidationErrors) != 2 {
		t.Fatal("pollution not classified")
	}
	raw, _ := json.Marshal(row)
	if bytes.Contains(raw, []byte("hidden-config-sentinel")) {
		t.Fatal("polluted configuration echoed")
	}
}
