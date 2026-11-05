package adminapi

import (
 "context"
 "encoding/json"
 "errors"
 "io"
 "net"
 "net/http"
 "strconv"
 "net/url"
 "strings"
 "sync"
 "time"

 "github.com/yuwen002/meteor-forge/internal/adminstore"
)

const adminSessionCookie = "meteor_admin_session"
const adminSessionLifetime = 12*time.Hour

type adminContextKey struct{}
type loginAttempt struct { count int; windowStart, blockedUntil time.Time }
type loginLimiter struct { mu sync.Mutex; entries map[string]loginAttempt }
func newLoginLimiter()*loginLimiter{return &loginLimiter{entries:make(map[string]loginAttempt)}}
func(l *loginLimiter) allow(key string,now time.Time)bool{
 l.mu.Lock();defer l.mu.Unlock()
 for k,v:=range l.entries { if now.Sub(v.windowStart)>10*time.Minute && !v.blockedUntil.After(now){delete(l.entries,k)} }
 v,ok:=l.entries[key]
 if !ok && len(l.entries)>=4096{return false}
 if v.blockedUntil.After(now){return false}
 if !ok || now.Sub(v.windowStart)>time.Minute {v=loginAttempt{windowStart:now}}
 v.count++
 if v.count>5 {v.blockedUntil=now.Add(30*time.Second)}
 l.entries[key]=v
 return v.count<=5
}
func(l *loginLimiter) success(key string){l.mu.Lock();delete(l.entries,key);l.mu.Unlock()}

