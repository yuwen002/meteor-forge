package adminstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupToCreatesVerifiedStandaloneSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "admin.db")
	backupPath := filepath.Join(dir, "backups", "admin-20261106.db")
	store, err := Open(ctx, sourcePath, testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	source, err := store.Create(ctx, DataSourceInput{Name: "backup source", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenantWithAudit(ctx, TenantInput{
		Name: "Backup tenant", Code: "backup-tenant", DataSourceID: source.ID, Status: "active",
	}, "backup-admin", "127.0.0.1")
	if err != nil { t.Fatal(err) }

	gotPath, err := store.BackupTo(ctx, backupPath)
	if err != nil { t.Fatal(err) }
	if gotPath != backupPath {
		if absolute, absErr := filepath.Abs(backupPath); absErr == nil && gotPath != absolute {
			t.Fatalf("backup path=%q want %q", gotPath, absolute)
		}
	}
	info, err := os.Stat(gotPath)
	if err != nil { t.Fatal(err) }
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions=%#o want 0600", info.Mode().Perm())
	}

	backup, err := Open(ctx, gotPath, testKey)
	if err != nil { t.Fatalf("open backup: %v", err) }
	defer backup.Close()
	if _, err := backup.Get(ctx, source.ID); err != nil {
		t.Fatalf("backup is missing data source: %v", err)
	}
	if _, err := backup.GetTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("backup is missing tenant: %v", err)
	}
	events, total, err := backup.ListTenantAuditEvents(ctx, tenant.ID, 20, 0)
	if err != nil { t.Fatal(err) }
	if total != 1 || len(events) != 1 || events[0].Actor != "backup-admin" {
		t.Fatalf("backup is missing tenant audit history: total=%d events=%+v", total, events)
	}
	version, err := backup.SchemaVersion(ctx)
	if err != nil { t.Fatal(err) }
	if version < 14 {
		t.Fatalf("backup schema version=%d want at least 14", version)
	}
	if _, err := store.BackupTo(ctx, gotPath); err != ErrBackupDestinationExists {
		t.Fatalf("existing destination error=%v want %v", err, ErrBackupDestinationExists)
	}
	if _, err := store.BackupTo(ctx, sourcePath); err != ErrBackupDestinationInvalid {
		t.Fatalf("same-file destination error=%v want %v", err, ErrBackupDestinationInvalid)
	}
}

func TestBackupToSupportsInMemoryMetadataStore(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	path := filepath.Join(t.TempDir(), "memory-backup.db")
	if _, err := store.BackupTo(ctx, path); err != nil {
		t.Fatalf("backup in-memory store: %v", err)
	}
}
