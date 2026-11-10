package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestMigrationTaskDispositionRequiresAdministratorAndRecordsServerSnapshot(t *testing.T) {
	ctx := context.Background()
	target := filepath.Join(t.TempDir(), "target.db")
	store, err := adminstore.Open(ctx, ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	source, err := store.Create(ctx, adminstore.DataSourceInput{Name:"disposition target", Driver:"sqlite", DSN:target})
	if err != nil { t.Fatal(err) }
	server := newTestServerWithStore(store)
	defer server.Close()
	task, err := store.CreateMigrationTask(ctx, adminstore.MigrationTask{ID:"disposition-task",DataSourceID:source.ID,Action:"up"})
	if err != nil { t.Fatal(err) }
	started:=time.Now().UTC()
	if err:=store.UpdateMigrationTask(ctx,task.ID,"running","running",&started,nil);err!=nil{t.Fatal(err)}
	if err:=store.UpdateMigrationTaskProgress(ctx,task.ID,adminstore.MigrationTaskProgress{CurrentVersion:"000001",CurrentAction:"up"});err!=nil{t.Fatal(err)}
	if err:=store.RecoverInterruptedTasks(ctx);err!=nil{t.Fatal(err)}

	body,_:=json.Marshal(createMigrationTaskDispositionRequest{Action:"terminate",OperatorNote:"已检查目标库，决定终止并保留现场。",Confirmation:migrationDispositionConfirmation})
	req:=httptest.NewRequest(http.MethodPost,"/api/v1/migration-tasks/"+task.ID+"/disposition",bytes.NewReader(body))
	req.SetPathValue("id",task.ID)
	req=req.WithContext(contextWithAdmin(req.Context(),"migration_operator"))
	rec:=httptest.NewRecorder()
	server.createMigrationTaskDisposition(rec,req)
	if rec.Code!=http.StatusForbidden { t.Fatalf("migration_operator status=%d body=%s",rec.Code,rec.Body.String()) }

	req=httptest.NewRequest(http.MethodPost,"/api/v1/migration-tasks/"+task.ID+"/disposition",bytes.NewReader(body))
	req.SetPathValue("id",task.ID)
	req=req.WithContext(contextWithAdmin(req.Context(),"administrator"))
	rec=httptest.NewRecorder()
	server.createMigrationTaskDisposition(rec,req)
	if rec.Code!=http.StatusCreated { t.Fatalf("administrator status=%d body=%s",rec.Code,rec.Body.String()) }
	var saved adminstore.MigrationTaskDisposition
	if err:=json.Unmarshal(rec.Body.Bytes(),&saved);err!=nil{t.Fatal(err)}
	if saved.Action!="terminate" || saved.Actor=="" || len(saved.Reconciliation)==0 { t.Fatalf("unexpected disposition: %+v",saved) }
	var snapshot migrationTaskReconciliation
	if err:=json.Unmarshal(saved.Reconciliation,&snapshot);err!=nil{t.Fatal(err)}
	if snapshot.TaskID!=task.ID || snapshot.Status!="interrupted" { t.Fatalf("unexpected server snapshot: %+v",snapshot) }
	got,err:=store.GetMigrationTask(ctx,task.ID)
	if err!=nil{t.Fatal(err)}
	if got.Status!="interrupted" { t.Fatalf("disposition changed task status to %q",got.Status) }

	req=httptest.NewRequest(http.MethodPost,"/api/v1/migration-tasks/"+task.ID+"/disposition",bytes.NewReader(body))
	req.SetPathValue("id",task.ID)
	req=req.WithContext(contextWithAdmin(req.Context(),"administrator"))
	rec=httptest.NewRecorder()
	server.createMigrationTaskDisposition(rec,req)
	if rec.Code!=http.StatusConflict { t.Fatalf("duplicate status=%d body=%s",rec.Code,rec.Body.String()) }
}

func contextWithAdmin(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, adminContextKey{}, adminstore.AdminSession{Admin:adminstore.Admin{Username:"test-admin",Role:role}})
}
