package adminstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatalf("Open() error = %v", err) }
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestDataSourceCRUDAndCredentialEncryption(t *testing.T) {
	store := newTestStore(t)
	input := DataSourceInput{Name: "local mysql", Driver: "mysql", DSN: "user:secret@tcp(localhost:3306)/app"}
	created, err := store.Create(context.Background(), input)
	if err != nil { t.Fatalf("Create() error = %v", err) }
	if created.ID == "" || created.Name != input.Name { t.Fatalf("unexpected created data source: %+v", created) }

	list, err := store.List(context.Background())
	if err != nil { t.Fatalf("List() error = %v", err) }
	if len(list) != 1 || list[0].ID != created.ID { t.Fatalf("unexpected list: %+v", list) }

	var stored []byte
	if err := store.db.QueryRowContext(context.Background(), "SELECT dsn_ciphertext FROM admin_data_sources WHERE id = ?", created.ID).Scan(&stored); err != nil { t.Fatalf("read stored ciphertext: %v", err) }
	if strings.Contains(string(stored), input.DSN) || strings.Contains(string(stored), "secret") { t.Fatal("DSN appears in plaintext in metadata database") }

	driver, dsn, err := store.ConnectionConfig(context.Background(), created.ID)
	if err != nil { t.Fatalf("ConnectionConfig() error = %v", err) }
	if driver != input.Driver || dsn != input.DSN { t.Fatalf("decrypted config mismatch: driver=%q dsn=%q", driver, dsn) }

	updated, err := store.Update(context.Background(), created.ID, DataSourceInput{
		Name: "renamed mysql", Driver: "mysql", DSN: "new-secret",
	})
	if err != nil { t.Fatalf("Update() error = %v", err) }
	if updated.Name != "renamed mysql" { t.Fatalf("updated name = %q", updated.Name) }

	if err := store.Delete(context.Background(), created.ID); err != nil { t.Fatalf("Delete() error = %v", err) }
	if _, err := store.Get(context.Background(), created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func TestDataSourceValidationAndDuplicateName(t *testing.T) {
	store := newTestStore(t)
	invalid := []DataSourceInput{
		{Name: "", Driver: "mysql", DSN: "dsn"},
		{Name: "bad driver", Driver: "oracle", DSN: "dsn"},
		{Name: "missing dsn", Driver: "mysql"},
	}
	for _, input := range invalid {
		if _, err := store.Create(context.Background(), input); err == nil {
			t.Fatalf("Create(%+v) expected validation error", input)
		}
	}
	input := DataSourceInput{Name: "primary", Driver: "sqlite", DSN: ":memory:"}
	if _, err := store.Create(context.Background(), input); err != nil { t.Fatal(err) }
	if _, err := store.Create(context.Background(), input); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate Create() error = %v, want ErrConflict", err)
	}
}

func TestOpenRejectsInvalidEncryptionKey(t *testing.T) {
	if _, err := Open(context.Background(), ":memory:", "short"); err == nil {
		t.Fatal("Open() expected invalid encryption key error")
	}
}
