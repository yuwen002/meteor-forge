package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestShardCloneTaskAPI(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "target.db")
	targetDB, err := sql.Open("sqlite", targetPath)
	if err != nil { t.Fatal(err) }
	if _, err := targetDB.Exec("CREATE TABLE orders (id INTEGER, customer TEXT)"); err != nil { t.Fatal(err) }
	if _, err := targetDB.Exec("INSERT INTO orders (id, customer) VALUES (1, 'must not be copied')"); err != nil { t.Fatal(err) }
	if err := targetDB.Close(); err != nil { t.Fatal(err) }

	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{Name: "clone target", Driver: "sqlite", DSN: targetPath})
	if err != nil { t.Fatal(err) }
	server := NewServerWithStore(store)
	t.Cleanup(func() { _ = server.Close() })

	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}
	createdRule := request(http.MethodPost, "/api/v1/shard-rules",
		`{"data_source_id":"`+source.ID+`","base_table":"orders","shard_type":"month"}`)
	if createdRule.Code != http.StatusCreated { t.Fatalf("create rule status=%d body=%s", createdRule.Code, createdRule.Body.String()) }
	var rule adminstore.ShardRule
	if err := json.Unmarshal(createdRule.Body.Bytes(), &rule); err != nil { t.Fatal(err) }

	preview := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/clone-preview",
		`{"source_table":"orders","target_table":"orders_202611"}`)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "CREATE TABLE") || !strings.Contains(preview.Body.String(), "copies_data") {
		t.Fatalf("clone preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	missingConfirmation := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/clone-tasks",
		`{"source_table":"orders","target_table":"orders_202611"}`)
	if missingConfirmation.Code != http.StatusBadRequest || !strings.Contains(missingConfirmation.Body.String(), "confirmation_required") {
		t.Fatalf("missing confirmation status=%d body=%s", missingConfirmation.Code, missingConfirmation.Body.String())
	}

	submitted := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/clone-tasks",
		`{"source_table":"orders","target_table":"orders_202611","confirm":true}`)
	if submitted.Code != http.StatusAccepted { t.Fatalf("submit clone task status=%d body=%s", submitted.Code, submitted.Body.String()) }
	var task adminstore.ShardDDLTask
	if err := json.Unmarshal(submitted.Body.Bytes(), &task); err != nil { t.Fatal(err) }
	if task.ID == "" || task.Status != "queued" { t.Fatalf("unexpected queued task: %+v", task) }

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := request(http.MethodGet, "/api/v1/shard-ddl-tasks/"+task.ID, "")
		if result.Code != http.StatusOK { t.Fatalf("get task status=%d body=%s", result.Code, result.Body.String()) }
		if err := json.Unmarshal(result.Body.Bytes(), &task); err != nil { t.Fatal(err) }
		if task.Status == "succeeded" || task.Status == "failed" { break }
		time.Sleep(10 * time.Millisecond)
	}
	if task.Status != "succeeded" { t.Fatalf("clone task did not succeed: %+v", task) }

	checkDB, err := sql.Open("sqlite", targetPath)
	if err != nil { t.Fatal(err) }
	defer checkDB.Close()
	var columnCount int
	if err := checkDB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('orders_202611')").Scan(&columnCount); err != nil { t.Fatal(err) }
	if columnCount != 2 { t.Fatalf("cloned column count=%d, want 2", columnCount) }
	var copiedRows int
	if err := checkDB.QueryRow("SELECT COUNT(*) FROM orders_202611").Scan(&copiedRows); err != nil { t.Fatal(err) }
	if copiedRows != 0 { t.Fatalf("clone copied %d source rows; expected structure-only clone", copiedRows) }

	audit := request(http.MethodGet, "/api/v1/shard-ddl-tasks/"+task.ID+"/audit", "")
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), `"to_status":"queued"`) || !strings.Contains(audit.Body.String(), `"to_status":"succeeded"`) {
		t.Fatalf("task audit status=%d body=%s", audit.Code, audit.Body.String())
	}
	list := request(http.MethodGet, "/api/v1/shard-ddl-tasks", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), task.ID) {
		t.Fatalf("task list status=%d body=%s", list.Code, list.Body.String())
	}
	duplicate := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/clone-preview",
		`{"source_table":"orders","target_table":"orders_202611"}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "clone_target_exists") {
		t.Fatalf("duplicate target preview status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}
