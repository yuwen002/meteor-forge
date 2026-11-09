package adminstore

import (
	"context"
	"database/sql"
	"fmt"
)

// ForeignKeyViolation describes a pre-existing row that violates a declared
// SQLite foreign-key relationship. RowID is invalid for WITHOUT ROWID tables.
type ForeignKeyViolation struct {
	Table  string
	RowID  sql.NullInt64
	Parent string
	FKID   int
}

// CheckForeignKeys reports existing metadata inconsistencies without changing
// or deleting data. SQLite's foreign_key_check also detects violations created
// by older application versions when foreign-key enforcement was disabled.
func (s *Store) CheckForeignKeys(ctx context.Context) ([]ForeignKeyViolation, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, fmt.Errorf("check metadata foreign-key integrity: %w", err)
	}
	defer rows.Close()

	violations := make([]ForeignKeyViolation, 0)
	for rows.Next() {
		var violation ForeignKeyViolation
		if err := rows.Scan(&violation.Table, &violation.RowID, &violation.Parent, &violation.FKID); err != nil {
			return nil, fmt.Errorf("read metadata foreign-key violation: %w", err)
		}
		violations = append(violations, violation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metadata foreign-key violations: %w", err)
	}
	return violations, nil
}
