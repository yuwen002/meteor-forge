package adminstore

import (
 "context"
 "crypto/hmac"
 "crypto/rand"
 "crypto/sha256"
 "crypto/subtle"
 "database/sql"
 "encoding/base64"
 "encoding/hex"
 "errors"
 "fmt"
 "strings"
 "time"
)

var (
 ErrAdminAlreadyExists = errors.New("administrator already exists")
 ErrInvalidCredentials = errors.New("invalid administrator credentials")
 ErrInvalidPassword = errors.New("password must contain 12 to 1024 characters")
 ErrSessionNotFound = errors.New("administrator session not found")
)

const passwordIterations = 310000

type Admin struct { ID, Username, Role string; CreatedAt time.Time }
type AdminSession struct { Admin Admin; CreatedAt, ExpiresAt time.Time }

func hashPassword(password string, salt []byte) []byte {
 var counter [4]byte
 counter[3] = 1
 mac := hmac.New(sha256.New, []byte(password))
 _, _ = mac.Write(salt); _, _ = mac.Write(counter[:])
 u := mac.Sum(nil); result := append([]byte(nil), u...)
 for i:=1; i<passwordIterations; i++ {
  mac = hmac.New(sha256.New, []byte(password)); _, _ = mac.Write(u); u = mac.Sum(nil)
  for j := range result { result[j] ^= u[j] }
 }
 return result
}

func encodePassword(password string) (string,error) {
 if len(password)<12 || len(password)>1024 || strings.TrimSpace(password)=="" { return "",ErrInvalidPassword }
 salt:=make([]byte,16); if _,err:=rand.Read(salt); err!=nil{return "",fmt.Errorf("generate password salt: %w",err)}
 return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",passwordIterations,hex.EncodeToString(salt),hex.EncodeToString(hashPassword(password,salt))),nil
}
func verifyPassword(encoded,password string) bool {
 p:=strings.Split(encoded,"$")
 if len(p)!=4 || p[0]!="pbkdf2-sha256" || p[1]!=fmt.Sprint(passwordIterations){return false}
 salt,e1:=hex.DecodeString(p[2]); want,e2:=hex.DecodeString(p[3])
 if e1!=nil || e2!=nil || len(salt)!=16 || len(want)!=sha256.Size{return false}
 return subtle.ConstantTimeCompare(hashPassword(password,salt),want)==1
}

