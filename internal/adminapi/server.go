// Package adminapi exposes the HTTP boundary for the meteor-forge management
// plane. Business operations are added through services in later milestones;
// core migration, tenant, and sharding packages remain HTTP-agnostic.
package adminapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

const apiPrefix = "/api/v1"

// Server owns the management API routes and middleware.
type Server struct {
	handler http.Handler
	store   dataSourceStore
}

// NewServer builds the API without a metadata store, useful for health-only embedding.
func NewServer() *Server {
	return NewServerWithStore(nil)
}

// NewServerWithStore builds an API server backed by the management metadata store.
func NewServerWithStore(store *adminstore.Store) *Server {
	mux := http.NewServeMux()
	server := &Server{store: store}
	mux.HandleFunc("GET "+apiPrefix+"/health", healthHandler)
	server.registerDataSourceRoutes(mux)
	server.handler = withCommonHeaders(mux)
	return server
}

// Handler returns the HTTP handler. It is exposed to support embedding and tests.
func (s *Server) Handler() http.Handler { return s.handler }

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "service": "meteor-forge-admin", "api": "v1",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
