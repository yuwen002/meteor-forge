package adminstore

import (
 "context"
 "errors"
 "testing"
)

func TestAdminMutationsRollbackWhenSecurityAuditInsertFails(t *testing.T) {
 ctx:=context.Background()
 store,err:=Open(ctx,":memory:",authTestKey);if err!=nil{t.Fatal(err)};defer store.Close()
 root,err:=store.BootstrapAdmin(ctx,"root","a-strong-password-123");if err!=nil{t.Fatal(err)}
 target,err:=store.CreateAdminUser(ctx,"target","a-target-password-123","viewer");if err!=nil{t.Fatal(err)}
 _,err=store.db.Exec("CREATE TRIGGER reject_security_audit BEFORE INSERT ON admin_security_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
 if err!=nil{t.Fatal(err)}
 event:=func(action,subject string) SecurityAuditEvent{return SecurityAuditEvent{Actor:"root",Subject:subject,Action:action,Outcome:"succeeded",Details:"test"}}

 if _,err=store.CreateAdminUserWithAudit(ctx,"created","a-created-password-123","viewer",event("admin_create","created"));err==nil{t.Fatal("expected create audit failure")}
 if _,err=store.AuthenticateAdmin(ctx,"created","a-created-password-123");!errors.Is(err,ErrInvalidCredentials){t.Fatalf("account creation was not rolled back: %v",err)}

 if err=store.UpdateAdminRoleWithAudit(ctx,target.ID,"migration_operator",event("admin_role_update",target.ID));err==nil{t.Fatal("expected role audit failure")}
 var role string
 if err=store.db.QueryRow("SELECT role FROM admin_users WHERE id=?",target.ID).Scan(&role);err!=nil{t.Fatal(err)}
 if role!="viewer"{t.Fatalf("role changed despite audit failure: %s",role)}

 if err=store.DeleteAdminUserWithAudit(ctx,target.ID,event("admin_delete",target.ID));err==nil{t.Fatal("expected delete audit failure")}
 if _,err=store.AuthenticateAdmin(ctx,"target","a-target-password-123");err!=nil{t.Fatalf("account deletion was not rolled back: %v",err)}

 if err=store.ChangeAdminPasswordWithAudit(ctx,root.ID,"a-strong-password-123","a-new-password-456",event("password_change","root"));err==nil{t.Fatal("expected password audit failure")}
 if _,err=store.AuthenticateAdmin(ctx,"root","a-strong-password-123");err!=nil{t.Fatalf("password change was not rolled back: %v",err)}
 if _,err=store.AuthenticateAdmin(ctx,"root","a-new-password-456");!errors.Is(err,ErrInvalidCredentials){t.Fatalf("new password persisted despite audit failure: %v",err)}
}
