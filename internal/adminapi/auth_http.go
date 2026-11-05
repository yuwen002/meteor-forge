package adminapi

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "net"
 "net/http"
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
 if err:=dec.Decode(&extra);err==nil{writeJSON(w,http.StatusBadRequest,map[string]string{"error":"invalid request"});return false}
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
  writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"invalid username or password"});return
 }
 token,expires,err:=s.authStore.CreateAdminSession(r.Context(),admin.ID,adminSessionLifetime)
 if err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to create session"});return}
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
 clearAdminCookie(w,r);w.WriteHeader(http.StatusNoContent)
}
func(s *Server) passwordHandler(w http.ResponseWriter,r *http.Request){
 if !requireAuthJSON(w,r){return}
 session,ok:=adminSessionFromRequest(r);if !ok{writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"authentication required"});return}
 var input struct{CurrentPassword string `json:"current_password"`;NewPassword string `json:"new_password"`}
 if !decodeAuthJSON(w,r,&input){return}
 if err:=s.authStore.ChangeAdminPassword(r.Context(),session.Admin.ID,input.CurrentPassword,input.NewPassword);err!=nil{
  if errors.Is(err,adminstore.ErrInvalidCredentials){writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"current password is incorrect"});return}
  if errors.Is(err,adminstore.ErrInvalidPassword){writeJSON(w,http.StatusBadRequest,map[string]string{"error":"new password must contain 12 to 1024 characters"});return}
  writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"unable to change password"});return
 }
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
  ctx:=context.WithValue(r.Context(),adminContextKey{},session)
  next.ServeHTTP(w,r.WithContext(ctx))
 })
}
func sameOrigin(r *http.Request)bool{
 origin:=r.Header.Get("Origin");if origin==""{return false}
 parsed,err:=url.Parse(origin);if err!=nil||parsed.User!=nil||parsed.Path!=""||parsed.RawQuery!=""||parsed.Fragment!=""{return false}
 if parsed.Scheme!="http"&&parsed.Scheme!="https"{return false}
 return strings.EqualFold(parsed.Host,r.Host)
}
var _ = fmt.Sprintf