func(s *Server) registerAuthRoutes(mux *http.ServeMux){
 mux.HandleFunc("POST "+apiPrefix+"/auth/login",s.loginHandler)
 mux.HandleFunc("GET "+apiPrefix+"/auth/session",s.sessionHandler)
 mux.HandleFunc("POST "+apiPrefix+"/auth/logout",s.logoutHandler)
 mux.HandleFunc("POST "+apiPrefix+"/auth/password",s.passwordHandler)
 mux.HandleFunc("GET "+apiPrefix+"/auth/users",s.listAdminUsersHandler)
 mux.HandleFunc("GET "+apiPrefix+"/auth/security-events",s.listSecurityAuditEventsHandler)
 mux.HandleFunc("POST "+apiPrefix+"/auth/users",s.createAdminUserHandler)
 mux.HandleFunc("PUT "+apiPrefix+"/auth/users/{id}/role",s.updateAdminRoleHandler)
 mux.HandleFunc("DELETE "+apiPrefix+"/auth/users/{id}",s.deleteAdminUserHandler)
}
func loginKey(r *http.Request,username string)string{
 host:=r.RemoteAddr
 if parsed,_,err:=net.SplitHostPort(host);err==nil{host=parsed}
 return strings.ToLower(host)+"|"+strings.ToLower(strings.TrimSpace(username))
}
func decodeAuthJSON(w http.ResponseWriter,r *http.Request,dst any)bool{
 if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")),"application/json"){
  writeJSON(w,http.StatusUnsupportedMediaType,map[string]string{"error":"application/json is required"});return false
 }
 dec:=json.NewDecoder(http.MaxBytesReader(w,r.Body,8<<10));dec.DisallowUnknownFields()
 if err:=dec.Decode(dst);err!=nil{writeJSON(w,http.StatusBadRequest,map[string]string{"error":"invalid request"});return false}
 var extra any
 if err:=dec.Decode(&extra);!errors.Is(err,io.EOF){writeJSON(w,http.StatusBadRequest,map[string]string{"error":"invalid request"});return false}
 return true
}
func(s *Server) loginHandler(w http.ResponseWriter,r *http.Request){
 if s.authStore==nil{writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"authentication unavailable"});return}
 var input struct{Username string `json:"username"`;Password string `json:"password"`}
 if !decodeAuthJSON(w,r,&input){return}
 key:=loginKey(r,input.Username)
 if !s.loginLimiter.allow(key,time.Now()){writeJSON(w,http.StatusTooManyRequests,map[string]string{"error":"too many login attempts"});return}
 admin,err:=s.authStore.AuthenticateAdmin(r.Context(),input.Username,input.Password)
 if err!=nil{
  if !errors.Is(err,adminstore.ErrInvalidCredentials){writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"authentication failed"});return}
  _ = s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Subject:strings.TrimSpace(input.Username),Action:"login",Outcome:"failed",RequestSource:requestSourceForAudit(r),Details:"invalid credentials"})
  writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"invalid username or password"});return
 }
 token,expires,err:=s.authStore.CreateAdminSession(r.Context(),admin.ID,adminSessionLifetime)
 if err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to create session"});return}
 if auditErr:=s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Actor:admin.Username,Subject:admin.Username,Action:"login",Outcome:"succeeded",RequestSource:requestSourceForAudit(r),Details:"administrator authenticated"});auditErr!=nil{
  _ = s.authStore.RevokeAdminSession(r.Context(),token)
  writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to record authentication event"});return
 }
 s.loginLimiter.success(key)
 http.SetCookie(w,&http.Cookie{Name:adminSessionCookie,Value:token,Path:apiPrefix,HttpOnly:true,Secure:r.TLS!=nil,SameSite:http.SameSiteStrictMode,Expires:expires,MaxAge:int(adminSessionLifetime.Seconds())})
 writeJSON(w,http.StatusOK,map[string]any{"username":admin.Username,"role":admin.Role,"expires_at":expires.Format(time.RFC3339)})
}
func(s *Server) sessionHandler(w http.ResponseWriter,r *http.Request){
 session,ok:=adminSessionFromRequest(r);if !ok{writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"authentication required"});return}
 writeJSON(w,http.StatusOK,map[string]any{"username":session.Admin.Username,"role":session.Admin.Role,"expires_at":session.ExpiresAt.Format(time.RFC3339)})
}
func(s *Server) logoutHandler(w http.ResponseWriter,r *http.Request){
 if !requireAuthJSON(w,r){return}
 cookie,_:=r.Cookie(adminSessionCookie)
 if cookie!=nil{if err:=s.authStore.RevokeAdminSession(r.Context(),cookie.Value);err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to revoke session"});return}}
 session,_:=adminSessionFromRequest(r)
 if err:=s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Actor:session.Admin.Username,Subject:session.Admin.Username,Action:"logout",Outcome:"succeeded",RequestSource:requestSourceForAudit(r),Details:"administrator logged out"});err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to record logout event"});return}
 clearAdminCookie(w,r);w.WriteHeader(http.StatusNoContent)
}
func(s *Server) passwordHandler(w http.ResponseWriter,r *http.Request){
 if !requireAuthJSON(w,r){return}
 session,ok:=adminSessionFromRequest(r);if !ok{writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"authentication required"});return}
 var input struct{CurrentPassword string `json:"current_password"`;NewPassword string `json:"new_password"`}
 if !decodeAuthJSON(w,r,&input){return}
 if err:=s.authStore.ChangeAdminPassword(r.Context(),session.Admin.ID,input.CurrentPassword,input.NewPassword);err!=nil{
  if errors.Is(err,adminstore.ErrInvalidCredentials){
   _ = s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Actor:session.Admin.Username,Subject:session.Admin.Username,Action:"password_change",Outcome:"failed",RequestSource:requestSourceForAudit(r),Details:"current password verification failed"})
   writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"current password is incorrect"});return
  }
  if errors.Is(err,adminstore.ErrInvalidPassword){writeJSON(w,http.StatusBadRequest,map[string]string{"error":"new password must contain 12 to 1024 characters"});return}
  writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to change password"});return
 }
 if err:=s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Actor:session.Admin.Username,Subject:session.Admin.Username,Action:"password_change",Outcome:"succeeded",RequestSource:requestSourceForAudit(r),Details:"administrator password changed"});err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to record password change event"});return}
 clearAdminCookie(w,r);w.WriteHeader(http.StatusNoContent)
}
func requireAuthJSON(w http.ResponseWriter,r *http.Request)bool{
 if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")),"application/json"){writeJSON(w,http.StatusUnsupportedMediaType,map[string]string{"error":"application/json is required"});return false};return true
}
func clearAdminCookie(w http.ResponseWriter,r *http.Request){
 http.SetCookie(w,&http.Cookie{Name:adminSessionCookie,Value:"",Path:apiPrefix,HttpOnly:true,Secure:r.TLS!=nil,SameSite:http.SameSiteStrictMode,MaxAge:-1,Expires:time.Unix(1,0)})
}
func adminSessionFromRequest(r *http.Request)(adminstore.AdminSession,bool){
 v,ok:=r.Context().Value(adminContextKey{}).(adminstore.AdminSession);return v,ok
}
func(s *Server) withAuthentication(next http.Handler)http.Handler{
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path==apiPrefix+"/health" || r.URL.Path==apiPrefix+"/auth/login"{next.ServeHTTP(w,r);return}
  if s.authStore==nil{writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"authentication required"});return}
  cookie,err:=r.Cookie(adminSessionCookie);if err!=nil{writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"authentication required"});return}
  session,err:=s.authStore.GetAdminSession(r.Context(),cookie.Value);if err!=nil{clearAdminCookie(w,r);writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"authentication required"});return}
  switch r.Method{case http.MethodPost,http.MethodPut,http.MethodPatch,http.MethodDelete:
   if !sameOrigin(r){writeJSON(w,http.StatusForbidden,map[string]string{"error":"same-origin request required"});return}
   if strings.HasPrefix(r.URL.Path,apiPrefix+"/auth/")&&!requireAuthJSON(w,r){return}
  }
  if !authorizeAdminRole(session.Admin.Role,r.Method,r.URL.Path) { writeJSON(w,http.StatusForbidden,map[string]string{"error":"permission denied"});return }
  ctx:=context.WithValue(r.Context(),adminContextKey{},session)
  next.ServeHTTP(w,r.WithContext(ctx))
 })
}
func sameOrigin(r *http.Request)bool{
 origin:=r.Header.Get("Origin");if origin==""{return false}
 parsed,err:=url.Parse(origin);if err!=nil||parsed.User!=nil||parsed.Path!=""||parsed.RawQuery!=""||parsed.Fragment!=""{return false}
 expectedScheme:="http"
 if r.TLS!=nil { expectedScheme="https" }
 if parsed.Scheme!=expectedScheme { return false }
 return strings.EqualFold(parsed.Host,r.Host)
}

