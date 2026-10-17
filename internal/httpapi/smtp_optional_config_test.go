package httpapi

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
)

// The channel API permits optional fields to be omitted or JSON null. Neither
// form may silently turn into an SMTP login named "<nil>" or a sender name.
func TestSMTPInstanceOptionalFieldsReachRealTransport(t *testing.T) {
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "smtp-optional-admin", "super_admin")
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := g09fixture.NewJournal(filepath.Join(private, "smtp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	sink, err := g09fixture.StartSMTP(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	host, portText, err := net.SplitHostPort(sink.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	for index, mode := range []string{"omitted", "null", "empty"} {
		t.Run(mode, func(t *testing.T) {
			id := "smtp-optional-" + mode
			config := map[string]any{"host": host, "port": port, "from": "sender@example.test", "tls_mode": "none"}
			if mode == "null" {
				config["username"], config["from_name"] = nil, nil
			} else if mode == "empty" {
				config["username"], config["from_name"] = "", ""
			}
			expect(t, request(handler, "POST", "/api/v1/admin/channel-instances", map[string]any{
				"id": id, "category": "mail", "implementation_id": "smtp", "display_name": id,
				"enabled": true, "secret": "fixture-unused-secret", "config": config,
			}, admin, ""), 201)
			runtime, err := db.ResolveSMTPConfigForInstance(t.Context(), id, store.SMTPRuntimeConfig{})
			if err != nil || runtime.Username != "" || runtime.Password != "" || runtime.FromName != "" || runtime.Host != host || runtime.Port != port || runtime.TLSMode != "none" {
				t.Fatal("optional SMTP fields changed anonymous transport semantics", err)
			}
			provider, err := notification.NewMailProvider("smtp", module.Config{Values: map[string]any{
				"host": runtime.Host, "port": runtime.Port, "username": runtime.Username, "from": runtime.From,
				"from_name": runtime.FromName, "tls_mode": runtime.TLSMode,
			}, Secret: runtime.Password})
			if err != nil {
				t.Fatal(err)
			}
			if err = provider.Send(store.EmailJob{Recipient: "recipient@example.test", Subject: "optional " + mode, Body: "real anonymous loopback SMTP"}); err != nil {
				t.Fatal("API-configured SMTP did not reach real loopback transport", err)
			}
			if len(journal.Snapshot()) != index+1 || journal.Err() != nil {
				t.Fatal("missing durable SMTP acceptance")
			}
		})
	}
	// Required JSON null is a configuration error, never a literal "<nil>".
	for _, field := range []string{"host", "port", "from"} {
		config := map[string]any{"host": host, "port": port, "from": "sender@example.test", "tls_mode": "none"}
		config[field] = nil
		response := request(handler, "POST", "/api/v1/admin/channel-instances", map[string]any{
			"id": "smtp-required-" + field, "category": "mail", "implementation_id": "smtp", "display_name": fmt.Sprint(field),
			"enabled": true, "secret": "fixture-unused-secret", "config": config,
		}, admin, "")
		expect(t, response, 409)
		if !strings.Contains(response.Body.String(), "invalid_channel_config") {
			t.Fatal("required null returned the wrong channel error")
		}
	}
}
