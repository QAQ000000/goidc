// legacy-import imports a reviewed, repeatable snapshot from the old MySQL
// schema. It is dry-run by default; --mode apply is required for writes.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const sourceName = "zjmf-manger-decoded"

type importer struct {
	source         legacy.Snapshot
	report         legacy.Report
	sourceName     string
	uploadRoot     string
	fileAuthorID   int64
	legacyAuthCode string
}

func main() {
	mode := flag.String("mode", "dry_run", "dry_run or apply")
	sourceID := flag.String("source", sourceName, "legacy source identity")
	uploadRoot := flag.String("legacy-upload-root", os.Getenv("LEGACY_UPLOAD_ROOT"), "read-only legacy public/upload directory")
	fileAuthorID := flag.Int64("file-author-id", 0, "target active super administrator responsible for imported files and ticket history")
	flag.Parse()
	if *mode != "dry_run" && *mode != "apply" {
		log.Fatal("--mode must be dry_run or apply")
	}
	dsn := os.Getenv("LEGACY_MYSQL_DSN")
	if dsn == "" {
		log.Fatal("LEGACY_MYSQL_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	mysqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer mysqlDB.Close()
	snapshot, err := legacy.Read(ctx, mysqlDB)
	if err != nil {
		log.Fatal(err)
	}
	report := legacy.Inspect(snapshot, *sourceID)
	report.Mode = *mode
	imp := importer{source: snapshot, report: report, sourceName: *sourceID, uploadRoot: *uploadRoot, fileAuthorID: *fileAuthorID, legacyAuthCode: os.Getenv("LEGACY_CMF_AUTHCODE")}
	if *mode == "dry_run" {
		if err = legacy.ExpandTicketAttachments(imp.source); err == nil {
			err = imp.validate()
		}
		if err == nil {
			err = imp.prepareDownloadFiles()
		}
		if err == nil {
			err = imp.prepareTicketFiles()
		}
		if err != nil {
			imp.report.ApplyValidationError = err.Error()
			output(imp.report)
			os.Exit(1)
		}
		output(imp.report)
		return
	}
	target := os.Getenv("DATABASE_URL")
	if target == "" {
		log.Fatal("DATABASE_URL is required for apply")
	}
	cfg, err := pgxpool.ParseConfig(target)
	if err != nil {
		log.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		log.Fatal(err)
	}
	if err = (&store.Store{Pool: pool}).CheckSchema(ctx); err != nil {
		log.Fatal(err)
	}
	if err = imp.apply(ctx, pool); err != nil {
		log.Fatal(err)
	}
	output(imp.report)
}

func output(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Fatal(err)
	}
}

func randomHash() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte("legacy-reset-"+hex.EncodeToString(raw)), bcrypt.MinCost)
	return string(h), err
}
func validEmail(raw string, id int64) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if e, err := mail.ParseAddress(raw); err == nil && e.Address == raw && len(raw) <= 254 {
		return raw
	}
	return fmt.Sprintf("legacy-%d@import.invalid", id)
}
func slug(id int64) string { return "legacy-" + strconv.FormatInt(id, 10) }
func cents(raw string) int64 {
	n, err := legacy.Cents(raw)
	if err != nil {
		return 0
	}
	return n
}
func ts(raw string) time.Time {
	n, _ := strconv.ParseInt(raw, 10, 64)
	if n <= 0 {
		return time.Unix(0, 0).UTC()
	}
	return time.Unix(n, 0).UTC()
}
func jsonMap(r legacy.Record) []byte { b, _ := json.Marshal(r); return b }
func checksum(b []byte) string       { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func sourceID(r legacy.Record) int64 { n, _ := strconv.ParseInt(r["id"], 10, 64); return n }
func mapStatus(entity, raw string) string {
	switch entity {
	case "orders":
		if strings.EqualFold(raw, "Cancelled") || strings.EqualFold(raw, "Fraud") {
			return "cancelled"
		}
		if strings.EqualFold(raw, "Active") || strings.EqualFold(raw, "Completed") {
			return "active"
		}
		if strings.EqualFold(raw, "Refunded") {
			return "refunded"
		}
		return "pending"
	case "invoices":
		if strings.EqualFold(raw, "Paid") {
			return "paid"
		}
		if strings.EqualFold(raw, "Cancelled") {
			return "cancelled"
		}
		if strings.EqualFold(raw, "Refunded") {
			return "refunded"
		}
		return "unpaid"
	case "services":
		switch strings.ToLower(raw) {
		case "active":
			return "provisioning"
		case "suspended":
			return "suspended"
		case "cancelled", "fraud":
			return "cancelled"
		case "completed":
			return "terminated"
		case "deleted":
			return "terminated"
		}
		return "pending_payment"
	}
	return "pending"
}

func (im *importer) apply(ctx context.Context, pool *pgxpool.Pool) error {
	if err := legacy.ExpandTicketAttachments(im.source); err != nil {
		return err
	}
	if err := im.validate(); err != nil {
		return err
	}
	if err := im.prepareDownloadFiles(); err != nil {
		return err
	}
	if err := im.prepareTicketFiles(); err != nil {
		return err
	}
	var runID int64
	if err := pool.QueryRow(ctx, "INSERT INTO legacy_import_runs(source,mode) VALUES($1,'apply') RETURNING id", im.sourceName).Scan(&runID); err != nil {
		return err
	}
	im.report.RunID = runID
	tx, err := pool.Begin(ctx)
	if err != nil {
		_, _ = pool.Exec(ctx, "UPDATE legacy_import_runs SET status='failed',completed_at=now(),error=$2 WHERE id=$1", runID, err.Error())
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "legacy-import:"+im.sourceName); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	// Take locks in the same order as customer file writers (RBAC -> downloads -> private files).
	for _, query := range []string{"SELECT pg_advisory_xact_lock_shared(70108190)", "SELECT pg_advisory_xact_lock(70108198)", "SELECT pg_advisory_xact_lock(70108199)"} {
		if _, err = tx.Exec(ctx, query); err != nil {
			return im.fail(ctx, tx, pool, runID, err)
		}
	}
	if err = im.checkFileAuthor(ctx, tx); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	// Management, personal navigation and checkout must not observe half an import.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108197)"); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.verifySnapshot(ctx, tx); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	users := map[int64]int64{}
	customerGroups := map[int64]int64{}
	products := map[int64]int64{}
	orders := map[int64]int64{}
	invoices := map[int64]int64{}
	services := map[int64]int64{}
	groups := map[int64]int64{}
	configMaps := legacyConfigMaps{}
	if err = im.importCustomerGroups(ctx, tx, customerGroups, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importUsers(ctx, tx, users, customerGroups, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importContacts(ctx, tx, users, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importServiceGroups(ctx, tx, groups, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importProducts(ctx, tx, products, groups, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if configMaps, err = im.importProductConfiguration(ctx, tx, products, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importProductConfigurationOptionLinks(ctx, tx, configMaps, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importNavigationPreferences(ctx, tx, users, groups, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importDownloadFiles(ctx, tx, users, products, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importOrders(ctx, tx, users, products, orders, invoices, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importInvoices(ctx, tx, users, orders, invoices, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importServices(ctx, tx, users, products, orders, services, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importServiceOptionSelections(ctx, tx, services, configMaps, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importTickets(ctx, tx, users, services, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if err = im.importEmailTemplates(ctx, tx, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	for entity, records := range im.source {
		for _, r := range records {
			if err = record(ctx, tx, im.sourceName, entity, sourceID(r), runID, jsonMap(archivableRecord(entity, r))); err != nil {
				return im.fail(ctx, tx, pool, runID, err)
			}
		}
	}
	if err = im.importIssues(ctx, tx, runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO legacy_snapshot_manifests(source,contract_version,entities,run_id) VALUES($1,1,$2,$3) ON CONFLICT(source) DO NOTHING", im.sourceName, im.snapshotEntities(), runID); err != nil {
		return im.fail(ctx, tx, pool, runID, err)
	}
	b, _ := json.Marshal(im.report.Mapped)
	if _, err = tx.Exec(ctx, "UPDATE legacy_import_runs SET status='succeeded',completed_at=now(),counts=$2 WHERE id=$1", runID, b); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}
func (im *importer) fail(ctx context.Context, tx pgx.Tx, pool *pgxpool.Pool, runID int64, e error) error {
	_ = tx.Rollback(ctx)
	_, _ = pool.Exec(ctx, "UPDATE legacy_import_runs SET status='failed',completed_at=now(),error=$2 WHERE id=$1", runID, e.Error())
	return e
}

func (im *importer) importUsers(ctx context.Context, tx pgx.Tx, out, customerGroups map[int64]int64, runID int64) error {
	for _, r := range im.source["users"] {
		id := sourceID(r)
		if replayed, err := im.replay(ctx, tx, "users", r, out); err != nil {
			return err
		} else if replayed {
			if err = im.upgradeReplayedUser(ctx, tx, r, out[id]); err != nil {
				return err
			}
			continue
		}
		var target int64
		hash, err := randomHash()
		if err != nil {
			return err
		}
		legacyHash := strings.TrimSpace(r["password"])
		resetRequired := true
		if legacy.ValidCMFPasswordHash(legacyHash) && strings.TrimSpace(im.legacyAuthCode) != "" {
			hash = legacy.WrapCMFPasswordHash(legacyHash)
			resetRequired = false
		}
		if resetRequired {
			found := false
			for _, issue := range im.report.Issues {
				if issue.Entity == "users" && issue.LegacyID == id && issue.Code == "password_reset_required" {
					found = true
					break
				}
			}
			if !found {
				im.report.Issues = append(im.report.Issues, legacy.Issue{Entity: "users", LegacyID: id, Code: "password_reset_required", Detail: map[string]any{}})
			}
		}
		err = tx.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,status,balance_cents,legacy_source,legacy_id,password_reset_required)
VALUES($1,$2,$3,$4,$5,0,$6,$7,$8) ON CONFLICT (legacy_source,legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL DO UPDATE SET username=EXCLUDED.username,email=EXCLUDED.email,password_hash=EXCLUDED.password_hash,password_reset_required=EXCLUDED.password_reset_required RETURNING id`, "legacy-"+strconv.FormatInt(id, 10), validEmail(r["email"], id), hash, r["username"], mapBoolStatus(r["status"]), im.sourceName, id, resetRequired).Scan(&target)
		if err != nil {
			return err
		}
		out[id] = target
		credit := cents(r["credit"])
		if credit > 0 {
			if _, err = tx.Exec(ctx, `UPDATE users SET balance_cents=$2,updated_at=now() WHERE id=$1`, target, credit); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note) VALUES($1,'adjustment',$2,$2,'legacy_import',$3,$4,'旧系统 credit 导入') ON CONFLICT(user_id,entry_type,idempotency_key) DO NOTHING`, target, credit, id, fmt.Sprintf("legacy-balance-%d", id)); err != nil {
				return err
			}
		}
		im.report.Mapped["users"]++
		if groupID := referenceID(r, "groupid"); groupID != 0 {
			if group := customerGroups[groupID]; group != 0 {
				if _, err = tx.Exec(ctx, "INSERT INTO customer_group_members(customer_id,group_id) VALUES($1,$2) ON CONFLICT(customer_id) DO UPDATE SET group_id=EXCLUDED.group_id", target, group); err != nil {
					return err
				}
			} else {
				im.report.Issues = append(im.report.Issues, legacy.Issue{Entity: "users", LegacyID: id, Code: "missing_customer_group", Detail: map[string]any{"group_id": groupID}})
			}
		}
		if err = im.recordMapped(ctx, tx, "users", r, target, runID); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) upgradeReplayedUser(ctx context.Context, tx pgx.Tx, r legacy.Record, target int64) error {
	legacyHash := strings.TrimSpace(r["password"])
	if target < 1 || !legacy.ValidCMFPasswordHash(legacyHash) || strings.TrimSpace(im.legacyAuthCode) == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE users SET password_hash=$2,password_reset_required=false,updated_at=now()
WHERE id=$1 AND password_reset_required=true`, target, legacy.WrapCMFPasswordHash(legacyHash))
	return err
}
func mapBoolStatus(raw string) string {
	if raw != "1" {
		return "disabled"
	}
	return "active"
}

func (im *importer) importProducts(ctx context.Context, tx pgx.Tx, out, groups map[int64]int64, runID int64) error {
	prices := map[int64]legacy.Record{}
	for _, r := range im.source["prices"] {
		if r["currency"] == im.currencyID() {
			prices[priceProductID(r)] = r
		}
	}
	for _, r := range im.source["products"] {
		id := sourceID(r)
		if replayed, err := im.replay(ctx, tx, "products", r, out); err != nil {
			return err
		} else if replayed {
			continue
		}
		p := prices[id]
		cycle, price := choosePrice(p)
		if price < 0 {
			cycle = "once"
			price = 0
		}
		var target int64
		setupField := map[string]string{"monthly": "msetupfee", "quarterly": "qsetupfee", "annually": "asetupfee", "once": "osetupfee"}[cycle]
		setup := int64(0)
		if p[setupField] != "" {
			setup = cents(p[setupField])
		}
		err := tx.QueryRow(ctx, `INSERT INTO products(name,slug,description,product_type,billing_cycle,price_cents,setup_fee_cents,stock,active,provision_mode,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6,$9,NULL,false,'manual',$7,$8) RETURNING id`, r["name"], slug(id), r["description"], r["type"], cycle, price, im.sourceName, id, setup).Scan(&target)
		if err != nil {
			return err
		}
		if group := groups[referenceID(r, "groupid")]; group != 0 {
			if _, err = tx.Exec(ctx, "UPDATE products SET service_group_id=$2 WHERE id=$1", target, group); err != nil {
				return err
			}
		}
		out[id] = target
		for _, pricing := range im.source["prices"] {
			if priceProductID(pricing) != id || pricing["currency"] != im.currencyID() {
				continue
			}
			for _, field := range []struct{ price, setup, cycle string }{{"monthly", "msetupfee", "monthly"}, {"quarterly", "qsetupfee", "quarterly"}, {"annually", "asetupfee", "annually"}, {"onetime", "osetupfee", "once"}} {
				value := cents(pricing[field.price])
				if value < 0 {
					continue
				}
				if _, err = tx.Exec(ctx, `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents) VALUES($1,$2,$3,$4)
ON CONFLICT(product_id,billing_cycle) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents`, target, field.cycle, value, cents(pricing[field.setup])); err != nil {
					return err
				}
			}
		}
		im.report.Mapped["products"]++
		if err = im.recordMapped(ctx, tx, "products", r, target, runID); err != nil {
			return err
		}
	}
	return nil
}
func priceProductID(r legacy.Record) int64 {
	// pricing.id is not the product id; relid links the price row to shd_products.
	return sourceID(legacy.Record{"id": r["relid"]})
}
func choosePrice(r legacy.Record) (string, int64) {
	for _, v := range []struct{ k, c string }{{"monthly", "monthly"}, {"quarterly", "quarterly"}, {"annually", "annually"}, {"onetime", "once"}} {
		if r[v.k] != "" && cents(r[v.k]) >= 0 {
			return v.c, cents(r[v.k])
		}
	}
	return "once", -1
}

func (im *importer) importOrders(ctx context.Context, tx pgx.Tx, users, products, orders, invoices map[int64]int64, runID int64) error {
	invRows := index(im.source["invoices"])
	for _, r := range im.source["orders"] {
		id := sourceID(r)
		uid := sourceID(legacy.Record{"id": r["uid"]})
		user := users[uid]
		invID := sourceID(legacy.Record{"id": r["invoiceid"]})
		inv := invRows[invID]
		if user == 0 || inv == nil {
			continue
		}
		if replayed, err := im.replay(ctx, tx, "orders", r, orders); err != nil {
			return err
		} else if replayed {
			invoices[invID] = orders[id]
			continue
		}
		total := cents(r["amount"])
		status := mapStatus("orders", r["status"])
		if deleted(r) {
			status = "cancelled"
		}
		var target int64
		err := tx.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash,legacy_source,legacy_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT (legacy_source,legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL DO UPDATE SET total_cents=EXCLUDED.total_cents,status=EXCLUDED.status RETURNING id`, r["ordernum"], user, status, total, fmt.Sprintf("legacy-order-%d", id), checksum([]byte(r["ordernum"])), im.sourceName, id, ts(r["create_time"])).Scan(&target)
		if err != nil {
			return err
		}
		orders[id] = target
		if invID > 0 {
			invoices[invID] = target
		}
		im.report.Mapped["orders"]++
		if err = im.recordMapped(ctx, tx, "orders", r, target, runID); err != nil {
			return err
		}
	}
	_ = products
	return nil
}

func (im *importer) importInvoices(ctx context.Context, tx pgx.Tx, users, orders, invoices map[int64]int64, runID int64) error {
	sourceInvoices := index(im.source["invoices"])
	orphanTotals := map[int64]int64{}
	for _, item := range im.source["invoice_items"] {
		id := sourceID(legacy.Record{"id": item["invoice_id"]})
		if sourceInvoices[id] == nil && !deleted(item) {
			orphanTotals[id] += cents(item["amount"])
		}
	}
	// A few historical invoice items point at deleted invoice rows. Keep them
	// under review in a synthetic, non-payable invoice instead of dropping money.
	for _, item := range im.source["invoice_items"] {
		if deleted(item) {
			continue
		}
		legacyInvoiceID := sourceID(legacy.Record{"id": item["invoice_id"]})
		if sourceInvoices[legacyInvoiceID] != nil {
			continue
		}
		user := users[sourceID(legacy.Record{"id": item["uid"]})]
		if user == 0 {
			continue
		}
		target := invoices[legacyInvoiceID]
		if target == 0 {
			syntheticID := -legacyInvoiceID
			synthetic := legacy.Record{"id": strconv.FormatInt(syntheticID, 10)}
			mapped := map[int64]int64{}
			replayed, err := im.replay(ctx, tx, "orphan_invoices", synthetic, mapped)
			if err != nil {
				return err
			}
			if replayed {
				target = mapped[syntheticID]
			} else {
				err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,due_at,legacy_source,legacy_id,import_review_required)
VALUES($1,$2,NULL,'cancelled',$3,now(),$4,$5,true)
RETURNING id`, fmt.Sprintf("LEG-ORPHAN-%d", legacyInvoiceID), user, orphanTotals[legacyInvoiceID], im.sourceName, syntheticID).Scan(&target)
				if err != nil {
					return err
				}
				if err = im.recordMapped(ctx, tx, "orphan_invoices", synthetic, target, runID); err != nil {
					return err
				}
				im.report.Mapped["orphan_invoices"]++
			}
			invoices[legacyInvoiceID] = target
		}
		if err := im.insertInvoiceItem(ctx, tx, target, item, runID); err != nil {
			return err
		}
	}
	for _, r := range im.source["invoices"] {
		id := sourceID(r)
		user := users[sourceID(legacy.Record{"id": r["uid"]})]
		if user == 0 {
			continue
		}
		order := invoices[id]
		if order > 0 {
			var owner int64
			if err := tx.QueryRow(ctx, "SELECT user_id FROM orders WHERE id=$1", order).Scan(&owner); err != nil {
				return err
			}
			if owner != user {
				order = 0
			}
		}
		if replayed, err := im.replay(ctx, tx, "invoices", r, invoices); err != nil {
			return err
		} else if replayed {
			if err = im.importInvoiceItems(ctx, tx, id, invoices[id], runID); err != nil {
				return err
			}
			continue
		}
		amount := cents(r["total"])
		status := mapStatus("invoices", r["status"])
		if deleted(r) {
			status = "cancelled"
		}
		review := true
		var target int64
		var orderArg any
		if order > 0 {
			orderArg = order
		}
		err := tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,due_at,paid_at,legacy_source,legacy_id,import_review_required) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (legacy_source,legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL DO UPDATE SET amount_cents=EXCLUDED.amount_cents,status=EXCLUDED.status,import_review_required=EXCLUDED.import_review_required RETURNING id`, fmt.Sprintf("LEG-%d", id), user, orderArg, status, amount, ts(r["due_time"]), paidTime(r["paid_time"], status), im.sourceName, id, review).Scan(&target)
		if err != nil {
			return err
		}
		invoices[id] = target
		im.report.Mapped["invoices"]++
		if err = im.importInvoiceItems(ctx, tx, id, target, runID); err != nil {
			return err
		}
		if err = im.recordMapped(ctx, tx, "invoices", r, target, runID); err != nil {
			return err
		}
	}
	_ = orders
	return nil
}
func paidTime(raw, status string) any {
	if status != "paid" || raw == "0" || raw == "" {
		return nil
	}
	return ts(raw)
}
func (im *importer) importInvoiceItems(ctx context.Context, tx pgx.Tx, legacyInvoiceID, target, runID int64) error {
	for _, item := range im.source["invoice_items"] {
		if !deleted(item) && sourceID(legacy.Record{"id": item["invoice_id"]}) == legacyInvoiceID {
			if err := im.insertInvoiceItem(ctx, tx, target, item, runID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (im *importer) insertInvoiceItem(ctx context.Context, tx pgx.Tx, invoice int64, r legacy.Record, runID int64) error {
	if replayed, err := im.replay(ctx, tx, "invoice_items", r, map[int64]int64{}); err != nil {
		return err
	} else if replayed {
		return nil
	}
	var target int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, invoice, r["description"], r["type"], cents(r["amount"]), im.sourceName, sourceID(r)).Scan(&target); err != nil {
		return err
	}
	im.report.Mapped["invoice_items"]++
	return im.recordMapped(ctx, tx, "invoice_items", r, target, runID)
}

func (im *importer) importServices(ctx context.Context, tx pgx.Tx, users, products, orders, services map[int64]int64, runID int64) error {
	for _, r := range im.source["services"] {
		id := sourceID(r)
		if replayed, err := im.replay(ctx, tx, "services", r, services); err != nil {
			return err
		} else if replayed {
			var item int64
			if err = tx.QueryRow(ctx, `SELECT oi.id FROM order_items oi JOIN services s ON s.order_item_id=oi.id
WHERE s.id=$1 AND oi.legacy_source=$2 AND oi.legacy_id=$3`, services[id], im.sourceName, id).Scan(&item); err != nil {
				return fmt.Errorf("stale order item mapping: services/%d", id)
			}
			if err = im.recordMapped(ctx, tx, "order_items", r, item, runID); err != nil {
				return err
			}
			im.report.Replayed["order_items"]++
			continue
		}
		user := users[sourceID(legacy.Record{"id": r["uid"]})]
		product := products[sourceID(legacy.Record{"id": r["productid"]})]
		order := orders[sourceID(legacy.Record{"id": r["orderid"]})]
		if user == 0 || product == 0 || order == 0 {
			continue
		}
		var orderOwner int64
		if err := tx.QueryRow(ctx, "SELECT user_id FROM orders WHERE id=$1", order).Scan(&orderOwner); err != nil {
			return err
		}
		if orderOwner != user {
			continue
		}
		state := mapStatus("services", r["domainstatus"])
		review := true
		var item int64
		err := tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,legacy_source,legacy_id) SELECT $1,$2,p.name,$3,1,$4,0,$4,$5,$6 FROM products p WHERE p.id=$2 ON CONFLICT (legacy_source,legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL DO UPDATE SET total_cents=EXCLUDED.total_cents RETURNING id`, order, product, cycle(r["billingcycle"]), cents(r["firstpaymentamount"]), im.sourceName, id).Scan(&item)
		if err != nil {
			return err
		}
		if err = im.recordMapped(ctx, tx, "order_items", r, item, runID); err != nil {
			return err
		}
		im.report.Mapped["order_items"]++
		var target int64
		err = tx.QueryRow(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,upstream_id,last_error,provision_mode,legacy_source,legacy_id,import_review_required,recurring_price_cents) SELECT $1,$2,$3,p.id,p.name,$4,$5,$6,$7,$8,$9,'manual',$10,$11,$12,$14 FROM products p WHERE p.id=$13 ON CONFLICT (legacy_source,legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL DO UPDATE SET status=EXCLUDED.status,last_error=EXCLUDED.last_error RETURNING id`, user, order, item, r["domain"], cycle(r["billingcycle"]), state, instantOrNil(r["nextduedate"]), fmt.Sprintf("legacy-host-%d", id), "旧系统资源需人工核验", im.sourceName, id, review, product, cents(r["amount"])).Scan(&target)
		if err != nil {
			return err
		}
		services[id] = target
		im.report.Mapped["services"]++
		if err = im.recordMapped(ctx, tx, "services", r, target, runID); err != nil {
			return err
		}
	}
	return nil
}
func cycle(raw string) string {
	switch strings.ToLower(raw) {
	case "monthly":
		return "monthly"
	case "quarterly":
		return "quarterly"
	case "annually", "yearly":
		return "annually"
	case "once", "onetime":
		return "once"
	}
	return ""
}
func instantOrNil(raw string) any {
	if raw == "0" || raw == "" {
		return nil
	}
	return ts(raw)
}
func record(ctx context.Context, tx pgx.Tx, source, entity string, id, runID int64, payload []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO legacy_source_records(source,entity,legacy_id,run_id,payload,checksum) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(source,entity,legacy_id) DO UPDATE SET run_id=EXCLUDED.run_id,payload=EXCLUDED.payload,checksum=EXCLUDED.checksum`, source, entity, id, runID, payload, checksum(payload))
	return err
}
func (im *importer) importIssues(ctx context.Context, tx pgx.Tx, runID int64) error {
	for _, issue := range im.report.Issues {
		b, _ := json.Marshal(issue.Detail)
		if _, err := tx.Exec(ctx, `INSERT INTO legacy_import_issues(run_id,entity,legacy_id,code,detail) VALUES($1,$2,$3,$4,$5)`, runID, issue.Entity, issue.LegacyID, issue.Code, b); err != nil {
			return err
		}
	}
	return nil
}
func index(rows []legacy.Record) map[int64]legacy.Record {
	out := map[int64]legacy.Record{}
	for _, r := range rows {
		out[sourceID(r)] = r
	}
	return out
}
