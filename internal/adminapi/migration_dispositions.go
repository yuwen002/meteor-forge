package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

const migrationDispositionConfirmation = "我已核对目标数据库实际结构并理解中断迁移风险"

type createMigrationTaskDispositionRequest struct {
	Action string `json:"action"`
	OperatorNote string `json:"operator_note"`
	Confirmation string `json:"confirmation"`
}

type migrationTaskDispositionStore interface {
	CreateMigrationTaskDisposition(context.Context, adminstore.MigrationTaskDisposition) (adminstore.MigrationTaskDisposition, error)
	GetMigrationTaskDisposition(context.Context, string) (adminstore.MigrationTaskDisposition, error)
}

func (s *Server) createMigrationTaskDisposition(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || s.tasks == nil { return }
	session, ok := adminSessionFromRequest(r)
	if !ok || session.Admin.Role != "administrator" {
		writeAPIError(w, http.StatusForbidden, "administrator_required", "only administrators may record migration task dispositions")
		return
	}
	store, ok := s.store.(migrationTaskDispositionStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disposition_store_unavailable", "migration task disposition storage is unavailable")
		return
	}
	var request createMigrationTaskDispositionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Action = strings.TrimSpace(request.Action)
	request.OperatorNote = strings.TrimSpace(request.OperatorNote)
	if request.Confirmation != migrationDispositionConfirmation {
		writeAPIError(w, http.StatusBadRequest, "confirmation_required", "the explicit migration-risk confirmation text is required")
		return
	}
	switch request.Action {
	case "confirm_completed", "authorize_retry", "terminate":
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_action", "action must be confirm_completed, authorize_retry, or terminate")
		return
	}
	if request.OperatorNote == "" || len(request.OperatorNote) > 2000 {
		writeAPIError(w, http.StatusBadRequest, "invalid_operator_note", "operator_note must contain 1 to 2000 characters")
		return
	}

	// Generate the reconciliation snapshot inside the server. Never accept a
	// client-supplied snapshot as evidence for a high-risk decision.
	taskID := r.PathValue("id")
	reconcileRequest := r.Clone(r.Context())
	reconcileRequest.SetPathValue("id", taskID)
	recorder := newResponseRecorder()
	s.reconcileMigrationTask(recorder, reconcileRequest)
	if recorder.status != http.StatusOK {
		if recorder.status == 0 { recorder.status = http.StatusInternalServerError }
		w.WriteHeader(recorder.status)
		_, _ = w.Write(recorder.body.Bytes())
		return
	}
	var snapshot migrationTaskReconciliation
	if err := json.Unmarshal(recorder.body.Bytes(), &snapshot); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "reconciliation_failed", "unable to decode server reconciliation result")
		return
	}
	if snapshot.Status != "interrupted" {
		writeAPIError(w, http.StatusConflict, "task_not_interrupted", "only interrupted migration tasks can receive a disposition")
		return
	}
	if request.Action != "terminate" && (!snapshot.HistoryAvailable ||
		snapshot.CurrentVersionState == "unknown" ||
		snapshot.CurrentVersionChecksum == "unknown" ||
		snapshot.CurrentVersionChecksum == "mismatch" ||
		snapshot.CurrentVersionChecksum == "local_script_missing") {
		writeAPIError(w, http.StatusConflict, "reconciliation_not_sufficient", "target migration history is unavailable or inconsistent; confirm completion and retry authorization are blocked")
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" { host = r.RemoteAddr }
	item, err := store.CreateMigrationTaskDisposition(r.Context(), adminstore.MigrationTaskDisposition{
		TaskID: taskID, Action: request.Action, Reconciliation: recorder.body.Bytes(),
		OperatorNote: request.OperatorNote, Actor: session.Admin.Username, RequestSource: host,
	})
	if err != nil {
		switch {
		case errors.Is(err, adminstore.ErrNotFound):
			writeAPIError(w, http.StatusNotFound, "not_found", "migration task not found")
		case errors.Is(err, adminstore.ErrMigrationTaskNotInterrupted):
			writeAPIError(w, http.StatusConflict, "task_not_interrupted", "only interrupted migration tasks can receive a disposition")
		case errors.Is(err, adminstore.ErrMigrationTaskDispositionExists):
			writeAPIError(w, http.StatusConflict, "disposition_already_exists", "this migration task already has a manual disposition")
		case errors.Is(err, adminstore.ErrInvalidInput):
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid migration task disposition")
		default:
			writeAPIError(w, http.StatusInternalServerError, "disposition_write_failed", "unable to record migration task disposition")
		}
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getMigrationTaskDisposition(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	store, ok := s.store.(migrationTaskDispositionStore)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "disposition_store_unavailable", "migration task disposition storage is unavailable")
		return
	}
	item, err := store.GetMigrationTaskDisposition(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, adminstore.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "not_found", "migration task disposition not found")
		} else {
			writeAPIError(w, http.StatusInternalServerError, "disposition_query_failed", "unable to read migration task disposition")
		}
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// responseRecorder captures the existing read-only reconciliation handler's
// response so the disposition endpoint stores the exact server-side snapshot.
type responseRecorder struct {
	header http.Header
	status int
	body *bytes.Buffer
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: make(http.Header), body: &bytes.Buffer{}}
}
func (r *responseRecorder) Header() http.Header { return r.header }
func (r *responseRecorder) WriteHeader(status int) { if r.status == 0 { r.status = status } }
func (r *responseRecorder) Write(p []byte) (int, error) { if r.status == 0 { r.status = http.StatusOK }; return r.body.Write(p) }
