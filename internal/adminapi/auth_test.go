package adminapi

import (
 "bytes"
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "testing"

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
 if got:=call(http.MethodPost,"/api/v1/auth/logout",[]byte(`{}`),cookie,"");got.Code!=http.StatusNoContent{t.Fatalf("logout status=%d body=%s",got.Code,got.Body.String())}
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
 if !limiter.allow("127.0.0.1|admin",now.Add(61*time.Second)){t.Fatal("attempt should be allowed after the rate window")}
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
 server:=NewServerWithMigrationDir(store,"../../migrations");defer server.Close()
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
