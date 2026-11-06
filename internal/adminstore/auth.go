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
 ErrAdminNotFound = errors.New("administrator not found")
 ErrAdminConflict = errors.New("administrator username already exists")
 ErrInvalidRole = errors.New("invalid administrator role")
 ErrInvalidAdminInput = errors.New("invalid administrator input")
 ErrLastAdministrator = errors.New("cannot remove or demote the last administrator")
)

const passwordIterations = 310000

type Admin struct {
 ID string `json:"id"`
 Username string `json:"username"`
 Role string `json:"role"`
 CreatedAt time.Time `json:"created_at"`
}
type AdminSession struct { Admin Admin; CreatedAt, ExpiresAt time.Time }

func pbkdf2SHA256(password string, salt []byte, iterations int) []byte {
 if iterations < 1 { return nil }
 var counter [4]byte
 counter[3] = 1
 mac := hmac.New(sha256.New, []byte(password))
 _, _ = mac.Write(salt)
 _, _ = mac.Write(counter[:])
 u := mac.Sum(nil)
 result := append([]byte(nil), u...)
 for i := 1; i < iterations; i++ {
  mac = hmac.New(sha256.New, []byte(password))
  _, _ = mac.Write(u)
  u = mac.Sum(nil)
  for j := range result { result[j] ^= u[j] }
 }
 return result
}

func hashPassword(password string, salt []byte) []byte {
 return pbkdf2SHA256(password, salt, passwordIterations)
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
 if errors.Is(err,sql.ErrNoRows){ _ = hashPassword(password, []byte("meteor-forge-dummy-salt")); return Admin{},ErrInvalidCredentials };if err!=nil{return Admin{},fmt.Errorf("load administrator credentials: %w",err)}
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
 err:=s.db.QueryRowContext(ctx,`SELECT u.id,u.username,u.role,u.created_at,s.created_at,s.expires_at FROM admin_sessions s JOIN admin_users u ON u.id=s.admin_id WHERE s.token_hash=? AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday(?)`,sum[:],time.Now().UTC().Format(time.RFC3339Nano)).Scan(&v.Admin.ID,&v.Admin.Username,&v.Admin.Role,&adminCreated,&sessionCreated,&expires)
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

func validAdminRole(role string) bool {
 switch role { case "administrator", "viewer", "migration_operator", "shard_operator": return true; default: return false }
}

func (s *Store) CreateAdminUser(ctx context.Context, username, password, role string) (Admin, error) {
 username = strings.TrimSpace(username)
 if username == "" || len(username) > 128 { return Admin{}, ErrInvalidAdminInput }
 if !validAdminRole(role) { return Admin{}, ErrInvalidRole }
 encoded, err := encodePassword(password)
 if err != nil { return Admin{}, err }
 id, err := newID()
 if err != nil { return Admin{}, err }
 now := time.Now().UTC()
 stamp := now.Format(time.RFC3339Nano)
 _, err = s.db.ExecContext(ctx, `INSERT INTO admin_users(id, username, password_hash, role, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)`, id, username, encoded, role, stamp, stamp)
 if err != nil {
  if strings.Contains(strings.ToLower(err.Error()), "unique") { return Admin{}, ErrAdminConflict }
  return Admin{}, fmt.Errorf("create administrator account: %w", err)
 }
 return Admin{ID:id, Username:username, Role:role, CreatedAt:now}, nil
}

func (s *Store) ListAdminUsers(ctx context.Context) ([]Admin, error) {
 rows, err := s.db.QueryContext(ctx, "SELECT id, username, role, created_at FROM admin_users ORDER BY username COLLATE NOCASE")
 if err != nil { return nil, fmt.Errorf("list administrator accounts: %w", err) }
 defer rows.Close()
 result := make([]Admin, 0)
 for rows.Next() {
  var item Admin
  var created string
  if err := rows.Scan(&item.ID, &item.Username, &item.Role, &created); err != nil { return nil, fmt.Errorf("scan administrator account: %w", err) }
  item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
  if err != nil { return nil, fmt.Errorf("parse administrator creation time: %w", err) }
  result = append(result, item)
 }
 return result, rows.Err()
}

func (s *Store) UpdateAdminRole(ctx context.Context, id, role string) error {
 if !validAdminRole(role) { return ErrInvalidRole }
 tx, err := s.db.BeginTx(ctx, nil)
 if err != nil { return fmt.Errorf("begin administrator role update: %w", err) }
 defer tx.Rollback() //nolint:errcheck
 var current string
 if err := tx.QueryRowContext(ctx, "SELECT role FROM admin_users WHERE id = ?", id).Scan(&current); err != nil {
  if errors.Is(err, sql.ErrNoRows) { return ErrAdminNotFound }
  return fmt.Errorf("load administrator role: %w", err)
 }
 if current == "administrator" && role != "administrator" {
  var count int
  if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users WHERE role = 'administrator'").Scan(&count); err != nil { return err }
  if count <= 1 { return ErrLastAdministrator }
 }
 now := time.Now().UTC().Format(time.RFC3339Nano)
 if _, err := tx.ExecContext(ctx, "UPDATE admin_users SET role = ?, updated_at = ? WHERE id = ?", role, now, id); err != nil { return fmt.Errorf("update administrator role: %w", err) }
 if _, err := tx.ExecContext(ctx, "UPDATE admin_sessions SET revoked_at = ? WHERE admin_id = ? AND revoked_at IS NULL", now, id); err != nil { return fmt.Errorf("revoke sessions after role change: %w", err) }
 if err := tx.Commit(); err != nil { return fmt.Errorf("commit administrator role update: %w", err) }
 return nil
}

func (s *Store) DeleteAdminUser(ctx context.Context, id string) error {
 tx, err := s.db.BeginTx(ctx, nil)
 if err != nil { return fmt.Errorf("begin administrator deletion: %w", err) }
 defer tx.Rollback() //nolint:errcheck
 var role string
 if err := tx.QueryRowContext(ctx, "SELECT role FROM admin_users WHERE id = ?", id).Scan(&role); err != nil {
  if errors.Is(err, sql.ErrNoRows) { return ErrAdminNotFound }
  return fmt.Errorf("load administrator account: %w", err)
 }
 if role == "administrator" {
  var count int
  if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users WHERE role = 'administrator'").Scan(&count); err != nil { return err }
  if count <= 1 { return ErrLastAdministrator }
 }
 if _, err := tx.ExecContext(ctx, "DELETE FROM admin_sessions WHERE admin_id = ?", id); err != nil { return fmt.Errorf("delete administrator sessions: %w", err) }
 if _, err := tx.ExecContext(ctx, "DELETE FROM admin_users WHERE id = ?", id); err != nil { return fmt.Errorf("delete administrator account: %w", err) }
 if err := tx.Commit(); err != nil { return fmt.Errorf("commit administrator deletion: %w", err) }
 return nil
}
