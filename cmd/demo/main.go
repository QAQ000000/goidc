package main

import (
	"context"
	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"log"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, p := range []struct {
		Name, Slug, Description string
		Price                   int64
	}{
		{"通用云主机 S1", "cloud-s1", "2 核 · 4 GB 内存 · 80 GB SSD · 5 Mbps", 4900},
		{"高性能云主机 P1", "cloud-p1", "4 核 · 16 GB 内存 · 200 GB SSD · 10 Mbps", 15900},
		{"企业云主机 E1", "cloud-e1", "8 核 · 32 GB 内存 · 500 GB SSD · 20 Mbps", 39900},
	} {
		if _, err = tx.Exec(ctx, "INSERT INTO products(name,slug,description,product_type,billing_cycle,price_cents,stock,provision_mode) VALUES($1,$2,$3,'server','monthly',$4,100,'simulation') ON CONFLICT(slug) DO NOTHING", p.Name, p.Slug, p.Description, p.Price); err != nil {
			log.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		log.Fatal(err)
	}
	log.Println("development catalog inserted; this command does not provision external resources")
}
