package main

import (
	"context"
	"github.com/codex/zjmfgo/internal/config"
	_ "github.com/codex/zjmfgo/internal/modules/all"
	"github.com/codex/zjmfgo/internal/store"
	"log"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	db, err := store.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(context.Background()); err != nil {
		log.Fatal(err)
	}
	log.Println("migrations applied")
}
