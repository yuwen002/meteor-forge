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
