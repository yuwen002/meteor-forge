// Command server starts the meteor-forge Web management API.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminapi"
)

func main() {
	addr := os.Getenv("METEOR_ADMIN_ADDR")
	if addr == "" {
		// Keep the first-run service local-only until authentication is configured.
		addr = "127.0.0.1:8081"
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           adminapi.NewServer().Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("meteor-forge admin API listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("admin API server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("admin API shutdown failed: %v", err)
	}
}
