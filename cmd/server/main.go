// Command server starts the meteor-forge Web management API.
package main

import (
	"context"
	"errors"
	"log"
	"net"
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
		// Keep the service local-only even after administrator authentication is enabled.
		addr = "127.0.0.1:8081"
	}
	if err := validateAdminAddr(addr); err != nil {
		log.Fatalf("unsafe management API bind address: %v", err)
	}

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	store, err := openMetadataStore(startupCtx)
	startupCancel()
	if err != nil {
		log.Fatalf("initialize management metadata store: %v", err)
	}
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := initializeAdministrator(bootstrapCtx, store); err != nil {
		bootstrapCancel()
		_ = store.Close()
		log.Fatalf("initialize management administrator: %v", err)
	}
	bootstrapCancel()
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("close management metadata store: %v", err)
		}
	}()

	migrationDir := os.Getenv("METEOR_MIGRATIONS_DIR")
	apiServer := adminapi.NewServerWithMigrationDir(store, migrationDir)
	defer func() {
		if err := apiServer.Close(); err != nil { log.Printf("close target database pools: %v", err) }
	}()
	httpServer := &http.Server{
		Addr: addr, Handler: apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("meteor-forge admin API listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		log.Fatalf("admin API server failed: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("admin API shutdown failed: %v", err)
	}
}
