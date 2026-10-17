package g09fixture

import (
	"context"
	"net"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
)

// Actual registered production adapters talk to the sinks. This is protocol
// compatibility, without a database, outbox claim, worker or real recipient.
func TestRegisteredProductionProvidersReachDurableSinks(t *testing.T) {
	j := testJournal(t)
	smtp, err := StartSMTP(j)
	if err != nil {
		t.Fatal(err)
	}
	defer smtp.Close()
	host, portText, err := net.SplitHostPort(smtp.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := notification.NewMailProvider("smtp", module.Config{Values: map[string]any{
		"host": host, "port": port, "from": "fixture@example.test", "tls_mode": "none",
	}})
	if err != nil || !provider.Enabled() {
		t.Fatal("registered SMTP factory failed", err)
	}
	if err = provider.Send(store.EmailJob{Recipient: "staff@example.test", Subject: "g09-ticket-1", Body: "fixture ticket 1"}); err != nil {
		t.Fatal(err)
	}
	handler, err := SMSHandler(j, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	config := store.SMSRuntimeConfig{Enabled: true, Provider: "generic_http_json", Endpoint: server.URL, Token: "fixture-token"}
	job := store.SMSJob{Recipient: "+999000000001", Body: "fixture ticket 1", Kind: "ticket_customer_created", ExpiresAt: time.Now().Add(time.Minute)}
	evidence, err := notification.SendSMSWithEvidence(ctx, config, job)
	if err != nil || evidence.Outcome != "accepted" {
		t.Fatal("registered SMS adapter did not accept durable sink", err)
	}
	records := j.Snapshot()
	if len(records) != 2 || records[0].Channel != "email" || records[1].Channel != "sms" || records[0].RecipientHash != digest("staff@example.test") || records[1].RecipientHash != digest(job.Recipient) || records[1].MessageHash != digest(job.Body) || j.Err() != nil {
		t.Fatal("provider acceptance evidence does not match fixture identities")
	}
	config.Token = "incorrect-token"
	if _, err = notification.SendSMSWithEvidence(ctx, config, job); err == nil || len(j.Snapshot()) != 2 {
		t.Fatal("rejected provider request changed accepted evidence")
	}
}
