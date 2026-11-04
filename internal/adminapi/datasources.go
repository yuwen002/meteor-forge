package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type dataSourceStore interface {
	Create(context.Context, adminstore.DataSourceInput) (adminstore.DataSource, error)
	List(context.Context) ([]adminstore.DataSource, error)
	Get(context.Context, string) (adminstore.DataSource, error)
	ConnectionConfig(context.Context, string) (string, string, error)
	Update(context.Context, string, adminstore.DataSourceInput) (adminstore.DataSource, error)
	Delete(context.Context, string) error
}

func (s *Server) registerDataSourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/datasources", s.listDataSources)
	mux.HandleFunc("POST "+apiPrefix+"/datasources", s.createDataSource)
	mux.HandleFunc("GET "+apiPrefix+"/datasources/{id}", s.getDataSource)
	mux.HandleFunc("PUT "+apiPrefix+"/datasources/{id}", s.updateDataSource)
	mux.HandleFunc("DELETE "+apiPrefix+"/datasources/{id}", s.deleteDataSource)
	mux.HandleFunc("POST "+apiPrefix+"/datasources/{id}/test", s.testDataSource)
}

func (s *Server) listDataSources(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	items, err := s.store.List(r.Context())
	if err != nil { writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to list data sources"); return }
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	var input adminstore.DataSourceInput
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }
	item, err := s.store.Create(r.Context(), input)
	if err != nil { writeStoreError(w, err); return }
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	item, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil { writeStoreError(w, err); return }
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	var input adminstore.DataSourceInput
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }
	id := r.PathValue("id")
	item, err := s.store.Update(r.Context(), id, input)
	if err != nil { writeStoreError(w, err); return }
	s.pools.Invalidate(id)
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	id := r.PathValue("id")
	if err := s.store.Delete(r.Context(), id); err != nil { writeStoreError(w, err); return }
	s.pools.Invalidate(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	db, err := s.pools.Get(ctx, id, func(ctx context.Context, id string) (string, string, error) {
		driver, dsn, err := s.store.ConnectionConfig(ctx, id)
		if err != nil { return "", "", err }
		driverName := map[string]string{"mysql":"mysql", "postgres":"pgx", "sqlite":"sqlite", "clickhouse":"clickhouse"}[driver]
		if driverName == "" { return "", "", errors.New("unsupported database driver") }
		return driverName, dsn, nil
	})
	if err == nil { err = db.PingContext(ctx) }
	if err != nil { s.pools.Invalidate(id) }
	if err != nil {
		// Do not return the driver's raw error: it may contain host or credential details.
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "message": "connection failed; verify the endpoint and credentials"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "connection successful"})
}

func (s *Server) requireStore(w http.ResponseWriter) bool {
	if s.store != nil { return true }
	writeAPIError(w, http.StatusServiceUnavailable, "metadata_unavailable", "management metadata store is not configured")
	return false
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil { return fmt.Errorf("invalid JSON body: %w", err) }
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminstore.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "data source not found")
	case errors.Is(err, adminstore.ErrConflict):
		writeAPIError(w, http.StatusConflict, "conflict", "a data source with this name already exists")
	case errors.Is(err, adminstore.ErrInvalidInput):
		writeAPIError(w, http.StatusBadRequest, "invalid_data_source", safeValidationMessage(err))
	default:
		// Store errors are deliberately not returned verbatim to avoid leaking DSNs.
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to process data source request")
	}
}

func safeValidationMessage(err error) string {
	message := err.Error()
	for _, secretMarker := range []string{"password=", "://", "@tcp(", "@"} {
		if strings.Contains(message, secretMarker) {
			return "invalid data source configuration"
		}
	}
	if strings.Contains(message, "must ") || strings.Contains(message, "driver ") {
		return message
	}
	return "unable to process data source request"
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message})
}
