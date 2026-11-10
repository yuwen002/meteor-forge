package adminstore

import "strings"

// metadataSQLiteDSN configures connection-local PRAGMAs for every physical
// SQLite connection opened by database/sql. Executing PRAGMA once through
// *sql.DB only configures whichever pooled connection happens to be borrowed.
func metadataSQLiteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_busy_timeout=5000&_foreign_keys=on"
}
