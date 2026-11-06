package adminstore

import (
 "context"
 "crypto/rand"
 "crypto/sha256"
 "encoding/base64"
 "fmt"
 "time"
)

func (s *Store) CreateAdminSessionWithAudit(ctx context.Context, adminID string, lifetime time.Duration, event SecurityAuditEvent) (string,time.Time,error) {
 if lifetime<=0 || lifetime>24*time.Hour { return "",time.Time{},fmt.Errorf("session lifetime must be between 1ns and 24h") }
 raw:=make([]byte,32)
 if _,err:=rand.Read(raw);err!=nil{return "",time.Time{},fmt.Errorf("generate session token: %w",err)}
 token:=base64.RawURLEncoding.EncodeToString(raw)
 sum:=sha256.Sum256([]byte(token))
 now:=time.Now().UTC()
 expires:=now.Add(lifetime)
 tx,err:=s.db.BeginTx(ctx,nil)
 if err!=nil{return "",time.Time{},fmt.Errorf("begin administrator session creation: %w",err)}
 defer tx.Rollback() //nolint:errcheck
 if _,err=tx.ExecContext(ctx,"INSERT INTO admin_sessions(token_hash,admin_id,created_at,expires_at) VALUES(?,?,?,?)",sum[:],adminID,now.Format(time.RFC3339Nano),expires.Format(time.RFC3339Nano));err!=nil{
  return "",time.Time{},fmt.Errorf("persist administrator session: %w",err)
 }
 if err=insertSecurityAuditEvent(ctx,tx,event);err!=nil{return "",time.Time{},err}
 if err=tx.Commit();err!=nil{return "",time.Time{},fmt.Errorf("commit administrator session creation: %w",err)}
 return token,expires,nil
}