func authorizeAdminRole(role, method, path string) bool {
 if role == "administrator" { return true }
 if path == apiPrefix+"/auth/logout" || path == apiPrefix+"/auth/password" { return method == http.MethodPost }
 if path == apiPrefix+"/auth/users" || strings.HasPrefix(path, apiPrefix+"/auth/users/") || path == apiPrefix+"/auth/security-events" { return false }
 if method == http.MethodGet || method == http.MethodHead { return true }
 parts := strings.Split(strings.Trim(path, "/"), "/")
 if method == http.MethodPost {
  if path == apiPrefix+"/tenant-migration-batches" { return role == "migration_operator" }
  if len(parts)==6 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="datasources" && parts[4]=="migrations" && (parts[5]=="preview" || parts[5]=="tasks") { return role=="migration_operator" }
  if len(parts)==6 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="tenants" && parts[4]=="migrations" && parts[5]=="tasks" { return role=="migration_operator" }
  if len(parts)==5 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="migration-tasks" && parts[4]=="cancel" { return role=="migration_operator" }
  if len(parts)==5 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="shard-rules" && parts[4]=="preview" { return role=="viewer" || role=="shard_operator" }
  if len(parts)==6 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="shard-rules" && parts[4]=="tables" && parts[5]=="exists" { return role=="viewer" || role=="shard_operator" }
  if len(parts)==5 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="shard-rules" && parts[4]=="clone-preview" { return role=="shard_operator" }
  if len(parts)==5 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="shard-rules" && parts[4]=="clone-tasks" { return role=="shard_operator" }
  if len(parts)==5 && parts[0]=="api" && parts[1]=="v1" && parts[2]=="shard-ddl-tasks" && parts[4]=="cancel" { return role=="shard_operator" }
 }
 return false
}

