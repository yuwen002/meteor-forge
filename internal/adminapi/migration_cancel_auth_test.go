package adminapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestMigrationOperatorCanOnlyCancelUpTasks(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.BootstrapAdmin(context.Background(), "root", "a-strong-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAdminUser(context.Background(), "operator", "a-operator-password-123", "migration_operator"); err != nil {
		t.Fatal(err)
	}
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "cancel authorization target", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil {
		t.Fatal(err)
	}

	server := NewServerWithMigrationDir(store, t.TempDir())
	defer server.Close()

	login := func(username, password string) *http.Cookie {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/auth/login",
			bytes.NewBufferString(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("login %q status=%d body=%s", username, rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}
	operatorCookie := login("operator", "a-operator-password-123")
	adminCookie := login("root", "a-strong-password-123")

	addActiveTask := func(id, action string) <-chan struct{} {
		t.Helper()
		_, err := store.CreateMigrationTask(context.Background(), adminstore.MigrationTask{
			ID: id, DataSourceID: source.ID, Action: action,
		})
		if err != nil {
			t.Fatalf("create %s task: %v", action, err)
		}
		canceled := make(chan struct{})
		var once sync.Once
		server.tasks.mu.Lock()
		server.tasks.cancels[id] = func() { once.Do(func() { close(canceled) }) }
		server.tasks.mu.Unlock()
		return canceled
	}
	cancel := func(id string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/migration-tasks/"+id+"/cancel", nil)
		req.Header.Set("Origin", "http://localhost")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}
	assertNotCanceled := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
			t.Fatal("unauthorized cancellation reached the task manager")
		default:
		}
	}
	assertCanceled := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		default:
			t.Fatal("authorized cancellation did not reach the task manager")
		}
	}

	for i, action := range []string{"down", "down_steps", "to_version"} {
		id := fmt.Sprintf("rollback-%d", i)
		canceled := addActiveTask(id, action)
		rec := cancel(id, operatorCookie)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("operator cancel %s status=%d body=%s", action, rec.Code, rec.Body.String())
		}
		assertNotCanceled(canceled)
	}

	upCanceled := addActiveTask("migration-up", "up")
	if rec := cancel("migration-up", operatorCookie); rec.Code != http.StatusAccepted {
		t.Fatalf("operator cancel up status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertCanceled(upCanceled)

	adminCanceled := addActiveTask("admin-rollback", "down")
	if rec := cancel("admin-rollback", adminCookie); rec.Code != http.StatusAccepted {
		t.Fatalf("administrator cancel rollback status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertCanceled(adminCanceled)
}
