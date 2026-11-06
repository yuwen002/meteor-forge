package adminapi

import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"

 "github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestAuthenticationProtectsManagementRoutesAndSupportsLogout(t *testing.T){
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)}
 defer store.Close()
 if _,err=store.BootstrapAdmin(context.Background(),"admin","a-strong-password-123");err!=nil{t.Fatal(err)}
 server:=NewServerWithStore(store);defer server.Close()
 call:=func(method,path string,body []byte,cookie *http.Cookie,origin string)*httptest.ResponseRecorder{
  req:=httptest.NewRequest(method,"http://localhost"+path,bytes.NewReader(body))
  if len(body)>0{req.Header.Set("Content-Type","application/json")}
  if cookie!=nil{req.AddCookie(cookie)}
  if origin!=""{req.Header.Set("Origin",origin)}
  rec:=httptest.NewRecorder();server.Handler().ServeHTTP(rec,req);return rec
 }
 if got:=call(http.MethodGet,"/api/v1/datasources",nil,nil,"");got.Code!=http.StatusUnauthorized{t.Fatalf("anonymous data source status=%d",got.Code)}
 login:=call(http.MethodPost,"/api/v1/auth/login",[]byte(`{"username":"admin","password":"a-strong-password-123"}`),nil,"")
 if login.Code!=http.StatusOK{t.Fatalf("login status=%d body=%s",login.Code,login.Body.String())}
 cookie:=login.Result().Cookies()[0]
 if cookie.Name!=adminSessionCookie||!cookie.HttpOnly||cookie.SameSite!=http.SameSiteStrictMode||cookie.Path!=apiPrefix{t.Fatalf("unsafe session cookie: %+v",cookie)}
 if len(cookie.Value)<40{t.Fatal("session token missing")}
 session:=call(http.MethodGet,"/api/v1/auth/session",nil,cookie,"")
 if session.Code!=http.StatusOK{t.Fatalf("session status=%d body=%s",session.Code,session.Body.String())}
 if got:=call(http.MethodPost,"/api/v1/auth/logout",[]byte(`{}`),cookie,"http://localhost");got.Code!=http.StatusNoContent{t.Fatalf("logout status=%d body=%s",got.Code,got.Body.String())}
 if got:=call(http.MethodGet,"/api/v1/datasources",nil,cookie,"");got.Code!=http.StatusUnauthorized{t.Fatalf("revoked session status=%d",got.Code)}
}
func TestAuthenticationRejectsMissingOriginOnMutation(t *testing.T){
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 if _,err=store.BootstrapAdmin(context.Background(),"admin","a-strong-password-123");err!=nil{t.Fatal(err)}
 server:=NewServerWithStore(store);defer server.Close()
 req:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"admin","password":"a-strong-password-123"}`));req.Header.Set("Content-Type","application/json")
 rec:=httptest.NewRecorder();server.Handler().ServeHTTP(rec,req);cookie:=rec.Result().Cookies()[0]
 mutation:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/logout",bytes.NewBufferString(`{}`));mutation.Header.Set("Content-Type","application/json");mutation.AddCookie(cookie)
 out:=httptest.NewRecorder();server.Handler().ServeHTTP(out,mutation)
 if out.Code!=http.StatusForbidden{t.Fatalf("missing Origin status=%d body=%s",out.Code,out.Body.String())}
}
func TestLoginDoesNotRevealUnknownUsername(t *testing.T){
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 if _,err=store.BootstrapAdmin(context.Background(),"admin","a-strong-password-123");err!=nil{t.Fatal(err)}
 server:=NewServerWithStore(store);defer server.Close()
 req:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"missing","password":"a-strong-password-123"}`));req.Header.Set("Content-Type","application/json")
 rec:=httptest.NewRecorder();server.Handler().ServeHTTP(rec,req)
 if rec.Code!=http.StatusUnauthorized||!bytes.Contains(rec.Body.Bytes(),[]byte("invalid username or password")){t.Fatalf("unexpected login response: %d %s",rec.Code,rec.Body.String())}
 var ignored map[string]any;_ = json.Unmarshal(rec.Body.Bytes(),&ignored)
}

