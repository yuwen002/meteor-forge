// Package adminapi exposes the HTTP boundary for the meteor-forge management
// plane. Business operations are added through services in later milestones;
// core migration, tenant, and sharding packages remain HTTP-agnostic.
package adminapi

import (
	"encoding/json"
	"net/http"
	"time"
)

const apiPrefix = "/api/v1"

// Server owns the management API routes and HTTP middleware.
type Server struct {
	handler http.Handler
}

// NewServer builds an API server with safe defaults and no implicit database
// connections. The management service binds to loopback by default in cmd/server.
func NewServer() *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apiPrefix+"/health", healthHandler)
	return &Server{
		handler: withCommonHeaders(mux),
	}
}

// Handler returns the HTTP handler. It is exposed to support embedding and tests.
func (s *Server) Handler() http.Handler {
	return s.handler
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"service":   "meteor-forge-admin",
		"api":       "v1",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
