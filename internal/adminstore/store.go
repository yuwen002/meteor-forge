// Package adminstore persists management-plane metadata separately from target databases.
package adminstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"database/sql"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("data source not found")
	ErrConflict = errors.New("data source name already exists")
	ErrInvalidInput = errors.New("invalid data source input")
	ErrActiveTasks = errors.New("data source has active migration tasks")
	ErrActiveShardDDLTasks = errors.New("data source has active shard DDL tasks")
)

// DataSource contains only fields safe to return to API clients.
type DataSource struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Driver    string    `json:"driver"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DataSourceInput is accepted from trusted management API handlers.
// DSN is intentionally never included in DataSource responses.
type DataSourceInput struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

// Store owns the management metadata database and the DSN encryption key.
type Store struct {
	db  *sql.DB
	aead cipher.AEAD
}

// Open creates the metadata directory, opens SQLite, and applies schema migrations.
// keyHex must contain exactly 32 bytes encoded as 64 hexadecimal characters.
func Open(ctx context.Context, path, keyHex string) (*Store, error) {
	key, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil || len(key) != 32 {
		return nil, errors.New("METEOR_ADMIN_ENCRYPTION_KEY must be 64 hexadecimal characters (32 bytes)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create metadata encryption cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create metadata encryption mode: %w", err)
	}

	if path == "" {
		return nil, errors.New("metadata database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create metadata directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open metadata database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, aead: aead}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure metadata database: %w", err)
	}
	if path != ":memory:" {
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("enable metadata WAL: %w", err)
		}
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("secure metadata database file permissions: %w", err)
		}
	}
	return store, nil
}

// Close releases the metadata database connection pool.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metadata schema migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	const schema = `CREATE TABLE IF NOT EXISTS admin_data_sources (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL COLLATE NOCASE UNIQUE,
		driver TEXT NOT NULL,
		dsn_ciphertext BLOB NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate management metadata schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create metadata schema history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_migration_tasks (
		id TEXT PRIMARY KEY,
		data_source_id TEXT NOT NULL,
		action TEXT NOT NULL,
		target_version TEXT NOT NULL DEFAULT '',
		steps INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		message TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT
	)`); err != nil {
		return fmt.Errorf("create migration task table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_migration_tasks_created
		ON admin_migration_tasks(created_at DESC)`); err != nil {
		return fmt.Errorf("index migration tasks: %w", err)
	}

	// Add batch_id without rebuilding existing metadata databases.
	columns, err := tx.QueryContext(ctx, "PRAGMA table_info(admin_migration_tasks)")
	if err != nil { return fmt.Errorf("inspect migration task schema: %w", err) }
	hasBatchID := false
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			columns.Close()
			return fmt.Errorf("read migration task schema: %w", err)
		}
		if name == "batch_id" { hasBatchID = true }
	}
	if err := columns.Err(); err != nil { columns.Close(); return err }
	if err := columns.Close(); err != nil { return err }
	if !hasBatchID {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE admin_migration_tasks ADD COLUMN batch_id TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("add migration task batch ID: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_admin_migration_tasks_batch ON admin_migration_tasks(batch_id, created_at)"); err != nil {
		return fmt.Errorf("index migration task batches: %w", err)
	}
	// Keep data source configuration stable while queued or running migrations
	// still depend on its connection settings. Triggers make the check atomic
	// with the mutation, including requests from concurrent HTTP handlers.
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_datasource_update_with_active_tasks
		BEFORE UPDATE ON admin_data_sources
		WHEN EXISTS (SELECT 1 FROM admin_migration_tasks
			WHERE data_source_id = OLD.id AND status IN ('queued', 'running'))
		BEGIN
			SELECT RAISE(ABORT, 'data source has active migration tasks');
		END`); err != nil {
		return fmt.Errorf("protect active migration data source updates: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_datasource_delete_with_active_tasks
		BEFORE DELETE ON admin_data_sources
		WHEN EXISTS (SELECT 1 FROM admin_migration_tasks
			WHERE data_source_id = OLD.id AND status IN ('queued', 'running'))
		BEGIN
			SELECT RAISE(ABORT, 'data source has active migration tasks');
		END`); err != nil {
		return fmt.Errorf("protect active migration data source deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_migration_audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		data_source_id TEXT NOT NULL,
		action TEXT NOT NULL,
		from_status TEXT NOT NULL DEFAULT '',
		to_status TEXT NOT NULL,
		actor TEXT NOT NULL DEFAULT 'local-api',
		request_source TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT '',
		occurred_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration audit event table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_migration_audit_task
		ON admin_migration_audit_events(task_id, occurred_at)`); err != nil {
		return fmt.Errorf("index migration audit events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_migration_task_progress (
		task_id TEXT PRIMARY KEY,
		current_version TEXT NOT NULL DEFAULT '',
		completed_steps INTEGER NOT NULL DEFAULT 0,
		total_steps INTEGER NOT NULL DEFAULT 0,
		current_action TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES admin_migration_tasks(id) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("create migration task progress table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_tenants (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		code TEXT NOT NULL COLLATE NOCASE UNIQUE,
		data_source_id TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		FOREIGN KEY(data_source_id) REFERENCES admin_data_sources(id)
	)`); err != nil {
		return fmt.Errorf("create tenant table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_tenants_datasource
		ON admin_tenants(data_source_id)`); err != nil {
		return fmt.Errorf("index tenant data source mapping: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_migration_task_tenants (
		task_id TEXT PRIMARY KEY,
		tenant_id TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES admin_migration_tasks(id) ON DELETE CASCADE,
		FOREIGN KEY(tenant_id) REFERENCES admin_tenants(id)
	)`); err != nil {
		return fmt.Errorf("create tenant migration task mapping: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_migration_task_tenants_tenant
		ON admin_migration_task_tenants(tenant_id)`); err != nil {
		return fmt.Errorf("index tenant migration task mapping: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_datasource_update_with_tenants
		BEFORE UPDATE OF driver, dsn_ciphertext ON admin_data_sources
		WHEN EXISTS (SELECT 1 FROM admin_tenants WHERE data_source_id = OLD.id)
		BEGIN
			SELECT RAISE(ABORT, 'data source is assigned to tenants');
		END`); err != nil {
		return fmt.Errorf("protect tenant data source configuration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_datasource_delete_with_tenants
		BEFORE DELETE ON admin_data_sources
		WHEN EXISTS (SELECT 1 FROM admin_tenants WHERE data_source_id = OLD.id)
		BEGIN
			SELECT RAISE(ABORT, 'data source is assigned to tenants');
		END`); err != nil {
		return fmt.Errorf("protect tenant data source deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_tenant_insert_with_active_migrations
		BEFORE INSERT ON admin_tenants
		WHEN EXISTS (SELECT 1 FROM admin_migration_tasks
			WHERE data_source_id = NEW.data_source_id AND status IN ('queued', 'running'))
		BEGIN
			SELECT RAISE(ABORT, 'tenant is used by an active migration task');
		END`); err != nil {
		return fmt.Errorf("protect tenant creation during migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_tenant_update_with_active_migrations
		BEFORE UPDATE ON admin_tenants
		WHEN EXISTS (SELECT 1 FROM admin_migration_tasks
			WHERE data_source_id IN (OLD.data_source_id, NEW.data_source_id)
			AND status IN ('queued', 'running'))
		BEGIN
			SELECT RAISE(ABORT, 'tenant is used by an active migration task');
		END`); err != nil {
		return fmt.Errorf("protect tenant updates during migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_tenant_delete_with_active_migrations
		BEFORE DELETE ON admin_tenants
		WHEN EXISTS (SELECT 1 FROM admin_migration_tasks
			WHERE data_source_id = OLD.data_source_id AND status IN ('queued', 'running'))
		BEGIN
			SELECT RAISE(ABORT, 'tenant is used by an active migration task');
		END`); err != nil {
		return fmt.Errorf("protect tenant deletion during migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_shard_rules (
		id TEXT PRIMARY KEY,
		data_source_id TEXT NOT NULL,
		base_table TEXT NOT NULL,
		shard_type TEXT NOT NULL,
		hash_shards INTEGER NOT NULL DEFAULT 16,
		range_size INTEGER NOT NULL DEFAULT 1000000,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(data_source_id, base_table),
		FOREIGN KEY(data_source_id) REFERENCES admin_data_sources(id)
	)`); err != nil {
		return fmt.Errorf("create shard rule table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_shard_rules_datasource
		ON admin_shard_rules(data_source_id)`); err != nil {
		return fmt.Errorf("index shard rules: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_datasource_driver_update_with_shard_rules
		BEFORE UPDATE OF driver, dsn_ciphertext ON admin_data_sources
		WHEN EXISTS (SELECT 1 FROM admin_shard_rules WHERE data_source_id = OLD.id)
			AND (NEW.driver != OLD.driver OR NEW.dsn_ciphertext != OLD.dsn_ciphertext)
		BEGIN
			SELECT RAISE(ABORT, 'data source is referenced by shard rules');
		END`); err != nil {
		return fmt.Errorf("protect shard rule data source driver: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_datasource_delete_with_shard_rules
		BEFORE DELETE ON admin_data_sources
		WHEN EXISTS (SELECT 1 FROM admin_shard_rules WHERE data_source_id = OLD.id)
		BEGIN
			SELECT RAISE(ABORT, 'data source is referenced by shard rules');
		END`); err != nil {
		return fmt.Errorf("protect shard rule data source deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_shard_ddl_tasks (
		id TEXT PRIMARY KEY,
		shard_rule_id TEXT NOT NULL,
		data_source_id TEXT NOT NULL,
		action TEXT NOT NULL,
		source_table TEXT NOT NULL,
		target_table TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','canceled','interrupted')),
		message TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT
	)`); err != nil {
		return fmt.Errorf("create shard DDL task table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_shard_ddl_tasks_created
		ON admin_shard_ddl_tasks(created_at DESC)`); err != nil {
		return fmt.Errorf("index shard DDL tasks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_shard_ddl_audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		shard_rule_id TEXT NOT NULL,
		data_source_id TEXT NOT NULL,
		action TEXT NOT NULL,
		from_status TEXT NOT NULL DEFAULT '',
		to_status TEXT NOT NULL,
		actor TEXT NOT NULL DEFAULT 'local-api',
		request_source TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT '',
		occurred_at TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES admin_shard_ddl_tasks(id) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("create shard DDL audit table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_shard_ddl_audit_task
		ON admin_shard_ddl_audit_events(task_id, occurred_at)`); err != nil {
		return fmt.Errorf("index shard DDL audit events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_shard_ddl_with_active_migrations
		BEFORE INSERT ON admin_shard_ddl_tasks
		WHEN EXISTS (SELECT 1 FROM admin_migration_tasks WHERE data_source_id = NEW.data_source_id AND status IN ('queued','running'))
		BEGIN
			SELECT RAISE(ABORT, 'data source has active migration tasks');
		END`); err != nil {
		return fmt.Errorf("serialize shard DDL with migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_migration_with_active_shard_ddl
		BEFORE INSERT ON admin_migration_tasks
		WHEN EXISTS (SELECT 1 FROM admin_shard_ddl_tasks WHERE data_source_id = NEW.data_source_id AND status IN ('queued','running'))
		BEGIN
			SELECT RAISE(ABORT, 'data source has active shard DDL tasks');
		END`); err != nil {
		return fmt.Errorf("serialize migrations with shard DDL: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_shard_rule_update_with_active_ddl
		BEFORE UPDATE ON admin_shard_rules
		WHEN EXISTS (SELECT 1 FROM admin_shard_ddl_tasks WHERE shard_rule_id = OLD.id AND status IN ('queued','running'))
		BEGIN
			SELECT RAISE(ABORT, 'shard rule has active DDL tasks');
		END`); err != nil {
		return fmt.Errorf("protect active shard DDL rule updates: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS prevent_shard_rule_delete_with_active_ddl
		BEFORE DELETE ON admin_shard_rules
		WHEN EXISTS (SELECT 1 FROM admin_shard_ddl_tasks WHERE shard_rule_id = OLD.id AND status IN ('queued','running'))
		BEGIN
			SELECT RAISE(ABORT, 'shard rule has active DDL tasks');
		END`); err != nil {
		return fmt.Errorf("protect active shard DDL rule deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_users (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL COLLATE NOCASE UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL CHECK(role IN ('administrator','viewer','migration_operator','shard_operator')),
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil { return fmt.Errorf("create administrator table: %w", err) }
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_sessions (
		token_hash BLOB PRIMARY KEY,
		admin_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		revoked_at TEXT,
		FOREIGN KEY(admin_id) REFERENCES admin_users(id) ON DELETE CASCADE
	)`); err != nil { return fmt.Errorf("create administrator session table: %w", err) }
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_sessions_admin ON admin_sessions(admin_id, expires_at)`); err != nil {
		return fmt.Errorf("index administrator sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_security_audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		actor TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL,
		outcome TEXT NOT NULL,
		request_source TEXT NOT NULL DEFAULT '',
		details TEXT NOT NULL DEFAULT '',
		occurred_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create security audit event table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_security_audit_time
		ON admin_security_audit_events(occurred_at DESC, id DESC)`); err != nil {
		return fmt.Errorf("index security audit events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO admin_schema_migrations (version, applied_at)
		VALUES (11, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record metadata schema version: %w", err)
	}
	return tx.Commit()
}

func (s *Store) encrypt(plain string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate encryption nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Store) decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) < s.aead.NonceSize() {
		return "", errors.New("stored data source credential is invalid")
	}
	nonce, encrypted := ciphertext[:s.aead.NonceSize()], ciphertext[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return "", errors.New("unable to decrypt stored data source credential")
	}
	return string(plain), nil
}

func validateInput(input DataSourceInput) error {
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 128 {
		return fmt.Errorf("%w: name must contain between 1 and 128 characters", ErrInvalidInput)
	}
	if strings.TrimSpace(input.DSN) == "" || len(input.DSN) > 4096 {
		return fmt.Errorf("%w: dsn must contain between 1 and 4096 characters", ErrInvalidInput)
	}
	switch input.Driver {
	case "mysql", "postgres", "sqlite", "clickhouse":
		return nil
	default:
		return fmt.Errorf("%w: driver must be one of mysql, postgres, sqlite, clickhouse", ErrInvalidInput)
	}
}

func newID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate data source ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// Create stores a data source after encrypting its DSN.
func (s *Store) Create(ctx context.Context, input DataSourceInput) (DataSource, error) {
	if err := validateInput(input); err != nil {
		return DataSource{}, err
	}
	encrypted, err := s.encrypt(input.DSN)
	if err != nil {
		return DataSource{}, err
	}
	id, err := newID()
	if err != nil {
		return DataSource{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO admin_data_sources
		(id, name, driver, dsn_ciphertext, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, strings.TrimSpace(input.Name), input.Driver, encrypted, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return DataSource{}, ErrConflict
		}
		return DataSource{}, fmt.Errorf("create data source: %w", err)
	}
	return DataSource{ID: id, Name: strings.TrimSpace(input.Name), Driver: input.Driver, CreatedAt: now, UpdatedAt: now}, nil
}

// List returns data source metadata without credentials.
func (s *Store) List(ctx context.Context) ([]DataSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, driver, created_at, updated_at
		FROM admin_data_sources ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list data sources: %w", err)
	}
	defer rows.Close()
	result := make([]DataSource, 0)
	for rows.Next() {
		var item DataSource
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &item.Driver, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan data source: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil { return nil, fmt.Errorf("parse data source creation time: %w", err) }
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil { return nil, fmt.Errorf("parse data source update time: %w", err) }
		result = append(result, item)
	}
	return result, rows.Err()
}

// Get returns safe data source metadata.
func (s *Store) Get(ctx context.Context, id string) (DataSource, error) {
	var item DataSource
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, driver, created_at, updated_at
		FROM admin_data_sources WHERE id = ?`, id).Scan(&item.ID, &item.Name, &item.Driver, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) { return DataSource{}, ErrNotFound }
	if err != nil { return DataSource{}, fmt.Errorf("get data source: %w", err) }
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil { return DataSource{}, fmt.Errorf("parse data source creation time: %w", err) }
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil { return DataSource{}, fmt.Errorf("parse data source update time: %w", err) }
	return item, nil
}

// ConnectionConfig decrypts a DSN for an internal connection-test or execution use case.
// Callers must never include the returned DSN in logs or API responses.
func (s *Store) ConnectionConfig(ctx context.Context, id string) (driver, dsn string, err error) {
	var encrypted []byte
	err = s.db.QueryRowContext(ctx, `SELECT driver, dsn_ciphertext FROM admin_data_sources WHERE id = ?`, id).Scan(&driver, &encrypted)
	if errors.Is(err, sql.ErrNoRows) { return "", "", ErrNotFound }
	if err != nil { return "", "", fmt.Errorf("load data source connection config: %w", err) }
	dsn, err = s.decrypt(encrypted)
	if err != nil { return "", "", err }
	return driver, dsn, nil
}

// Update replaces the name, driver, and encrypted DSN atomically.
func (s *Store) Update(ctx context.Context, id string, input DataSourceInput) (DataSource, error) {
	if err := validateInput(input); err != nil { return DataSource{}, err }
	var currentDriver string
	var currentCiphertext []byte
	err := s.db.QueryRowContext(ctx, "SELECT driver, dsn_ciphertext FROM admin_data_sources WHERE id = ?", id).
		Scan(&currentDriver, &currentCiphertext)
	if errors.Is(err, sql.ErrNoRows) { return DataSource{}, ErrNotFound }
	if err != nil { return DataSource{}, fmt.Errorf("read current data source configuration: %w", err) }

	encrypted := currentCiphertext
	currentDSN, err := s.decrypt(currentCiphertext)
	if err != nil { return DataSource{}, err }
	if currentDriver != input.Driver || currentDSN != input.DSN {
		encrypted, err = s.encrypt(input.DSN)
		if err != nil { return DataSource{}, err }
	}

	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE admin_data_sources SET name = ?, driver = ?,
		dsn_ciphertext = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(input.Name), input.Driver, encrypted, now.Format(time.RFC3339Nano), id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") { return DataSource{}, ErrConflict }
		if strings.Contains(strings.ToLower(err.Error()), ErrActiveTasks.Error()) { return DataSource{}, ErrActiveTasks }
		if strings.Contains(strings.ToLower(err.Error()), ErrDataSourceHasTenants.Error()) { return DataSource{}, ErrDataSourceHasTenants }
		if strings.Contains(strings.ToLower(err.Error()), ErrDataSourceHasShardRules.Error()) { return DataSource{}, ErrDataSourceHasShardRules }
		return DataSource{}, fmt.Errorf("update data source: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil { return DataSource{}, err }
	if count == 0 { return DataSource{}, ErrNotFound }
	return s.Get(ctx, id)
}

// Delete removes a data source registration. It does not modify the target database.
func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM admin_data_sources WHERE id = ?", id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), ErrActiveTasks.Error()) { return ErrActiveTasks }
		if strings.Contains(strings.ToLower(err.Error()), ErrDataSourceHasTenants.Error()) { return ErrDataSourceHasTenants }
		if strings.Contains(strings.ToLower(err.Error()), ErrDataSourceHasShardRules.Error()) { return ErrDataSourceHasShardRules }
		return fmt.Errorf("delete data source: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil { return err }
	if count == 0 { return ErrNotFound }
	return nil
}