func TestRoleAuthorizationPolicy(t *testing.T) {
 cases:=[]struct{role,method,path string;want bool}{
  {"viewer",http.MethodGet,"/api/v1/audit-events",true},
  {"viewer",http.MethodPost,"/api/v1/datasources",false},
  {"viewer",http.MethodPost,"/api/v1/shard-rules/r1/preview",true},
  {"viewer",http.MethodPost,"/api/v1/datasources/d1/migrations/preview",false},
  {"migration_operator",http.MethodPost,"/api/v1/datasources/d1/migrations/tasks",true},
  {"migration_operator",http.MethodPost,"/api/v1/shard-rules/r1/clone-tasks",false},
  {"shard_operator",http.MethodPost,"/api/v1/shard-rules/r1/clone-tasks",true},
  {"shard_operator",http.MethodPost,"/api/v1/datasources/d1/migrations/tasks",false},
  {"viewer",http.MethodPost,"/api/v1/auth/users",false},
  {"viewer",http.MethodGet,"/api/v1/auth/security-events",false},
  {"viewer",http.MethodGet,"/api/v1/auth/users",false},
  {"viewer",http.MethodGet,"/api/v1/admin-only/future-endpoint",false},
  {"viewer",http.MethodGet,"/api/v1/datasources/d1/migrations/status",true},
  {"viewer",http.MethodGet,"/api/v1/shard-ddl-tasks/task-1/audit",true},
  {"viewer",http.MethodPost,"/api/v1/auth/logout",true},
  {"migration_operator",http.MethodPost,"/api/v1/tenant-migration-batches",true},
  {"migration_operator",http.MethodPost,"/api/v1/migration-tasks/task-1/cancel",true},
  {"shard_operator",http.MethodPost,"/api/v1/migration-tasks/task-1/cancel",false},
  {"viewer",http.MethodPost,"/api/v1/migration-tasks/task-1/cancel",false},
  {"administrator",http.MethodPost,"/api/v1/migration-tasks/task-1/cancel",true},
  {"migration_operator",http.MethodPost,"/api/v1/tenants/tenant-1/migrations/tasks",true},
  {"shard_operator",http.MethodPost,"/api/v1/shard-rules/r1/tables/exists",true},
  {"shard_operator",http.MethodPost,"/api/v1/shard-ddl-tasks/task-1/cancel",true},
  {"administrator",http.MethodDelete,"/api/v1/datasources/d1",true},
 }
 for _,tc:=range cases{if got:=authorizeAdminRole(tc.role,tc.method,tc.path);got!=tc.want{t.Errorf("authorizeAdminRole(%q,%q,%q)=%v, want %v",tc.role,tc.method,tc.path,got,tc.want)}}
}

func TestLoginRateLimiterIsBoundedAndAppliesCooldown(t *testing.T) {
 limiter:=newLoginLimiter()
 now:=time.Now()
 for i:=0;i<5;i++{if !limiter.allow("127.0.0.1|admin",now){t.Fatalf("attempt %d unexpectedly blocked",i+1)}}
 if limiter.allow("127.0.0.1|admin",now){t.Fatal("sixth attempt should be blocked")}
 if limiter.allow("127.0.0.1|admin",now.Add(10*time.Second)){t.Fatal("cooldown should remain active")}
 if !limiter.allow("127.0.0.1|admin",now.Add(31*time.Second)){t.Fatal("attempt should be allowed after the cooldown")}
}

func TestSameOriginChecksSchemeAndHost(t *testing.T) {
 httpRequest:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/logout",nil)
 httpRequest.Header.Set("Origin","https://localhost")
 if sameOrigin(httpRequest){t.Fatal("HTTPS Origin must not match an HTTP request")}
 httpsRequest:=httptest.NewRequest(http.MethodPost,"https://localhost/api/v1/auth/logout",nil)
 httpsRequest.Header.Set("Origin","https://localhost")
 if !sameOrigin(httpsRequest){t.Fatal("same HTTPS origin should be accepted")}
 crossHost:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/logout",nil)
 crossHost.Header.Set("Origin","http://attacker.example")
 if sameOrigin(crossHost){t.Fatal("cross-host Origin must be rejected")}
}

