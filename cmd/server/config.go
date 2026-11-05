package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func openMetadataStore(ctx context.Context) (*adminstore.Store, error) {
	key := os.Getenv("METEOR_ADMIN_ENCRYPTION_KEY")
	if key == "" {
		return nil, errors.New("METEOR_ADMIN_ENCRYPTION_KEY is required; generate one with: openssl rand -hex 32")
	}
	path := os.Getenv("METEOR_ADMIN_DB")
	if path == "" {
		configDir, err := os.UserConfigDir()
		if err != nil { return nil, err }
		path = filepath.Join(configDir, "meteor-forge", "admin.db")
	}
	return adminstore.Open(ctx, path, key)
}

// initializeAdministrator creates the first account exactly once. Existing credentials
// are never reset from environment variables on restart.
func initializeAdministrator(ctx context.Context, store *adminstore.Store) error {
	exists, err := store.HasAdmin(ctx)
	if err != nil { return err }
	if exists { return nil }
	username := os.Getenv("METEOR_ADMIN_USERNAME")
	if username == "" { username = "admin" }
	password := os.Getenv("METEOR_ADMIN_BOOTSTRAP_PASSWORD")
	if password == "" {
		return errors.New("METEOR_ADMIN_BOOTSTRAP_PASSWORD is required for first startup; set a password of at least 12 characters")
	}
	_, err = store.BootstrapAdmin(ctx, username, password)
	if errors.Is(err, adminstore.ErrAdminAlreadyExists) { return nil }
	return err
}
