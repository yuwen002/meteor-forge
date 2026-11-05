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