func TestMigrationOperatorCannotSubmitRollback(t *testing.T) {
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 if _,err:=store.BootstrapAdmin(context.Background(),"root","a-strong-password-123");err!=nil{t.Fatal(err)}
 operator,err:=store.CreateAdminUser(context.Background(),"operator","a-operator-password-123","migration_operator");if err!=nil{t.Fatal(err)}
 source,err:=store.Create(context.Background(),adminstore.DataSourceInput{Name:"rollback target",Driver:"sqlite",DSN:":memory:"});if err!=nil{t.Fatal(err)}
 _=operator
 server:=NewServerWithMigrationDir(store,t.TempDir());defer server.Close()
 login:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"operator","password":"a-operator-password-123"}`))
 login.Header.Set("Content-Type","application/json")
 loginRec:=httptest.NewRecorder();server.Handler().ServeHTTP(loginRec,login)
 if loginRec.Code!=http.StatusOK{t.Fatalf("login status=%d body=%s",loginRec.Code,loginRec.Body.String())}
 req:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/datasources/"+source.ID+"/migrations/tasks",bytes.NewBufferString(`{"action":"down","confirm":true}`))
 req.Header.Set("Content-Type","application/json");req.Header.Set("Origin","http://localhost");req.AddCookie(loginRec.Result().Cookies()[0])
 rec:=httptest.NewRecorder();server.Handler().ServeHTTP(rec,req)
 if rec.Code!=http.StatusForbidden||!strings.Contains(rec.Body.String(),"administrator_required"){t.Fatalf("rollback status=%d body=%s",rec.Code,rec.Body.String())}
 tasks,err:=store.ListMigrationTasks(context.Background(),10);if err!=nil{t.Fatal(err)}
 if len(tasks)!=0{t.Fatalf("rollback request created %d tasks",len(tasks))}
}

func TestAdministratorUserManagementEndpoints(t *testing.T) {
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 root,err:=store.BootstrapAdmin(context.Background(),"root","a-strong-password-123");if err!=nil{t.Fatal(err)}
 server:=NewServerWithStore(store);defer server.Close()
 login:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"root","password":"a-strong-password-123"}`))
 login.Header.Set("Content-Type","application/json")
 loginRec:=httptest.NewRecorder();server.Handler().ServeHTTP(loginRec,login)
 if loginRec.Code!=http.StatusOK{t.Fatalf("login status=%d body=%s",loginRec.Code,loginRec.Body.String())}
 cookie:=loginRec.Result().Cookies()[0]
 request:=func(method,path,body string)*httptest.ResponseRecorder{
  req:=httptest.NewRequest(method,"http://localhost"+path,bytes.NewBufferString(body));req.AddCookie(cookie)
  if body!=""{req.Header.Set("Content-Type","application/json")}
  if method!=http.MethodGet{req.Header.Set("Origin","http://localhost")}
  rec:=httptest.NewRecorder();server.Handler().ServeHTTP(rec,req);return rec
 }
 created:=request(http.MethodPost,"/api/v1/auth/users",`{"username":"operator","password":"a-operator-password-123","role":"migration_operator"}`)
 if created.Code!=http.StatusCreated{t.Fatalf("create user status=%d body=%s",created.Code,created.Body.String())}
 var user adminstore.Admin
 if err:=json.Unmarshal(created.Body.Bytes(),&user);err!=nil{t.Fatal(err)}
 if user.ID==""||user.Role!="migration_operator"{t.Fatalf("unexpected created user: %+v",user)}
 token,_,err:=store.CreateAdminSession(context.Background(),user.ID,12*time.Hour);if err!=nil{t.Fatal(err)}
 updated:=request(http.MethodPut,"/api/v1/auth/users/"+user.ID+"/role",`{"role":"shard_operator"}`)
 if updated.Code!=http.StatusNoContent{t.Fatalf("role update status=%d body=%s",updated.Code,updated.Body.String())}
 if _,err:=store.GetAdminSession(context.Background(),token);!errors.Is(err,adminstore.ErrSessionNotFound){t.Fatalf("role update did not revoke session: %v",err)}
 removed:=request(http.MethodDelete,"/api/v1/auth/users/"+user.ID,`{}`)
 if removed.Code!=http.StatusNoContent{t.Fatalf("delete user status=%d body=%s",removed.Code,removed.Body.String())}
 if got:=request(http.MethodDelete,"/api/v1/auth/users/"+root.ID,`{}`);got.Code!=http.StatusConflict{t.Fatalf("self-delete status=%d body=%s",got.Code,got.Body.String())}
 if got:=request(http.MethodPut,"/api/v1/auth/users/"+root.ID+"/role",`{"role":"viewer"}`);got.Code!=http.StatusConflict{t.Fatalf("self-role-change status=%d body=%s",got.Code,got.Body.String())}
}

