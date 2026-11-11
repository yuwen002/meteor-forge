package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"\n\t"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"\n\t"github.com/yuwen002/meteor-forge/pkg/migration"
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id",task.ID)
	req=req.WithContext(contextWithAdmin(req.Context(),"migration_operator"))
	rec:=httptest.NewRecorder()
	server.createMigrationTaskDisposition(rec,req)
	if rec.Code!=http.StatusForbidden { t.Fatalf("migration_operator status=%d body=%s",rec.Code,rec.Body.String()) }

	req=httptest.NewRequest(http.MethodPost,"/api/v1/migration-tasks/"+task.ID+"/disposition",bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id",task.ID)
	req=req.WithContext(contextWithAdmin(req.Context(),"administrator"))
	rec=httptest.NewRecorder()
	server.createMigrationTaskDisposition(rec,req)
	if rec.Code!=http.StatusConflict { t.Fatalf("duplicate status=%d body=%s",rec.Code,rec.Body.String()) }
}

func contextWithAdmin(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, adminContextKey{}, adminstore.AdminSession{Admin:adminstore.Admin{Username:"test-admin",Role:role}})
}


func TestMigrationDispositionReconciliationEvidenceRules(t *testing.T) {
	tests := []struct {
		name string
		action string
		snapshot migrationTaskReconciliation
		want bool
	}{
		{name: "确认完成要求版本存在且校验和匹配", action: "confirm_completed", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "present", CurrentVersionChecksum: "matched"}, want: true},
		{name: "兼容旧历史的未校验记录", action: "confirm_completed", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "present", CurrentVersionChecksum: "legacy_unverified"}, want: true},
		{name: "版本不存在不能确认完成", action: "confirm_completed", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "absent", CurrentVersionChecksum: "not_comparable"}, want: false},
		{name: "校验和不一致不能确认完成", action: "confirm_completed", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "present", CurrentVersionChecksum: "mismatch"}, want: false},
		{name: "授权重试要求版本不存在", action: "authorize_retry", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "absent", CurrentVersionChecksum: "not_comparable"}, want: true},
		{name: "版本存在不能授权重试", action: "authorize_retry", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "present", CurrentVersionChecksum: "matched"}, want: false},
		{name: "缺少本地脚本不能授权重试", action: "authorize_retry", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "absent", CurrentVersionChecksum: "local_script_missing"}, want: false},
		{name: "历史不可读不能执行高风险处置", action: "authorize_retry", snapshot: migrationTaskReconciliation{HistoryAvailable: false, CurrentVersionState: "unknown", CurrentVersionChecksum: "unknown"}, want: false},
		{name: "未知动作拒绝", action: "unexpected", snapshot: migrationTaskReconciliation{HistoryAvailable: true, CurrentVersionState: "present", CurrentVersionChecksum: "matched"}, want: false},
		{name: "终止不依赖核对", action: "terminate", snapshot: migrationTaskReconciliation{}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := migrationDispositionReconciliationAllows(tt.action, tt.snapshot); got != tt.want {
				t.Fatalf("migrationDispositionReconciliationAllows(%q, %+v) = %v, want %v", tt.action, tt.snapshot, got, tt.want)
			}
		})
	}
}
