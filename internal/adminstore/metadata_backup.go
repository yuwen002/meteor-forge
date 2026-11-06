package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrBackupDestinationExists  = errors.New("metadata backup destination already exists")
	ErrBackupDestinationInvalid = errors.New("invalid metadata backup destination")
)

// BackupTo creates a consistent SQLite snapshot using VACUUM INTO.
// The destination must not exist; an existing backup is never overwritten.
func (s *Store) BackupTo(ctx context.Context, destination string) (string, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" || destination == ":memory:" {
		return "", ErrBackupDestinationInvalid
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve metadata backup destination: %w", err)
	}
	if s.path != "" && s.path != ":memory:" {
		source, err := filepath.Abs(s.path)
		if err != nil {
			return "", fmt.Errorf("resolve metadata database path: %w", err)
		}
		if filepath.Clean(source) == filepath.Clean(absolute) {
			return "", ErrBackupDestinationInvalid
		}
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", ErrBackupDestinationExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect metadata backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return "", fmt.Errorf("create metadata backup directory: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", absolute); err != nil {
		return "", fmt.Errorf("create metadata database snapshot: %w", err)
	}

	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.Remove(absolute)
		}
	}()

	backupDB, err := sql.Open("sqlite", absolute)
	if err != nil {
		return "", fmt.Errorf("open metadata backup for verification: %w", err)
	}
	defer backupDB.Close()
	if err := backupDB.PingContext(ctx); err != nil {
		return "", fmt.Errorf("ping metadata backup: %w", err)
	}
	var integrity string
	if err := backupDB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return "", fmt.Errorf("check metadata backup integrity: %w", err)
	}
	if integrity != "ok" {
		return "", fmt.Errorf("metadata backup integrity check failed: %s", integrity)
	}
	var schemaVersion int
	if err := backupDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM admin_schema_migrations").Scan(&schemaVersion); err != nil {
		return "", fmt.Errorf("read metadata backup schema version: %w", err)
	}
	if schemaVersion == 0 {
		return "", errors.New("metadata backup has no schema migration history")
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		return "", fmt.Errorf("secure metadata backup permissions: %w", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", fmt.Errorf("open metadata backup for sync: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync metadata backup: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close metadata backup: %w", err)
	}
	keepBackup = true
	return absolute, nil
}

// SchemaVersion returns the highest applied management metadata schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM admin_schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("read metadata schema version: %w", err)
	}
	return version, nil
}
