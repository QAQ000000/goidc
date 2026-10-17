package g09fixture

// This is a fixture declaration, not evidence that a product API accepted work.
import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

type CohortSourceRecord struct {
	FlowID, Kind                                               string
	CustomerID, ProductID, SupplierAccountID, BindingID        int64
	OrderID, InvoiceID, OrderItemID, ServiceID, ProvisionJobID int64
	TicketID, EventID, StaffID, DepartmentID                   int64
	EmailDeliveryID, EmailOutboxID, SMSDeliveryID, SMSOutboxID int64
	AcceptedAtUTC                                              time.Time
}

// Cohort has no exported mutable state. Sources returns an independent copy.
type Cohort struct{ sources []CohortSourceRecord }

func (c Cohort) Sources() []CohortSourceRecord {
	return append([]CohortSourceRecord(nil), c.sources...)
}

var cohortFlow = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// SealCohortSources freezes exactly 300 paid quantity-one purchases and 150
// customer-created tickets, whose two notification deliveries give 600 tasks.
// Owners may repeat; entity identities and flow identities must not repeat.
func SealCohortSources(records []CohortSourceRecord) (Cohort, error) {
	if len(records) != 450 {
		return Cohort{}, errors.New("cohort_source_count_invalid")
	}
	seen := map[string]bool{}
	unique := func(table string, id int64) bool {
		key := fmt.Sprintf("%s:%d", table, id)
		if id <= 0 || seen[key] {
			return false
		}
		seen[key] = true
		return true
	}
	counts := map[string]int{}
	accounts := map[int64]int{}
	products := map[int64]string{}
	for _, r := range records {
		if !cohortFlow.MatchString(r.FlowID) || seen["flow:"+r.FlowID] || r.CustomerID <= 0 || !cohortUTC(r.AcceptedAtUTC) {
			return Cohort{}, errors.New("cohort_source_identity_invalid")
		}
		seen["flow:"+r.FlowID] = true
		counts[r.Kind]++
		switch r.Kind {
		case "simulation", "supplier":
			if r.ProductID <= 0 || !unique("order", r.OrderID) || !unique("invoice", r.InvoiceID) || !unique("item", r.OrderItemID) || !unique("service", r.ServiceID) || !unique("job", r.ProvisionJobID) || r.TicketID != 0 || r.EventID != 0 || r.StaffID != 0 || r.DepartmentID != 0 || r.EmailDeliveryID != 0 || r.EmailOutboxID != 0 || r.SMSDeliveryID != 0 || r.SMSOutboxID != 0 {
				return Cohort{}, errors.New("cohort_purchase_mapping_invalid")
			}
			identity := fmt.Sprintf("%s:%d:%d", r.Kind, r.SupplierAccountID, r.BindingID)
			if prior, ok := products[r.ProductID]; ok && prior != identity {
				return Cohort{}, errors.New("cohort_product_source_invalid")
			}
			products[r.ProductID] = identity
			if r.Kind == "simulation" {
				if r.SupplierAccountID != 0 || r.BindingID != 0 {
					return Cohort{}, errors.New("cohort_simulation_source_invalid")
				}
			} else {
				if r.SupplierAccountID <= 0 || r.BindingID <= 0 {
					return Cohort{}, errors.New("cohort_supplier_source_invalid")
				}
				accounts[r.SupplierAccountID]++
			}
		case "ticket":
			if !unique("ticket", r.TicketID) || !unique("ticket_customer", r.CustomerID) || !unique("event", r.EventID) || !unique("staff", r.StaffID) || !unique("department", r.DepartmentID) || !unique("delivery", r.EmailDeliveryID) || !unique("delivery", r.SMSDeliveryID) || !unique("email", r.EmailOutboxID) || !unique("sms", r.SMSOutboxID) || r.ProductID != 0 || r.SupplierAccountID != 0 || r.BindingID != 0 || r.OrderID != 0 || r.InvoiceID != 0 || r.OrderItemID != 0 || r.ServiceID != 0 || r.ProvisionJobID != 0 || r.StaffID == r.CustomerID {
				return Cohort{}, errors.New("cohort_ticket_mapping_invalid")
			}
		default:
			return Cohort{}, errors.New("cohort_source_kind_invalid")
		}
	}
	if counts["simulation"] != 270 || counts["supplier"] != 30 || counts["ticket"] != 150 || len(accounts) != 10 {
		return Cohort{}, errors.New("cohort_source_denominator_invalid")
	}
	for _, n := range accounts {
		if n != 3 {
			return Cohort{}, errors.New("cohort_supplier_distribution_invalid")
		}
	}
	for _, r := range records {
		if seen[fmt.Sprintf("staff:%d", r.CustomerID)] {
			return Cohort{}, errors.New("cohort_customer_staff_overlap")
		}
	}
	return Cohort{sources: append([]CohortSourceRecord(nil), records...)}, nil
}

