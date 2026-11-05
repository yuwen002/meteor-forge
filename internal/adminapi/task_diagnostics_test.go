package adminapi

import (
 "context"
 "errors"
 "strings"
 "testing"

 "github.com/yuwen002/meteor-forge/pkg/migration"
)

func TestSafeTaskFailureDiagnostics(t *testing.T) {
 cases := []struct{name,kind string; err error; code,hint string}{
  {"checksum","migration",migration.ErrChecksumMismatch,"MIGRATION_CHECKSUM_MISMATCH","do not edit an applied migration"},
  {"invalid version","migration",migration.ErrInvalidVersion,"MIGRATION_VERSION_INVALID","check the migration version"},
  {"authentication","migration",errors.New("pq: password authentication failed for user admin password=secret"),"DATABASE_AUTH_FAILED","verify the username, password"},
  {"permission","shard_ddl",errors.New("permission denied for relation orders"),"DATABASE_PERMISSION_DENIED","grant only the migration or DDL privileges"},
  {"network","migration",errors.New("dial tcp 127.0.0.1:3306: connect: connection refused"),"DATABASE_UNAVAILABLE","verify the host, port, network route"},
  {"syntax","migration",errors.New("SQLSTATE 42601: syntax error at or near SECRET_SQL"),"SQL_SYNTAX_ERROR","review the migration or generated DDL"},
  {"conflict","shard_ddl",errors.New("table orders_1 already exists"),"DATABASE_OBJECT_CONFLICT","inspect the target schema"},
  {"missing source","shard_ddl",errors.New("source physical table does not exist"),"SHARD_SOURCE_TABLE_MISSING","verify the shard rule"},
  {"generic DDL","shard_ddl",errors.New("driver internal error with dsn=secret"),"SHARD_DDL_FAILED","inspect the target table"},
  {"generic migration","migration",errors.New("driver internal error with dsn=secret"),"MIGRATION_EXECUTION_FAILED","inspect the task audit"},
  {"timeout","migration",context.DeadlineExceeded,"OPERATION_TIMEOUT","execution time limit"},
 }
 for _,tc:=range cases { t.Run(tc.name,func(t *testing.T){
  got:=safeTaskFailure(tc.kind,tc.err)
  if !strings.Contains(got,"["+tc.code+"]") || !strings.Contains(got,tc.hint) { t.Fatalf("diagnostic=%q, want code %s and hint %q",got,tc.code,tc.hint) }
  if strings.Contains(got,"secret") || strings.Contains(got,"SECRET_SQL") || strings.Contains(got,"127.0.0.1") { t.Fatalf("diagnostic leaked raw error details: %q",got) }
 }) }
}
