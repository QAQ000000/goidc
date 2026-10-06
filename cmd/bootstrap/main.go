package main

import (
	"context"
	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"log"
	"os"
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
	if err = db.CheckSchema(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err = db.Bootstrap(context.Background(), os.Getenv("BOOTSTRAP_USERNAME"), os.Getenv("BOOTSTRAP_EMAIL"), os.Getenv("BOOTSTRAP_PASSWORD")); err != nil {
		log.Fatal(err)
	}
	log.Println("administrator created")
}
