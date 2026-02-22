package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.toml", "path to config file")
	statePath := flag.String("state", "state.json", "path to state file")
	listen := flag.String("listen", ":8080", "web UI listen address")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Loaded config with %d device(s)", len(cfg.Devices))

	state := NewState(*statePath)
	if err := state.Load(); err != nil {
		log.Fatalf("Failed to load state: %v", err)
	}
	log.Printf("State loaded from %s", *statePath)

	// Start monitor
	monitor := NewMonitor(cfg, state)
	stopMonitor := make(chan struct{})
	go monitor.Run(stopMonitor)
	log.Println("Monitor started (checking every 60s)")

	// Start web server
	ws := NewWebServer(cfg, state)
	srv := &http.Server{
		Addr:    *listen,
		Handler: ws.Handler(),
	}
	go func() {
		log.Printf("Web UI listening on %s", *listen)
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

	if err := state.Save(); err != nil {
		log.Printf("Failed to save state: %v", err)
	} else {
		log.Println("State saved")
	}
}
