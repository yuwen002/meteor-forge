package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestShardRuleManagementAndRoutePreviewAPI(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "sharding api db", Driver: "sqlite", DSN: ":memory:",
	})
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

	body := `{"data_source_id":"` + source.ID + `","base_table":"orders","shard_type":"month"}`
	created := request(http.MethodPost, "/api/v1/shard-rules", body)
	if created.Code != http.StatusCreated { t.Fatalf("create status=%d body=%s", created.Code, created.Body.String()) }
	var rule adminstore.ShardRule
	if err := json.Unmarshal(created.Body.Bytes(), &rule); err != nil { t.Fatal(err) }
	if rule.ID == "" || rule.BaseTable != "orders" || rule.ShardType != "month" {
		t.Fatalf("unexpected shard rule: %+v", rule)
	}
	if strings.Contains(strings.ToLower(created.Body.String()), "dsn") {
		t.Fatalf("shard rule response must not contain DSN: %s", created.Body.String())
	}

	preview := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/preview",
		`{"timestamp":"2026-11-05T09:00:00+08:00"}`)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "orders_202611") {
		t.Fatalf("time preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	rangePreview := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/preview",
		`{"start_time":"2026-10-01T00:00:00Z","end_time":"2026-11-30T23:59:59Z"}`)
	if rangePreview.Code != http.StatusOK || !strings.Contains(rangePreview.Body.String(), "orders_202610") ||
		!strings.Contains(rangePreview.Body.String(), "orders_202611") {
		t.Fatalf("time range preview status=%d body=%s", rangePreview.Code, rangePreview.Body.String())
	}
	tooLarge := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/preview",
		`{"start_time":"2000-01-01T00:00:00Z","end_time":"2026-01-01T00:00:00Z"}`)
	if tooLarge.Code != http.StatusBadRequest || !strings.Contains(tooLarge.Body.String(), "range_too_large") {
		t.Fatalf("oversized time range status=%d body=%s", tooLarge.Code, tooLarge.Body.String())
	}

	updated := request(http.MethodPut, "/api/v1/shard-rules/"+rule.ID,
		`{"data_source_id":"`+source.ID+`","base_table":"orders","shard_type":"id_hash","hash_shards":8}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"hash_shards":8`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	idPreview := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/preview", `{"id":12345}`)
	if idPreview.Code != http.StatusOK || !strings.Contains(idPreview.Body.String(), "orders_") {
		t.Fatalf("ID preview status=%d body=%s", idPreview.Code, idPreview.Body.String())
	}
	invalid := request(http.MethodPost, "/api/v1/shard-rules/"+rule.ID+"/preview", `{"id":-1}`)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "invalid_id") {
		t.Fatalf("invalid ID preview status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	listed := request(http.MethodGet, "/api/v1/shard-rules", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), rule.ID) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	removed := request(http.MethodDelete, "/api/v1/shard-rules/"+rule.ID, "")
	if removed.Code != http.StatusNoContent { t.Fatalf("delete status=%d body=%s", removed.Code, removed.Body.String()) }
}