func cohortUTC(t time.Time) bool { _, off := t.Zone(); return !t.IsZero() && off == 0 }

type CohortLedgerCheckpoint struct{ ID, DeltaCents, BalanceAfter int64 }
type CohortCustomerBaseline struct {
	BalanceCents int64
	Ledger       []CohortLedgerCheckpoint
}
type CohortProductBaseline struct {
	Stock    *int64
	Services map[int64]bool
}
type CohortBaseline struct {
	Customers     map[int64]CohortCustomerBaseline
	Products      map[int64]CohortProductBaseline
	CapturedAtUTC time.Time
}

func freezeCohortBaseline(c Cohort, b CohortBaseline) (CohortBaseline, error) {
	bad := errors.New("cohort_baseline_invalid")
	if len(c.sources) != 450 || !cohortUTC(b.CapturedAtUTC) {
		return CohortBaseline{}, bad
	}
	users, products := map[int64]bool{}, map[int64]bool{}
	for _, r := range c.sources {
		users[r.CustomerID] = true
		if r.ProductID > 0 {
			products[r.ProductID] = true
		}
		if r.AcceptedAtUTC.Before(b.CapturedAtUTC) {
			return CohortBaseline{}, bad
		}
	}
	if len(users) != len(b.Customers) || len(products) != len(b.Products) {
		return CohortBaseline{}, bad
	}
	out := CohortBaseline{Customers: map[int64]CohortCustomerBaseline{}, Products: map[int64]CohortProductBaseline{}, CapturedAtUTC: b.CapturedAtUTC}
	ledgerIDs, serviceIDs := map[int64]bool{}, map[int64]bool{}
	for id, v := range b.Customers {
		if !users[id] || v.BalanceCents < 0 {
			return CohortBaseline{}, bad
		}
		var prev int64
		for _, l := range v.Ledger {
			if l.ID <= prev || ledgerIDs[l.ID] || l.BalanceAfter < 0 {
				return CohortBaseline{}, bad
			}
			prev = l.ID
			ledgerIDs[l.ID] = true
		}
		if len(v.Ledger) > 0 && v.Ledger[len(v.Ledger)-1].BalanceAfter != v.BalanceCents {
			return CohortBaseline{}, bad
		}
		v.Ledger = append([]CohortLedgerCheckpoint(nil), v.Ledger...)
		out.Customers[id] = v
	}
	for id, v := range b.Products {
		if !products[id] || v.Services == nil || (v.Stock != nil && *v.Stock < 0) {
			return CohortBaseline{}, bad
		}
		cp := CohortProductBaseline{Services: map[int64]bool{}}
		if v.Stock != nil {
			n := *v.Stock
			cp.Stock = &n
		}
		for sid, released := range v.Services {
			if sid <= 0 || serviceIDs[sid] {
				return CohortBaseline{}, bad
			}
			serviceIDs[sid] = true
			cp.Services[sid] = released
		}
		out.Products[id] = cp
	}
	for _, r := range c.sources {
		if r.ServiceID > 0 && serviceIDs[r.ServiceID] {
			return CohortBaseline{}, bad
		}
	}
	return out, nil
}
