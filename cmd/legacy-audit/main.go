// legacy-audit is a read-only inventory and reconciliation probe for the old
// ThinkPHP database. It deliberately has no PostgreSQL write path.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type report struct {
	Source string         `json:"source"`
	Mode   string         `json:"mode"`
	Tables map[string]int `json:"tables"`
	Checks map[string]int `json:"checks"`
	Notes  []string       `json:"notes"`
}

func main() {
	dsn := os.Getenv("LEGACY_MYSQL_DSN")
	if dsn == "" {
		log.Fatal("LEGACY_MYSQL_DSN is required; use a read-only MySQL account")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()
	r := report{Source: "legacy_mysql", Mode: "dry_run", Tables: map[string]int{}, Checks: map[string]int{}, Notes: []string{
		"password hashes and financial decimals are not copied by this probe",
		"review the JSON report before implementing an apply importer",
	}}
	for name, table := range map[string]string{"clients": "shd_clients", "products": "shd_products", "orders": "shd_orders", "invoices": "shd_invoices", "hosts": "shd_host"} {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&n); err != nil {
			log.Fatalf("count %s: %v", table, err)
		}
		r.Tables[name] = n
	}
	queries := map[string]string{
		"invoices_without_order":        "SELECT COUNT(*) FROM shd_invoices i LEFT JOIN shd_orders o ON o.invoiceid=i.id WHERE o.id IS NULL AND i.invoice_id=0",
		"orders_without_invoice":        "SELECT COUNT(*) FROM shd_orders o LEFT JOIN shd_invoices i ON i.id=o.invoiceid WHERE i.id IS NULL AND o.invoiceid<>0",
		"order_invoice_amount_mismatch": "SELECT COUNT(*) FROM shd_orders o JOIN shd_invoices i ON i.id=o.invoiceid WHERE ABS(o.amount-i.total)>0.005",
		"active_hosts_without_order":    "SELECT COUNT(*) FROM shd_host h LEFT JOIN shd_orders o ON o.id=h.orderid WHERE h.domainstatus='Active' AND o.id IS NULL",
	}
	for name, query := range queries {
		var n int
		if err = tx.QueryRowContext(ctx, query).Scan(&n); err != nil {
			log.Fatalf("check %s: %v", name, err)
		}
		r.Checks[name] = n
	}
	if err = tx.Commit(); err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err = enc.Encode(r); err != nil {
		log.Fatal(err)
	}
}
