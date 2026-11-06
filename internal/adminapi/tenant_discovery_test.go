package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	_ "modernc.org/sqlite"
)

func TestTenantDiscoveryPreviewAndConfirmedSync(t *testing.T) {
	ctx:=context.Background()
	controlPath:=filepath.Join(t.TempDir(),"control.db")
	controlDB,err:=sql.Open("sqlite",controlPath)
	if err!=nil { t.Fatal(err) }
	if _,err:=controlDB.Exec(`CREATE TABLE t_tenants (
		tenant_id TEXT PRIMARY KEY, dsn TEXT NOT NULL, status TEXT NOT NULL, driver TEXT
	)`);err!=nil { t.Fatal(err) }
	const secretDSN="mysql://user:very-secret-password@db.example/tenant_a"
	if _,err:=controlDB.Exec("INSERT INTO t_tenants (tenant_id,dsn,status,driver) VALUES (?,?,?,?)","tenant-a",secretDSN,"active","sqlite");err!=nil { t.Fatal(err) }
	if _,err:=controlDB.Exec("INSERT INTO t_tenants (tenant_id,dsn,status,driver) VALUES (?,?,?,?)","tenant-paused","mysql://hidden","suspended","sqlite");err!=nil { t.Fatal(err) }
	if err:=controlDB.Close();err!=nil { t.Fatal(err) }

	store,err:=adminstore.Open(ctx,":memory:",apiTestKey)
	if err!=nil { t.Fatal(err) }
	defer store.Close()
	controlSource,err:=store.Create(ctx,adminstore.DataSourceInput{Name:"control",Driver:"sqlite",DSN:controlPath})
	if err!=nil { t.Fatal(err) }
	targetSource,err:=store.Create(ctx,adminstore.DataSourceInput{Name:"target",Driver:"sqlite",DSN:":memory:"})
	if err!=nil { t.Fatal(err) }
	server:=newTestServerWithStore(store)
	defer server.Close()

	request:=func(method,path,body string)*httptest.ResponseRecorder{
		req:=httptest.NewRequest(method,path,strings.NewReader(body))
		req.Header.Set("Content-Type","application/json")
		rec:=httptest.NewRecorder()
		server.Handler().ServeHTTP(rec,req)
		return rec
	}
	preview:=request(http.MethodGet,"/api/v1/tenant-discovery/preview?data_source_id="+controlSource.ID,"")
	if preview.Code!=http.StatusOK { t.Fatalf("preview status=%d body=%s",preview.Code,preview.Body.String()) }
	if strings.Contains(preview.Body.String(),secretDSN) || strings.Contains(preview.Body.String(),"very-secret-password") {
		t.Fatalf("preview leaked control tenant DSN: %s",preview.Body.String())
	}
	var previewBody struct { Count int `json:"count"`; Items []tenantDiscoveryItem `json:"items"` }
	if err:=json.Unmarshal(preview.Body.Bytes(),&previewBody);err!=nil { t.Fatal(err) }
	if previewBody.Count!=1 || len(previewBody.Items)!=1 || previewBody.Items[0].ControlTenantID!="tenant-a" {
		t.Fatalf("unexpected discovery preview: %+v",previewBody)
	}
	notConfirmed:=request(http.MethodPost,"/api/v1/tenant-discovery/sync",`{"control_data_source_id":"`+controlSource.ID+`","items":[]}`)
	if notConfirmed.Code!=http.StatusBadRequest || !strings.Contains(notConfirmed.Body.String(),"confirmation_required") {
		t.Fatalf("unconfirmed sync status=%d body=%s",notConfirmed.Code,notConfirmed.Body.String())
	}
	body:=`{"control_data_source_id":"`+controlSource.ID+`","confirm":true,"items":[{"control_tenant_id":"tenant-a","name":"Tenant A","code":"tenant-a","data_source_id":"`+targetSource.ID+`"}]}`
	synced:=request(http.MethodPost,"/api/v1/tenant-discovery/sync",body)
	if synced.Code!=http.StatusCreated { t.Fatalf("sync status=%d body=%s",synced.Code,synced.Body.String()) }
	var result struct { Count int `json:"count"`; Items []adminstore.Tenant `json:"items"` }
	if err:=json.Unmarshal(synced.Body.Bytes(),&result);err!=nil { t.Fatal(err) }
	if result.Count!=1 || len(result.Items)!=1 || result.Items[0].Code!="tenant-a" { t.Fatalf("unexpected sync result: %+v",result) }
	audit,total,err:=store.ListTenantAuditEvents(ctx,result.Items[0].ID,20,0)
	if err!=nil { t.Fatal(err) }
	if total!=1 || len(audit)!=1 || audit[0].RequestSource=="" { t.Fatalf("discovery audit missing: %+v",audit) }
	if _,err:=os.Stat(controlPath);err!=nil { t.Fatal(err) }
}