func TestMigrationAuditRecordsAuthenticatedActor(t *testing.T) {
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 if _,err:=store.BootstrapAdmin(context.Background(),"audit-operator","a-strong-password-123");err!=nil{t.Fatal(err)}
 source,err:=store.Create(context.Background(),adminstore.DataSourceInput{Name:"audit actor source",Driver:"sqlite",DSN:":memory:"});if err!=nil{t.Fatal(err)}
 server:=NewServerWithMigrationDir(store,t.TempDir());defer server.Close()
 login:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"audit-operator","password":"a-strong-password-123"}`))
 login.Header.Set("Content-Type","application/json")
 loginRec:=httptest.NewRecorder();server.Handler().ServeHTTP(loginRec,login)
 if loginRec.Code!=http.StatusOK{t.Fatalf("login status=%d body=%s",loginRec.Code,loginRec.Body.String())}
 req:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/datasources/"+source.ID+"/migrations/tasks",bytes.NewBufferString(`{"action":"up"}`))
 req.Header.Set("Content-Type","application/json");req.Header.Set("Origin","http://localhost");req.AddCookie(loginRec.Result().Cookies()[0])
 rec:=httptest.NewRecorder();server.Handler().ServeHTTP(rec,req)
 if rec.Code!=http.StatusAccepted{t.Fatalf("submit migration status=%d body=%s",rec.Code,rec.Body.String())}
 var task adminstore.MigrationTask
 if err:=json.Unmarshal(rec.Body.Bytes(),&task);err!=nil{t.Fatal(err)}
 events,err:=store.ListMigrationAuditEvents(context.Background(),task.ID);if err!=nil{t.Fatal(err)}
 if len(events)==0{t.Fatal("expected audit events")}
 for _,event:=range events{if event.Actor!="audit-operator"{t.Errorf("audit actor=%q, want authenticated username",event.Actor)}}
}

func TestViewerRoleIsEnforcedByHTTPMiddleware(t *testing.T) {
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 if _,err:=store.BootstrapAdmin(context.Background(),"root","a-strong-password-123");err!=nil{t.Fatal(err)}
 if _,err:=store.CreateAdminUser(context.Background(),"reader","a-reader-password-123","viewer");err!=nil{t.Fatal(err)}
 server:=NewServerWithStore(store);defer server.Close()
 login:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"reader","password":"a-reader-password-123"}`));login.Header.Set("Content-Type","application/json")
 loginRec:=httptest.NewRecorder();server.Handler().ServeHTTP(loginRec,login)
 if loginRec.Code!=http.StatusOK{t.Fatalf("viewer login status=%d body=%s",loginRec.Code,loginRec.Body.String())}
 cookie:=loginRec.Result().Cookies()[0]
 read:=httptest.NewRequest(http.MethodGet,"http://localhost/api/v1/datasources",nil);read.AddCookie(cookie)
 readRec:=httptest.NewRecorder();server.Handler().ServeHTTP(readRec,read)
 if readRec.Code!=http.StatusOK{t.Fatalf("viewer read status=%d body=%s",readRec.Code,readRec.Body.String())}
 write:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/datasources",bytes.NewBufferString(`{"name":"blocked","driver":"sqlite","dsn":":memory:"}`))
 write.Header.Set("Content-Type","application/json");write.Header.Set("Origin","http://localhost");write.AddCookie(cookie)
 writeRec:=httptest.NewRecorder();server.Handler().ServeHTTP(writeRec,write)
 if writeRec.Code!=http.StatusForbidden{t.Fatalf("viewer write status=%d body=%s",writeRec.Code,writeRec.Body.String())}
 users:=httptest.NewRequest(http.MethodGet,"http://localhost/api/v1/auth/users",nil);users.AddCookie(cookie)
 usersRec:=httptest.NewRecorder();server.Handler().ServeHTTP(usersRec,users)
 if usersRec.Code!=http.StatusForbidden{t.Fatalf("viewer administrator-list status=%d body=%s",usersRec.Code,usersRec.Body.String())}
}

