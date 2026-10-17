package httpapi

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/supplier"
)

func g09CohortGuardSources() []g09CohortSource {
	records := make([]g09CohortSource, 0, 450)
	at := time.Now().UTC()
	for i := 0; i < 300; i++ {
		id := int64(i + 1)
		r := g09CohortSource{FlowID: fmt.Sprintf("purchase-%03d", i), Kind: "simulation", CustomerID: int64(i%150 + 1), ProductID: 1, OrderID: id, InvoiceID: id, OrderItemID: id, ServiceID: id, ProvisionJobID: id, AcceptedAtUTC: at}
		if i >= 270 {
			r.Kind = "supplier"
			r.SupplierAccountID = int64((i-270)/3 + 1)
			r.BindingID = r.SupplierAccountID
			r.ProductID = 1 + r.SupplierAccountID
		}
		records = append(records, r)
	}
	for i := 0; i < 150; i++ {
		id := int64(i + 1)
		records = append(records, g09CohortSource{FlowID: fmt.Sprintf("ticket-%03d", i), Kind: "ticket", CustomerID: id, TicketID: id, EventID: id, StaffID: id + 150, DepartmentID: id, EmailDeliveryID: id * 2, SMSDeliveryID: id*2 + 1, EmailOutboxID: id, SMSOutboxID: id, AcceptedAtUTC: at})
	}
	return records
}

func TestG09CohortSourceGuards(t *testing.T) {
	t.Run("exact cardinality and table-local IDs", func(t *testing.T) {
		if err := g09ValidateCohortSources(g09CohortGuardSources()); err != nil {
			t.Fatal(err)
		}
	})
	cases := map[string]func([]g09CohortSource) []g09CohortSource{
		"missing source":                     func(r []g09CohortSource) []g09CohortSource { return r[:449] },
		"duplicate queue ID":                 func(r []g09CohortSource) []g09CohortSource { r[1].ProvisionJobID = r[0].ProvisionJobID; return r },
		"duplicate ticket recipient":         func(r []g09CohortSource) []g09CohortSource { r[301].StaffID = r[300].StaffID; return r },
		"duplicate delivery across channels": func(r []g09CohortSource) []g09CohortSource { r[300].SMSDeliveryID = r[300].EmailDeliveryID; return r },
		"wrong supplier budget": func(r []g09CohortSource) []g09CohortSource {
			r[270].SupplierAccountID = r[273].SupplierAccountID
			return r
		},
		"unaccepted source": func(r []g09CohortSource) []g09CohortSource { r[0].AcceptedAtUTC = time.Time{}; return r },
		"missing buyer":     func(r []g09CohortSource) []g09CohortSource { r[0].CustomerID = 999; return r },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if g09ValidateCohortSources(mutate(g09CohortGuardSources())) == nil {
				t.Fatal("invalid cohort accepted")
			}
		})
	}
	t.Run("three-resource compiled supplier and periodic oracle", func(t *testing.T) {
		private := t.TempDir()
		if os.Chmod(private, 0700) != nil {
			t.Fatal("private guard directory unavailable")
		}
		path := filepath.Join(private, "supplier.private.jsonl")
		journal, err := g09fixture.NewJournal(path)
		if err != nil {
			t.Fatal("guard journal unavailable")
		}
		t.Cleanup(func() { journal.Close() })
		key := []byte("01234567890123456789012345678901")
		resources := []g09fixture.SupplierResource{{LocalID: 81, HostID: 420081, InvoiceID: 900081}, {LocalID: 82, HostID: 420082, InvoiceID: 900082}, {LocalID: 83, HostID: 420083, InvoiceID: 900083}}
		cfg := g09fixture.SupplierConfig{Username: "g09-guard-private", Password: "g09-guard-private-password", Token: "g09-guard-private-bearer", DownstreamURL: "http://127.0.0.1:29000", Resources: resources, DownstreamTokens: map[int64]string{}, RequestBudget: 240}
		for _, r := range resources {
			cfg.DownstreamTokens[r.LocalID] = g09SupplierWorkerToken(key, r.LocalID)
		}
		metadata, err := g09fixture.NewSupplierSink(journal, cfg)
		if err != nil {
			t.Fatal("metadata guard unavailable")
		}
		gate := &g09SupplierWorkerGate{sink: metadata}
		server := g09SourceLoopbackServer(t, gate)
		adapter, err := supplier.New(supplier.Account{Provider: supplier.ProviderZJMF, BaseURL: server.URL, Username: cfg.Username, Password: cfg.Password})
		if err != nil {
			t.Fatal("compiled guard adapter unavailable")
		}
		preview, err := adapter.Product(t.Context(), 7)
		if err != nil || !preview.Supported {
			t.Fatal("compiled guard preview failed")
		}
		prefix := journal.Snapshot()
		sink, err := g09fixture.NewSupplierSink(journal, cfg)
		if err != nil || !gate.freeze(sink) {
			t.Fatal("three-resource gate freeze failed")
		}
		start := time.Now().UTC()
		for _, r := range resources {
			result, err := adapter.Create(t.Context(), supplier.CreateRequest{ProductID: 7, Cycle: "monthly", Revision: preview.Revision, LocalServiceID: r.LocalID, Host: fmt.Sprintf("svc-%d", r.LocalID), DownstreamURL: cfg.DownstreamURL, DownstreamToken: cfg.DownstreamTokens[r.LocalID], MaximumCostCents: 100, Currency: "CNY"})
			if err != nil || result.UpstreamID != r.HostID || result.InvoiceID != r.InvoiceID {
				t.Fatal("compiled three-resource create failed")
			}
		}
		// Several legitimate later reads must not fail the create oracle.
		for i := 0; i < 2; i++ {
			for _, r := range resources {
				if _, err := adapter.Sync(t.Context(), r.HostID); err != nil {
					t.Fatal("compiled periodic read failed")
				}
			}
		}
		if sink.Err() != nil || gate.err() != nil {
			t.Fatal("three-resource supplier guard failed")
		}
		e := &g09CohortEnv{}
		e.verifySupplierProtocol(t, &g09CohortSupplier{sink: sink, journal: journal, prefix: prefix, path: path}, start, time.Now().UTC())
	})
}
