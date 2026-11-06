package adminstore

import (
 "context"
 "database/sql"
 "errors"
 "fmt"
 "strings"
 "time"
)

type securityAuditExecer interface {
 ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertSecurityAuditEvent(ctx context.Context, execer securityAuditExecer, event SecurityAuditEvent) error {
 switch event.Action {
 case "login", "logout", "password_change", "admin_create", "admin_role_update", "admin_delete":
 default: return fmt.Errorf("unsupported security audit action")
 }
 switch event.Outcome {
 case "succeeded", "failed", "denied":
 default: return fmt.Errorf("unsupported security audit outcome")
 }
 if len(event.Actor)>128 || len(event.Subject)>128 || len(event.RequestSource)>128 || len(event.Details)>256 { return fmt.Errorf("security audit field exceeds maximum length") }
 if event.OccurredAt.IsZero() { event.OccurredAt=time.Now().UTC() } else { event.OccurredAt=event.OccurredAt.UTC() }
 _,err:=execer.ExecContext(ctx, "INSERT INTO admin_security_audit_events (actor,subject,action,outcome,request_source,details,occurred_at) VALUES(?,?,?,?,?,?,?)", event.Actor,event.Subject,event.Action,event.Outcome,event.RequestSource,event.Details,event.OccurredAt.Format(time.RFC3339Nano))
 if err!=nil { return fmt.Errorf("record security audit event: %w",err) }
 return nil
}

func (s *Store) CreateAdminUserWithAudit(ctx context.Context, username,password,role string,event SecurityAuditEvent)(Admin,error) {
 username=strings.TrimSpace(username)
 if username=="" || len(username)>128 { return Admin{},ErrInvalidAdminInput }
 if !validAdminRole(role) { return Admin{},ErrInvalidRole }
 encoded,err:=encodePassword(password);if err!=nil{return Admin{},err}
 id,err:=newID();if err!=nil{return Admin{},err}
 now:=time.Now().UTC();stamp:=now.Format(time.RFC3339Nano)
 tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return Admin{},fmt.Errorf("begin administrator creation: %w",err)}
 defer tx.Rollback() //nolint:errcheck
 if _,err=tx.ExecContext(ctx,"INSERT INTO admin_users(id,username,password_hash,role,created_at,updated_at) VALUES(?,?,?,?,?,?)",id,username,encoded,role,stamp,stamp);err!=nil{
  if strings.Contains(strings.ToLower(err.Error()),"unique"){return Admin{},ErrAdminConflict}
  return Admin{},fmt.Errorf("create administrator account: %w",err)
 }
 event.Subject=username
 if err=insertSecurityAuditEvent(ctx,tx,event);err!=nil{return Admin{},err}
 if err=tx.Commit();err!=nil{return Admin{},fmt.Errorf("commit administrator creation: %w",err)}
 return Admin{ID:id,Username:username,Role:role,CreatedAt:now},nil
}

func (s *Store) UpdateAdminRoleWithAudit(ctx context.Context,id,role string,event SecurityAuditEvent) error {
 if !validAdminRole(role){return ErrInvalidRole}
 tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return fmt.Errorf("begin administrator role update: %w",err)}
 defer tx.Rollback() //nolint:errcheck
 var current string
 if err=tx.QueryRowContext(ctx,"SELECT role FROM admin_users WHERE id=?",id).Scan(&current);err!=nil{
  if errors.Is(err,sql.ErrNoRows){return ErrAdminNotFound};return fmt.Errorf("load administrator role: %w",err)
 }
 if current=="administrator" && role!="administrator"{
  var count int
  if err=tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM admin_users WHERE role='administrator'").Scan(&count);err!=nil{return err}
  if count<=1{return ErrLastAdministrator}
 }
 now:=time.Now().UTC().Format(time.RFC3339Nano)
 if _,err=tx.ExecContext(ctx,"UPDATE admin_users SET role=?,updated_at=? WHERE id=?",role,now,id);err!=nil{return fmt.Errorf("update administrator role: %w",err)}
 if _,err=tx.ExecContext(ctx,"UPDATE admin_sessions SET revoked_at=? WHERE admin_id=? AND revoked_at IS NULL",now,id);err!=nil{return fmt.Errorf("revoke sessions after role change: %w",err)}
 event.Details="administrator role changed to "+role
 if err=insertSecurityAuditEvent(ctx,tx,event);err!=nil{return err}
 if err=tx.Commit();err!=nil{return fmt.Errorf("commit administrator role update: %w",err)}
 return nil
}

func (s *Store) DeleteAdminUserWithAudit(ctx context.Context,id string,event SecurityAuditEvent) error {
 tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return fmt.Errorf("begin administrator deletion: %w",err)}
 defer tx.Rollback() //nolint:errcheck
 var role string
 if err=tx.QueryRowContext(ctx,"SELECT role FROM admin_users WHERE id=?",id).Scan(&role);err!=nil{
  if errors.Is(err,sql.ErrNoRows){return ErrAdminNotFound};return fmt.Errorf("load administrator account: %w",err)
 }
 if role=="administrator"{
  var count int
  if err=tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM admin_users WHERE role='administrator'").Scan(&count);err!=nil{return err}
  if count<=1{return ErrLastAdministrator}
 }
 if _,err=tx.ExecContext(ctx,"DELETE FROM admin_sessions WHERE admin_id=?",id);err!=nil{return fmt.Errorf("delete administrator sessions: %w",err)}
 if _,err=tx.ExecContext(ctx,"DELETE FROM admin_users WHERE id=?",id);err!=nil{return fmt.Errorf("delete administrator account: %w",err)}
 if err=insertSecurityAuditEvent(ctx,tx,event);err!=nil{return err}
 if err=tx.Commit();err!=nil{return fmt.Errorf("commit administrator deletion: %w",err)}
 return nil
}

func (s *Store) ChangeAdminPasswordWithAudit(ctx context.Context,adminID,currentPassword,newPassword string,event SecurityAuditEvent) error {
 if len(newPassword)<12 || len(newPassword)>1024 || strings.TrimSpace(newPassword)=="" { return ErrInvalidPassword }
 tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return fmt.Errorf("begin password change: %w",err)}
 defer tx.Rollback() //nolint:errcheck
 var encoded string
 if err=tx.QueryRowContext(ctx,"SELECT password_hash FROM admin_users WHERE id=?",adminID).Scan(&encoded);err!=nil{
  if errors.Is(err,sql.ErrNoRows){return ErrInvalidCredentials};return fmt.Errorf("load administrator password: %w",err)
 }
 if !verifyPassword(encoded,currentPassword){return ErrInvalidCredentials}
 newHash,err:=encodePassword(newPassword);if err!=nil{return err}
 now:=time.Now().UTC().Format(time.RFC3339Nano)
 if _,err=tx.ExecContext(ctx,"UPDATE admin_users SET password_hash=?,updated_at=? WHERE id=?",newHash,now,adminID);err!=nil{return fmt.Errorf("update administrator password: %w",err)}
 if _,err=tx.ExecContext(ctx,"UPDATE admin_sessions SET revoked_at=? WHERE admin_id=? AND revoked_at IS NULL",now,adminID);err!=nil{return fmt.Errorf("revoke sessions after password change: %w",err)}
 if err=insertSecurityAuditEvent(ctx,tx,event);err!=nil{return err}
 if err=tx.Commit();err!=nil{return fmt.Errorf("commit password change: %w",err)}
 return nil
}