func TestSecurityAuditRecordsLoginAndAdministratorActions(t *testing.T) {
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 if _,err:=store.BootstrapAdmin(context.Background(),"security-root","a-strong-password-123");err!=nil{t.Fatal(err)}
 server:=NewServerWithStore(store);defer server.Close()
 login:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"security-root","password":"a-strong-password-123"}`))
 login.Header.Set("Content-Type","application/json")
 loginRec:=httptest.NewRecorder();server.Handler().ServeHTTP(loginRec,login)
 if loginRec.Code!=http.StatusOK{t.Fatalf("login status=%d body=%s",loginRec.Code,loginRec.Body.String())}
 cookie:=loginRec.Result().Cookies()[0]
 create:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/users",bytes.NewBufferString(`{"username":"audited-viewer","password":"a-viewer-password-123","role":"viewer"}`))
 create.Header.Set("Content-Type","application/json");create.Header.Set("Origin","http://localhost");create.AddCookie(cookie)
 createRec:=httptest.NewRecorder();server.Handler().ServeHTTP(createRec,create)
 if createRec.Code!=http.StatusCreated{t.Fatalf("create status=%d body=%s",createRec.Code,createRec.Body.String())}
 list:=httptest.NewRequest(http.MethodGet,"http://localhost/api/v1/auth/security-events",nil);list.AddCookie(cookie)
 listRec:=httptest.NewRecorder();server.Handler().ServeHTTP(listRec,list)
 if listRec.Code!=http.StatusOK{t.Fatalf("audit list status=%d body=%s",listRec.Code,listRec.Body.String())}
 if !strings.Contains(listRec.Body.String(),`"action":"login"`)||!strings.Contains(listRec.Body.String(),`"action":"admin_create"`){t.Fatalf("missing security events: %s",listRec.Body.String())}
 if strings.Contains(listRec.Body.String(),"a-strong-password-123")||strings.Contains(listRec.Body.String(),"a-viewer-password-123"){t.Fatal("security audit leaked password material")}
 if _,err:=store.CreateAdminUser(context.Background(),"audited-viewer-2","a-viewer-password-456","viewer");err!=nil{t.Fatal(err)}
 viewerLogin:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",bytes.NewBufferString(`{"username":"audited-viewer-2","password":"a-viewer-password-456"}`));viewerLogin.Header.Set("Content-Type","application/json")
 viewerRec:=httptest.NewRecorder();server.Handler().ServeHTTP(viewerRec,viewerLogin)
 if viewerRec.Code!=http.StatusOK{t.Fatalf("viewer login status=%d",viewerRec.Code)}
 denied:=httptest.NewRequest(http.MethodGet,"http://localhost/api/v1/auth/security-events",nil);denied.AddCookie(viewerRec.Result().Cookies()[0])
 deniedRec:=httptest.NewRecorder();server.Handler().ServeHTTP(deniedRec,denied)
 if deniedRec.Code!=http.StatusForbidden{t.Fatalf("viewer security audit status=%d body=%s",deniedRec.Code,deniedRec.Body.String())}
}


func TestReadOnlyAdminRouteRequiresExactResourceShape(t *testing.T) {
 cases:=[]struct{path string;want bool}{
  {"/api/v1/datasources",true},
  {"/api/v1/datasources/source-1",true},
  {"/api/v1/datasources//migrations/status",false},
  {"/api/v1/datasources/source-1/migrations/status",true},
  {"/api/v1/datasources/source-1/migrations/status/extra",false},
  {"/api/v1/datasources/",false},
  {"/api/v1/tenants/tenant-1",true},
  {"/api/v1/tenants//",false},
  {"/api/v1/migration-tasks/task-1/audit",true},
  {"/api/v1/migration-tasks//audit",false},
  {"/api/v1/shard-ddl-tasks/task-1/audit",true},
  {"/api/v1/shard-ddl-tasks/task-1/audit/extra",false},
 }
 for _,tc:=range cases {
  if got:=isReadOnlyAdminRoute(tc.path);got!=tc.want { t.Errorf("isReadOnlyAdminRoute(%q)=%v, want %v",tc.path,got,tc.want) }
 }
}

func TestLoginLimiterAppliesPerIPAcrossDifferentUsernames(t *testing.T) {
 limiter:=newLoginLimiter()
 reqA:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",nil)
 reqB:=httptest.NewRequest(http.MethodPost,"http://localhost/api/v1/auth/login",nil)
 reqA.RemoteAddr="192.0.2.10:1000"
 reqB.RemoteAddr="192.0.2.10:2000"
 now:=time.Now()
 for i:=0;i<5;i++ {
  if !limiter.allow(loginIPKey(reqA),now) { t.Fatalf("IP attempt %d unexpectedly blocked",i+1) }
 }
 if limiter.allow(loginIPKey(reqB),now) { t.Fatal("changing username or source port must not bypass per-IP limit") }
}