func (s *Server) listAdminUsersHandler(w http.ResponseWriter, r *http.Request) {
 items, err := s.authStore.ListAdminUsers(r.Context())
 if err != nil { writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to list administrators"});return }
 writeJSON(w,http.StatusOK,map[string]any{"items":items})
}
func (s *Server) createAdminUserHandler(w http.ResponseWriter, r *http.Request) {
 var input struct { Username string `json:"username"`; Password string `json:"password"`; Role string `json:"role"` }
 if !decodeAuthJSON(w,r,&input) { return }
 item,err:=s.authStore.CreateAdminUser(r.Context(),input.Username,input.Password,input.Role)
 if err!=nil {
  switch {
  case errors.Is(err,adminstore.ErrInvalidPassword),errors.Is(err,adminstore.ErrInvalidRole),errors.Is(err,adminstore.ErrInvalidAdminInput):
   writeJSON(w,http.StatusBadRequest,map[string]string{"error":"invalid administrator input"})
  case errors.Is(err,adminstore.ErrAdminConflict):
   writeJSON(w,http.StatusConflict,map[string]string{"error":"administrator username already exists"})
  default:
   writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to create administrator"})
  }
  return
 }
 current,_:=adminSessionFromRequest(r)
 _ = s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Actor:current.Admin.Username,Subject:item.Username,Action:"admin_create",Outcome:"succeeded",RequestSource:requestSourceForAudit(r),Details:"administrator account created"})
 writeJSON(w,http.StatusCreated,item)
}
func (s *Server) updateAdminRoleHandler(w http.ResponseWriter, r *http.Request) {
 current,_:=adminSessionFromRequest(r)
 id:=r.PathValue("id")
 if id==current.Admin.ID { writeJSON(w,http.StatusConflict,map[string]string{"error":"cannot change your own role"});return }
 var input struct { Role string `json:"role"` }
 if !decodeAuthJSON(w,r,&input) { return }
 err:=s.authStore.UpdateAdminRole(r.Context(),id,input.Role)
 if err!=nil {
  switch {
  case errors.Is(err,adminstore.ErrAdminNotFound):writeJSON(w,http.StatusNotFound,map[string]string{"error":"administrator not found"})
  case errors.Is(err,adminstore.ErrInvalidRole):writeJSON(w,http.StatusBadRequest,map[string]string{"error":"invalid role"})
  case errors.Is(err,adminstore.ErrLastAdministrator):writeJSON(w,http.StatusConflict,map[string]string{"error":"cannot demote the last administrator"})
  default:writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to update administrator role"})
  }
  return
 }
 current,_:=adminSessionFromRequest(r)
 _ = s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Actor:current.Admin.Username,Subject:id,Action:"admin_role_update",Outcome:"succeeded",RequestSource:requestSourceForAudit(r),Details:"administrator role changed to "+input.Role})
 w.WriteHeader(http.StatusNoContent)
}
func (s *Server) deleteAdminUserHandler(w http.ResponseWriter, r *http.Request) {
 current,_:=adminSessionFromRequest(r)
 id:=r.PathValue("id")
 if id==current.Admin.ID { writeJSON(w,http.StatusConflict,map[string]string{"error":"cannot delete your own account"});return }
 err:=s.authStore.DeleteAdminUser(r.Context(),id)
 if err!=nil {
  switch {
  case errors.Is(err,adminstore.ErrAdminNotFound):writeJSON(w,http.StatusNotFound,map[string]string{"error":"administrator not found"})
  case errors.Is(err,adminstore.ErrLastAdministrator):writeJSON(w,http.StatusConflict,map[string]string{"error":"cannot delete the last administrator"})
  default:writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to delete administrator"})
  }
  return
 }
 current,_:=adminSessionFromRequest(r)
 _ = s.authStore.RecordSecurityAuditEvent(r.Context(),adminstore.SecurityAuditEvent{Actor:current.Admin.Username,Subject:id,Action:"admin_delete",Outcome:"succeeded",RequestSource:requestSourceForAudit(r),Details:"administrator account deleted"})
 w.WriteHeader(http.StatusNoContent)
}

func authenticatedActor(r *http.Request) string {
 session,ok:=adminSessionFromRequest(r)
 if ok && strings.TrimSpace(session.Admin.Username)!="" { return session.Admin.Username }
 return "local-api"
}

func requestSourceForAudit(r *http.Request) string {
 host:=r.RemoteAddr
 if parsed,_,err:=net.SplitHostPort(host);err==nil{host=parsed}
 if len(host)>128{return host[:128]}
 return host
}

func (s *Server) listSecurityAuditEventsHandler(w http.ResponseWriter,r *http.Request) {
 limit:=50
 if raw:=r.URL.Query().Get("limit");raw!="" { value,err:=strconv.Atoi(raw);if err!=nil||value<1||value>200{writeJSON(w,http.StatusBadRequest,map[string]string{"error":"limit must be between 1 and 200"});return};limit=value }
 offset:=0
 if raw:=r.URL.Query().Get("offset");raw!="" { value,err:=strconv.Atoi(raw);if err!=nil||value<0||value>10000000{writeJSON(w,http.StatusBadRequest,map[string]string{"error":"offset must be between 0 and 10000000"});return};offset=value }
 items,total,err:=s.authStore.ListSecurityAuditEvents(r.Context(),limit,offset)
 if err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to list security audit events"});return}
 writeJSON(w,http.StatusOK,map[string]any{"items":items,"total":total,"limit":limit,"offset":offset})
}
