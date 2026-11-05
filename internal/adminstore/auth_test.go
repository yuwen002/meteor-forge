package adminstore
import("context";"errors";"testing";"time")
const authTestKey="abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
func TestBootstrapAdminIsOneTimeAndPasswordIsHashed(t *testing.T){
 s,e:=Open(context.Background(),":memory:",authTestKey);if e!=nil{t.Fatal(e)};defer s.Close()
 a,e:=s.BootstrapAdmin(context.Background(),"root","a-strong-password-123");if e!=nil{t.Fatal(e)}
 if a.Role!="administrator"{t.Fatalf("role=%q",a.Role)}
 if _,e=s.BootstrapAdmin(context.Background(),"root","another-password-123");!errors.Is(e,ErrAdminAlreadyExists){t.Fatalf("second bootstrap: %v",e)}
 var encoded string;if e=s.db.QueryRow("SELECT password_hash FROM admin_users WHERE id=?",a.ID).Scan(&encoded);e!=nil{t.Fatal(e)}
 if encoded=="a-strong-password-123"||len(encoded)<80{t.Fatalf("password verifier not encoded: %q",encoded)}
 if _,e=s.AuthenticateAdmin(context.Background(),"root","wrong-password-123");!errors.Is(e,ErrInvalidCredentials){t.Fatalf("wrong password: %v",e)}
 if _,e=s.AuthenticateAdmin(context.Background(),"ROOT","a-strong-password-123");e!=nil{t.Fatal(e)}
}
func TestAdminSessionStoresOnlyHashAndSupportsRevocation(t *testing.T){
 s,e:=Open(context.Background(),":memory:",authTestKey);if e!=nil{t.Fatal(e)};defer s.Close()
 a,e:=s.BootstrapAdmin(context.Background(),"root","a-strong-password-123");if e!=nil{t.Fatal(e)}
 token,expiry,e:=s.CreateAdminSession(context.Background(),a.ID,12*time.Hour);if e!=nil{t.Fatal(e)}
 if len(token)<40||expiry.Before(time.Now()){t.Fatal("invalid token or expiry")}
 var stored []byte;if e=s.db.QueryRow("SELECT token_hash FROM admin_sessions LIMIT 1").Scan(&stored);e!=nil{t.Fatal(e)}
 if string(stored)==token{t.Fatal("raw token persisted")}
 if _,e=s.GetAdminSession(context.Background(),token);e!=nil{t.Fatal(e)}
 if e=s.RevokeAdminSession(context.Background(),token);e!=nil{t.Fatal(e)}
 if _,e=s.GetAdminSession(context.Background(),token);!errors.Is(e,ErrSessionNotFound){t.Fatalf("revoked session: %v",e)}
}
func TestPasswordChangeRevokesSessions(t *testing.T){
 s,e:=Open(context.Background(),":memory:",authTestKey);if e!=nil{t.Fatal(e)};defer s.Close()
 a,e:=s.BootstrapAdmin(context.Background(),"root","a-strong-password-123");if e!=nil{t.Fatal(e)}
 token,_,e:=s.CreateAdminSession(context.Background(),a.ID,12*time.Hour);if e!=nil{t.Fatal(e)}
 if e=s.ChangeAdminPassword(context.Background(),a.ID,"a-strong-password-123","a-new-strong-password-456");e!=nil{t.Fatal(e)}
 if _,e=s.GetAdminSession(context.Background(),token);!errors.Is(e,ErrSessionNotFound){t.Fatalf("old session: %v",e)}
 if _,e=s.AuthenticateAdmin(context.Background(),"root","a-new-strong-password-456");e!=nil{t.Fatal(e)}
}

func TestPBKDF2SHA256KnownVector(t *testing.T) {
 got:=hex.EncodeToString(pbkdf2SHA256("password",[]byte("salt"),1))
 const want="120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17"
 if got!=want{t.Fatalf("PBKDF2 vector=%s, want %s",got,want)}
}
func TestAdminSessionExpiryIsEnforced(t *testing.T) {
 s,err:=Open(context.Background(),":memory:",authTestKey);if err!=nil{t.Fatal(err)};defer s.Close()
 a,err:=s.BootstrapAdmin(context.Background(),"root","a-strong-password-123");if err!=nil{t.Fatal(err)}
 token,_,err:=s.CreateAdminSession(context.Background(),a.ID,12*time.Hour);if err!=nil{t.Fatal(err)}
 expired:=time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
 if _,err:=s.db.Exec("UPDATE admin_sessions SET expires_at=? WHERE admin_id=?",expired,a.ID);err!=nil{t.Fatal(err)}
 if _,err:=s.GetAdminSession(context.Background(),token);!errors.Is(err,ErrSessionNotFound){t.Fatalf("expired session error=%v",err)}
}
func TestRoleChangeRevokesExistingSessions(t *testing.T) {
 s,err:=Open(context.Background(),":memory:",authTestKey);if err!=nil{t.Fatal(err)};defer s.Close()
 root,err:=s.BootstrapAdmin(context.Background(),"root","a-strong-password-123");if err!=nil{t.Fatal(err)}
 other,err:=s.CreateAdminUser(context.Background(),"other","another-strong-password-456","viewer");if err!=nil{t.Fatal(err)}
 token,_,err:=s.CreateAdminSession(context.Background(),other.ID,12*time.Hour);if err!=nil{t.Fatal(err)}
 if err:=s.UpdateAdminRole(context.Background(),other.ID,"migration_operator");err!=nil{t.Fatal(err)}
 if _,err:=s.GetAdminSession(context.Background(),token);!errors.Is(err,ErrSessionNotFound){t.Fatalf("role change left session valid: %v",err)}
 if err:=s.DeleteAdminUser(context.Background(),root.ID);!errors.Is(err,ErrLastAdministrator){t.Fatalf("last administrator deletion error=%v",err)}
}
