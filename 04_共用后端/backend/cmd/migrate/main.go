// migrate applies pending schema migrations without starting workers or seeding data.
package main

import (
	"campusretail/internal/config"
	"campusretail/internal/db"
	"log"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	database, err := db.Open(cfg.MySQLDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database, cfg.MigrationsDir); err != nil {
		log.Fatal(err)
	}
	log.Print("Migrations applied successfully; no seed data written.")
}
