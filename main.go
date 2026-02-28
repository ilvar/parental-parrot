package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const banner = `
    ,///
   (o  >    ParentalParrot
    \_\>    Device usage monitor
`

func main() {
	dbPath := flag.String("db", "parentalparrot.db", "path to SQLite database")
	seedYAML := flag.String("seed", "config.example.yaml", "path to example YAML to seed DB when empty")
	listen := flag.String("listen", ":8080", "web UI listen address")
	resetPassword := flag.String("reset-password", "", "if set, update UI password in DB and exit (e.g. -reset-password=newpass)")
	flag.Parse()

	if *resetPassword != "" {
		db, err := OpenDB(*dbPath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer db.Close()
		if err := InitSchema(db); err != nil {
			log.Fatalf("Failed to init schema: %v", err)
		}
		_, err = db.Exec("INSERT OR REPLACE INTO config (id, ui_password) VALUES (1, ?)", *resetPassword)
		if err != nil {
			log.Fatalf("Failed to set password: %v", err)
		}
		log.Printf("UI password updated in %s", *dbPath)
		return
	}

	fmt.Print(banner)
	db, err := OpenDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		log.Fatalf("Failed to init schema: %v", err)
	}
	if err := SeedFromYAMLIfEmpty(db, *seedYAML); err != nil {
		log.Printf("Warning: seed from YAML: %v (continuing)", err)
	} else {
		log.Printf("Database ready (seeded from %s if was empty)", *seedYAML)
	}

	cfg, err := LoadConfigFromDB(db)
	if err != nil {
		log.Fatalf("Failed to load config from DB: %v", err)
	}
	log.Printf("Loaded config with %d device(s)", len(cfg.Devices))

	state := NewStateDB(db)
	if err := state.Load(); err != nil {
		log.Fatalf("Failed to load state: %v", err)
	}

	// Start monitor
	monitor := NewMonitor(cfg, state)
	stopMonitor := make(chan struct{})
	go monitor.Run(stopMonitor)
	log.Println("Monitor started (checking every 60s)")

	// Start web server (pass db for settings save + config reload)
	ws := NewWebServer(cfg, state, monitor, db)
	srv := &http.Server{
		Addr:    *listen,
		Handler: ws.Handler(),
	}
	go func() {
		host, port, _ := net.SplitHostPort(*listen)
		if host == "" || host == "0.0.0.0" {
			host = "localhost"
		}
		log.Printf("Web UI: https://%s:%s", host, port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Web server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
	close(stopMonitor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Println("State in DB (no separate save needed)")
}
