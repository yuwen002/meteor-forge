package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/migration"
)

func TestInterruptedMigrationReconciliationIsReadOnly(t *testing.T) {
	for _, applied := range []bool{true, false} {
		name := "version-absent"
		if applied { name = "version-present" }
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			writeMigrationPair(t, dir, "000001_create_reconcile_items",
				"CREATE TABLE reconcile_items (id INTEGER PRIMARY KEY)",
				"DROP TABLE reconcile_items",
			)
			target := filepath.Join(t.TempDir(), "target.db")
			store, err := adminstore.Open(ctx, ":memory:", apiTestKey)
			if err != nil { t.Fatal(err) }
			t.Cleanup(func() { _ = store.Close() })
			source, err := store.Create(ctx, adminstore.DataSourceInput{
				Name: "reconciliation target", Driver: "sqlite", DSN: target,
			})
			if err != nil { t.Fatal(err) }
			server := newServerWithMigrationDir(store, dir, false)
			t.Cleanup(func() { _ = server.Close() })

			driver, err := server.migrationDriver(ctx, source.ID)
			if err != nil { t.Fatal(err) }
			if err := driver.EnsureTable(ctx); err != nil { t.Fatal(err) }
			loader := migration.NewFileSystemLoader(os.DirFS(dir), ".")
			scripts, err := loader.LoadScripts(ctx)
			if err != nil { t.Fatal(err) }
			if applied {
				if err := driver.ApplyMigration(ctx, scripts[0], false); err != nil { t.Fatal(err) }
			}

			task, err := store.CreateMigrationTask(ctx, adminstore.MigrationTask{
				ID: "reconcile-task", DataSourceID: source.ID, Action: "up",
			})
			if err != nil { t.Fatal(err) }
			started := time.Now().UTC()
			if err := store.UpdateMigrationTask(ctx, task.ID, "running", "running", &started, nil); err != nil { t.Fatal(err) }
			if err := store.UpdateMigrationTaskProgress(ctx, task.ID, adminstore.MigrationTaskProgress{
				CurrentVersion: "000001", CompletedSteps: 0, TotalSteps: 1, CurrentAction: "up",
			}); err != nil { t.Fatal(err) }
			if err := store.RecoverInterruptedTasks(ctx); err != nil { t.Fatal(err) }

			request := httptest.NewRequest(http.MethodGet, "/api/v1/migration-tasks/"+task.ID+"/reconciliation", nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("reconciliation status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response migrationTaskReconciliation
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil { t.Fatal(err) }
			if !response.HistoryAvailable || response.CurrentVersion != "000001" {
				t.Fatalf("unexpected reconciliation response: %+v", response)
			}
			wantState := "absent"
			if applied { wantState = "present" }
			if response.CurrentVersionState != wantState {
				t.Fatalf("current version state=%q, want %q", response.CurrentVersionState, wantState)
			}
			if applied && response.CurrentVersionChecksum != "matched" {
				t.Fatalf("checksum state=%q, want matched", response.CurrentVersionChecksum)
			}
			got, err := store.GetMigrationTask(ctx, task.ID)
			if err != nil { t.Fatal(err) }
			if got.Status != "interrupted" {
				t.Fatalf("reconciliation changed task status to %q", got.Status)
			}

			// Reconciliation is intentionally limited to interrupted tasks.
			queuedTask, err := store.CreateMigrationTask(ctx, adminstore.MigrationTask{
				ID: "reconcile-queued-task", DataSourceID: source.ID, Action: "up",
			})
			if err != nil {
				t.Fatal(err)
			}
			conflictRequest := httptest.NewRequest(http.MethodGet, "/api/v1/migration-tasks/"+queuedTask.ID+"/reconciliation", nil)
			conflictRecorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(conflictRecorder, conflictRequest)
			if conflictRecorder.Code != http.StatusConflict {
				t.Fatalf("queued task reconciliation status=%d body=%s", conflictRecorder.Code, conflictRecorder.Body.String())
			}
		})
	}
}
