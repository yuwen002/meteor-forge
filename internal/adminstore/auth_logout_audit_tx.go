package adminstore

import (
 "context"
 "crypto/sha256"
 "fmt"
 "time"
)

func (s *Store) RevokeAdminSessionWithAudit(ctx context.Context, token string, event SecurityAuditEvent) error {
 if token=="" { return nil }
 sum:=sha256.Sum256([]byte(token))
 tx,err:=s.db.BeginTx(ctx,nil)
 if err!=nil { return fmt.Errorf("begin administrator logout: %w",err) }
 defer tx.Rollback() //nolint:errcheck
 now:=time.Now().UTC().Format(time.RFC3339Nano)
 if _,err=tx.ExecContext(ctx,"UPDATE admin_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL",now,sum[:]);err!=nil {
  return fmt.Errorf("revoke administrator session: %w",err)
 }
 if err=insertSecurityAuditEvent(ctx,tx,event);err!=nil { return err }
 if err=tx.Commit();err!=nil { return fmt.Errorf("commit administrator logout: %w",err) }
 return nil
}