// BootstrapAdmin creates the first administrator only and never resets an existing account.
func (s *Store) BootstrapAdmin(ctx context.Context,username,password string)(Admin,error){
 username=strings.TrimSpace(username); if username=="" || len(username)>128{return Admin{},errors.New("administrator username must contain 1 to 128 characters")}
 encoded,err:=encodePassword(password); if err!=nil{return Admin{},err}
 id,err:=newID(); if err!=nil{return Admin{},err}; now:=time.Now().UTC().Format(time.RFC3339Nano)
 tx,err:=s.db.BeginTx(ctx,nil); if err!=nil{return Admin{},fmt.Errorf("begin administrator bootstrap: %w",err)}; defer tx.Rollback() //nolint:errcheck
 var count int; if err:=tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM admin_users").Scan(&count);err!=nil{return Admin{},fmt.Errorf("check administrator bootstrap: %w",err)}
 if count!=0{return Admin{},ErrAdminAlreadyExists}
 if _,err:=tx.ExecContext(ctx,`INSERT INTO admin_users(id,username,password_hash,role,created_at,updated_at) VALUES(?,?,?,'administrator',?,?)`,id,username,encoded,now,now);err!=nil{return Admin{},fmt.Errorf("create initial administrator: %w",err)}
 if err:=tx.Commit();err!=nil{return Admin{},fmt.Errorf("commit administrator bootstrap: %w",err)}
 created,_:=time.Parse(time.RFC3339Nano,now); return Admin{ID:id,Username:username,Role:"administrator",CreatedAt:created},nil
}
func(s *Store) HasAdmin(ctx context.Context)(bool,error){var n int;if err:=s.db.QueryRowContext(ctx,"SELECT COUNT(*) FROM admin_users").Scan(&n);err!=nil{return false,fmt.Errorf("check administrator account: %w",err)};return n>0,nil}
func(s *Store) AuthenticateAdmin(ctx context.Context,username,password string)(Admin,error){
 var a Admin;var encoded,created string
 err:=s.db.QueryRowContext(ctx,"SELECT id,username,password_hash,role,created_at FROM admin_users WHERE username=? COLLATE NOCASE",strings.TrimSpace(username)).Scan(&a.ID,&a.Username,&encoded,&a.Role,&created)
 if errors.Is(err,sql.ErrNoRows){return Admin{},ErrInvalidCredentials};if err!=nil{return Admin{},fmt.Errorf("load administrator credentials: %w",err)}
 if !verifyPassword(encoded,password){return Admin{},ErrInvalidCredentials};a.CreatedAt,err=time.Parse(time.RFC3339Nano,created);if err!=nil{return Admin{},fmt.Errorf("parse administrator creation time: %w",err)};return a,nil
}
func(s *Store) CreateAdminSession(ctx context.Context,adminID string,lifetime time.Duration)(string,time.Time,error){
 if lifetime<=0 || lifetime>24*time.Hour{return "",time.Time{},errors.New("session lifetime must be between 1ns and 24h")}
 raw:=make([]byte,32);if _,err:=rand.Read(raw);err!=nil{return "",time.Time{},fmt.Errorf("generate session token: %w",err)}
 token:=base64.RawURLEncoding.EncodeToString(raw);sum:=sha256.Sum256([]byte(token));now:=time.Now().UTC();expires:=now.Add(lifetime)
 _,err:=s.db.ExecContext(ctx,`INSERT INTO admin_sessions(token_hash,admin_id,created_at,expires_at) VALUES(?,?,?,?)`,sum[:],adminID,now.Format(time.RFC3339Nano),expires.Format(time.RFC3339Nano))
 if err!=nil{return "",time.Time{},fmt.Errorf("persist administrator session: %w",err)};return token,expires,nil
}
func(s *Store) GetAdminSession(ctx context.Context,token string)(AdminSession,error){
 if token=="" || len(token)>128{return AdminSession{},ErrSessionNotFound};sum:=sha256.Sum256([]byte(token))
 var v AdminSession;var adminCreated,sessionCreated,expires string
 err:=s.db.QueryRowContext(ctx,`SELECT u.id,u.username,u.role,u.created_at,s.created_at,s.expires_at FROM admin_sessions s JOIN admin_users u ON u.id=s.admin_id WHERE s.token_hash=? AND s.revoked_at IS NULL AND s.expires_at>?`,sum[:],time.Now().UTC().Format(time.RFC3339Nano)).Scan(&v.Admin.ID,&v.Admin.Username,&v.Admin.Role,&adminCreated,&sessionCreated,&expires)
 if errors.Is(err,sql.ErrNoRows){return AdminSession{},ErrSessionNotFound};if err!=nil{return AdminSession{},fmt.Errorf("load administrator session: %w",err)}
 v.Admin.CreatedAt,err=time.Parse(time.RFC3339Nano,adminCreated);if err!=nil{return AdminSession{},fmt.Errorf("parse administrator creation time: %w",err)}
 v.CreatedAt,err=time.Parse(time.RFC3339Nano,sessionCreated);if err!=nil{return AdminSession{},fmt.Errorf("parse session creation time: %w",err)}
 v.ExpiresAt,err=time.Parse(time.RFC3339Nano,expires);if err!=nil{return AdminSession{},fmt.Errorf("parse session expiry: %w",err)}
 return v,nil
}
func(s *Store) RevokeAdminSession(ctx context.Context,token string)error{
 if token==""{return nil};sum:=sha256.Sum256([]byte(token));_,err:=s.db.ExecContext(ctx,"UPDATE admin_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL",time.Now().UTC().Format(time.RFC3339Nano),sum[:]);if err!=nil{return fmt.Errorf("revoke administrator session: %w",err)};return nil
}
func(s *Store) RevokeAllAdminSessions(ctx context.Context,adminID string)error{
 _,err:=s.db.ExecContext(ctx,"UPDATE admin_sessions SET revoked_at=? WHERE admin_id=? AND revoked_at IS NULL",time.Now().UTC().Format(time.RFC3339Nano),adminID);if err!=nil{return fmt.Errorf("revoke administrator sessions: %w",err)};return nil
}
func(s *Store) ChangeAdminPassword(ctx context.Context,adminID,currentPassword,newPassword string)error{
 var encoded string;err:=s.db.QueryRowContext(ctx,"SELECT password_hash FROM admin_users WHERE id=?",adminID).Scan(&encoded)
 if errors.Is(err,sql.ErrNoRows){return ErrInvalidCredentials};if err!=nil{return fmt.Errorf("load administrator password: %w",err)}
 if !verifyPassword(encoded,currentPassword){return ErrInvalidCredentials};newHash,err:=encodePassword(newPassword);if err!=nil{return err}
 now:=time.Now().UTC().Format(time.RFC3339Nano);tx,err:=s.db.BeginTx(ctx,nil);if err!=nil{return fmt.Errorf("begin password change: %w",err)};defer tx.Rollback() //nolint:errcheck
 if _,err:=tx.ExecContext(ctx,"UPDATE admin_users SET password_hash=?,updated_at=? WHERE id=?",newHash,now,adminID);err!=nil{return fmt.Errorf("update administrator password: %w",err)}
 if _,err:=tx.ExecContext(ctx,"UPDATE admin_sessions SET revoked_at=? WHERE admin_id=? AND revoked_at IS NULL",now,adminID);err!=nil{return fmt.Errorf("revoke sessions after password change: %w",err)}
 if err:=tx.Commit();err!=nil{return fmt.Errorf("commit password change: %w",err)};return nil
}
