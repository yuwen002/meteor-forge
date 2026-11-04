package adminapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestMigrationTaskUpAndConfirmedDown(t *testing.T) {
	dir := t.TempDir()
	writeMigrationPair(t, dir, "000001_create_task_items",
		"CREATE TABLE task_items (id INTEGER PRIMARY KEY)",
		"DROP TABLE task_items",
	)
	target := filepath.Join(t.TempDir(), "target.db")
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "task target", Driver: "sqlite", DSN: target,
	})
	if err != nil { t.Fatal(err) }
	server := NewServerWithMigrationDir(store, dir)
	t.Cleanup(func() { _ = server.Close() })

	up := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+source.ID+"/migrations/tasks", strings.NewReader(`{"action":"up"}`))
	up.Header.Set("Content-Type", "application/json")
	upRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(upRec, up)
	if upRec.Code != http.StatusAccepted { t.Fatalf("submit up status=%d body=%s", upRec.Code, upRec.Body.String()) }
	var created struct { ID string `json:"id"` }
	if err := json.Unmarshal(upRec.Body.Bytes(), &created); err != nil { t.Fatal(err) }
	waitForTaskStatus(t, store, created.ID, "succeeded")

	db, err := sql.Open("sqlite", target)
	if err != nil { t.Fatal(err) }
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_items'").Scan(&count); err != nil { t.Fatal(err) }
	if count != 1 { t.Fatal("Up task did not create target table") }
	_ = db.Close()

	downWithoutConfirmation := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+source.ID+"/migrations/tasks", strings.NewReader(`{"action":"down"}`))
	downWithoutConfirmation.Header.Set("Content-Type", "application/json")
	downRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(downRec, downWithoutConfirmation)
	if downRec.Code != http.StatusBadRequest { t.Fatalf("unconfirmed Down status=%d body=%s", downRec.Code, downRec.Body.String()) }

	down := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+source.ID+"/migrations/tasks", strings.NewReader(`{"action":"down","confirm":true}`))
	down.Header.Set("Content-Type", "application/json")
	downRec = httptest.NewRecorder()
	server.Handler().ServeHTTP(downRec, down)
	if downRec.Code != http.StatusAccepted { t.Fatalf("submit down status=%d body=%s", downRec.Code, downRec.Body.String()) }
	created.ID = ""
	if err := json.Unmarshal(downRec.Body.Bytes(), &created); err != nil { t.Fatal(err) }
	waitForTaskStatus(t, store, created.ID, "succeeded")

	db, err = sql.Open("sqlite", target)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_items'").Scan(&count); err != nil { t.Fatal(err) }
	if count != 0 { t.Fatal("Down task did not remove target table") }
}

func waitForTaskStatus(t *testing.T, store *adminstore.Store, id, expected string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.GetMigrationTask(context.Background(), id)
		if err == nil && task.Status == expected { return }
		if err == nil && (task.Status == "failed" || task.Status == "canceled" || task.Status == "interrupted") {
			t.Fatalf("task ended unexpectedly: %+v", task)
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _ := store.GetMigrationTask(context.Background(), id)
	t.Fatalf("task did not reach %q: %+v", expected, task)
}
