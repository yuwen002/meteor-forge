package adminapi

import (
 "context"
 "errors"
 "strings"

 "github.com/yuwen002/meteor-forge/pkg/migration"
)

// safeTaskFailure maps an internal execution error to a stable, actionable
// message. It never returns the driver's raw error because it may contain
// DSNs, SQL fragments, credentials, or server details.
func safeTaskFailure(kind string, err error) string {
 if err == nil { return "[OPERATION_FAILED] The operation failed without an error detail. Next step: inspect the task audit and target schema before retrying." }
 if errors.Is(err, context.DeadlineExceeded) { return "[OPERATION_TIMEOUT] The operation exceeded its execution time limit. Next step: inspect the target schema or table for partially completed changes before retrying." }
 if errors.Is(err, migration.ErrChecksumMismatch) { return "[MIGRATION_CHECKSUM_MISMATCH] An applied migration script differs from the version recorded in the database. Next step: restore the original applied script or create a new migration; do not edit an applied migration in place." }
 if errors.Is(err, migration.ErrInvalidVersion) { return "[MIGRATION_VERSION_INVALID] The requested migration version is not available. Next step: check the migration version and the scripts installed on the server." }
 message := strings.ToLower(err.Error())
 if strings.Contains(message,"authentication failed") || strings.Contains(message,"password authentication failed") || strings.Contains(message,"access denied for user") || strings.Contains(message,"invalid authorization specification") {
  return "[DATABASE_AUTH_FAILED] The target database rejected the configured credentials. Next step: verify the username, password, authentication method, and account status in the data source settings."
 }
 if strings.Contains(message,"permission denied") || strings.Contains(message,"insufficient privilege") || strings.Contains(message,"not authorized") {
  return "[DATABASE_PERMISSION_DENIED] The configured database account lacks a required permission. Next step: grant only the migration or DDL privileges required by this operation, then retry."
 }
 if strings.Contains(message,"connection refused") || strings.Contains(message,"no such host") || strings.Contains(message,"server selection") || strings.Contains(message,"connection reset") || strings.Contains(message,"dial tcp") {
  return "[DATABASE_UNAVAILABLE] The target database could not be reached. Next step: verify the host, port, network route, database availability, and firewall rules."
 }
 if strings.Contains(message,"syntax error") || strings.Contains(message,"sqlstate 42601") {
  return "[SQL_SYNTAX_ERROR] The database rejected a SQL statement. Next step: review the migration or generated DDL for syntax supported by the selected database version."
 }
 if strings.Contains(message,"no such file") || strings.Contains(message,"file does not exist") || strings.Contains(message,"cannot find the path") {
  return "[MIGRATION_SCRIPT_UNAVAILABLE] A required migration script or file is unavailable. Next step: verify the configured migration directory and ensure both up/down scripts are deployed."
 }
 if kind == "shard_ddl" {
  if strings.Contains(message,"source physical table does not exist") { return "[SHARD_SOURCE_TABLE_MISSING] The source physical table does not exist. Next step: verify the shard rule and source table name in the target database." }
  if strings.Contains(message,"target physical table already exists") { return "[SHARD_TARGET_TABLE_EXISTS] The target physical table already exists. Next step: inspect the existing table; do not retry the clone until its ownership and structure are confirmed." }
  return "[SHARD_DDL_FAILED] The physical table DDL operation failed. Next step: inspect the target table and task audit; verify the selected driver and required DDL privileges before retrying."
 }
 if strings.Contains(message,"already exists") || strings.Contains(message,"duplicate key") || strings.Contains(message,"unique constraint") {
  return "[DATABASE_OBJECT_CONFLICT] The operation conflicts with an existing database object or record. Next step: inspect the target schema and migration history before changing or retrying the operation."
 }
 return "[MIGRATION_EXECUTION_FAILED] The migration operation failed. Next step: inspect the task audit, migration history, and target schema before retrying."
}
