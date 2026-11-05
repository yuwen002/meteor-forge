package main

import (
 "context"
 "errors"
 "testing"

 "github.com/yuwen002/meteor-forge/internal/adminstore"
)

const configTestKey = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func TestInitializeAdministratorRequiresPasswordAndNeverResets(t *testing.T) {
 store,err:=adminstore.Open(context.Background(),":memory:",configTestKey)
 if err!=nil { t.Fatal(err) }
 defer store.Close()
 t.Setenv("METEOR_ADMIN_USERNAME","first-admin")
 t.Setenv("METEOR_ADMIN_BOOTSTRAP_PASSWORD","")
 if err:=initializeAdministrator(context.Background(),store);err==nil { t.Fatal("expected missing bootstrap password error") }
 t.Setenv("METEOR_ADMIN_BOOTSTRAP_PASSWORD","first-strong-password-123")
 if err:=initializeAdministrator(context.Background(),store);err!=nil { t.Fatal(err) }
 t.Setenv("METEOR_ADMIN_BOOTSTRAP_PASSWORD","replacement-password-456")
 if err:=initializeAdministrator(context.Background(),store);err!=nil { t.Fatal(err) }
 if _,err:=store.AuthenticateAdmin(context.Background(),"first-admin","first-strong-password-123");err!=nil { t.Fatalf("original password was replaced: %v",err) }
 if _,err:=store.AuthenticateAdmin(context.Background(),"first-admin","replacement-password-456");!errors.Is(err,adminstore.ErrInvalidCredentials) { t.Fatalf("replacement password unexpectedly accepted: %v",err) }
}

func TestValidateAdminAddrRequiresLoopback(t *testing.T) {
 for _,addr:=range []string{"127.0.0.1:8081","localhost:8081","[::1]:8081"} {
  if err:=validateAdminAddr(addr);err!=nil{t.Errorf("%s should be accepted: %v",addr,err)}
 }
 for _,addr:=range []string{"0.0.0.0:8081","192.0.2.10:8081",":8081","bad-address"} {
  if err:=validateAdminAddr(addr);err==nil{t.Errorf("%s should be rejected",addr)}
 }
}
