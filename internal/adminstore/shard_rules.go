package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrShardRuleNotFound = errors.New("shard rule not found")
	ErrShardRuleConflict = errors.New("shard rule already exists for this data source and base table")
	ErrShardRuleInvalidInput = errors.New("invalid shard rule input")
	ErrShardRuleDataSourceNotFound = errors.New("shard rule data source not found")
	ErrShardRuleHasActiveDDLTasks = errors.New("shard rule has active DDL tasks")
)

// ShardRule stores a validated logical-table sharding strategy.
// It contains no connection credentials or target-database schema.
type ShardRule struct {
	ID             string    `json:"id"`
	DataSourceID   string    `json:"data_source_id"`
	DataSourceName string    `json:"data_source_name"`
	Driver         string    `json:"driver"`
	BaseTable      string    `json:"base_table"`
	ShardType      string    `json:"shard_type"`
	HashShards     int       `json:"hash_shards"`
	RangeSize      int64     `json:"range_size"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ShardRuleInput is the configuration accepted by the management API.
type ShardRuleInput struct {
	DataSourceID string `json:"data_source_id"`
	BaseTable    string `json:"base_table"`
	ShardType    string `json:"shard_type"`
	HashShards   int    `json:"hash_shards"`
	RangeSize    int64  `json:"range_size"`
}

func validateShardRuleInput(raw ShardRuleInput) (ShardRuleInput, error) {
	input := raw
	input.DataSourceID = strings.TrimSpace(input.DataSourceID)
	input.BaseTable = strings.TrimSpace(input.BaseTable)
	input.ShardType = strings.ToLower(strings.TrimSpace(input.ShardType))
	if input.DataSourceID == "" {
		return ShardRuleInput{}, fmt.Errorf("%w: data_source_id is required", ErrShardRuleInvalidInput)
	}
	if len(input.BaseTable) == 0 || len(input.BaseTable) > 40 {
		return ShardRuleInput{}, fmt.Errorf("%w: base_table must contain 1 to 40 characters", ErrShardRuleInvalidInput)
	}
	for i, r := range input.BaseTable {
		valid := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !valid || (i == 0 && r >= '0' && r <= '9') {
			return ShardRuleInput{}, fmt.Errorf("%w: base_table must start with a letter or underscore and contain only letters, digits and underscores", ErrShardRuleInvalidInput)
		}
	}
	switch input.ShardType {
	case "year", "quarter", "month", "week", "ten_day":
		input.HashShards = 16
		input.RangeSize = 1000000
	case "id_hash":
		if input.HashShards == 0 {
			input.HashShards = 16
		}
		if input.HashShards < 1 || input.HashShards > 4096 {
			return ShardRuleInput{}, fmt.Errorf("%w: hash_shards must be between 1 and 4096", ErrShardRuleInvalidInput)
		}
		input.RangeSize = 1000000
	case "id_range":
		if input.RangeSize == 0 {
			input.RangeSize = 1000000
		}
		if input.RangeSize < 1 {
			return ShardRuleInput{}, fmt.Errorf("%w: range_size must be positive", ErrShardRuleInvalidInput)
		}
		input.HashShards = 16
	default:
		return ShardRuleInput{}, fmt.Errorf("%w: unsupported shard_type", ErrShardRuleInvalidInput)
	}
	return input, nil
}

const shardRuleSelect = `SELECT r.id, r.data_source_id, d.name, d.driver, r.base_table,
	r.shard_type, r.hash_shards, r.range_size, r.created_at, r.updated_at
	FROM admin_shard_rules r JOIN admin_data_sources d ON d.id = r.data_source_id`

type shardRuleScanner interface { Scan(dest ...any) error }

func scanShardRule(row shardRuleScanner) (ShardRule, error) {
	var item ShardRule
	var created, updated string
	err := row.Scan(&item.ID, &item.DataSourceID, &item.DataSourceName, &item.Driver,
		&item.BaseTable, &item.ShardType, &item.HashShards, &item.RangeSize, &created, &updated)
	if err != nil { return ShardRule{}, err }
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil { return ShardRule{}, fmt.Errorf("parse shard rule creation time: %w", err) }
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil { return ShardRule{}, fmt.Errorf("parse shard rule update time: %w", err) }
	return item, nil
}

// CreateShardRule registers a rule for an existing target data source.
func (s *Store) CreateShardRule(ctx context.Context, raw ShardRuleInput) (ShardRule, error) {
	input, err := validateShardRuleInput(raw)
	if err != nil { return ShardRule{}, err }
	id, err := newID()
	if err != nil { return ShardRule{}, err }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return ShardRule{}, fmt.Errorf("begin shard rule creation: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	var sourceExists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM admin_data_sources WHERE id = ?", input.DataSourceID).Scan(&sourceExists); errors.Is(err, sql.ErrNoRows) {
		return ShardRule{}, ErrShardRuleDataSourceNotFound
	} else if err != nil {
		return ShardRule{}, fmt.Errorf("verify shard rule data source: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_shard_rules
		(id, data_source_id, base_table, shard_type, hash_shards, range_size, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, input.DataSourceID, input.BaseTable,
		input.ShardType, input.HashShards, input.RangeSize, now, now)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unique") { return ShardRule{}, ErrShardRuleConflict }
		return ShardRule{}, fmt.Errorf("create shard rule: %w", err)
	}
	if err := tx.Commit(); err != nil { return ShardRule{}, fmt.Errorf("commit shard rule creation: %w", err) }
	return s.GetShardRule(ctx, id)
}

// ListShardRules returns all registered rules without target connection details.
func (s *Store) ListShardRules(ctx context.Context) ([]ShardRule, error) {
	rows, err := s.db.QueryContext(ctx, shardRuleSelect+" ORDER BY d.name COLLATE NOCASE, r.base_table")
	if err != nil { return nil, fmt.Errorf("list shard rules: %w", err) }
	defer rows.Close()
	items := make([]ShardRule, 0)
	for rows.Next() {
		item, err := scanShardRule(rows)
		if err != nil { return nil, fmt.Errorf("scan shard rule: %w", err) }
		items = append(items, item)
	}
	if err := rows.Err(); err != nil { return nil, fmt.Errorf("iterate shard rules: %w", err) }
	return items, nil
}

// GetShardRule returns one registered rule.
func (s *Store) GetShardRule(ctx context.Context, id string) (ShardRule, error) {
	item, err := scanShardRule(s.db.QueryRowContext(ctx, shardRuleSelect+" WHERE r.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) { return ShardRule{}, ErrShardRuleNotFound }
	if err != nil { return ShardRule{}, fmt.Errorf("get shard rule: %w", err) }
	return item, nil
}

// UpdateShardRule replaces a rule's strategy while preserving its ID.
func (s *Store) UpdateShardRule(ctx context.Context, id string, raw ShardRuleInput) (ShardRule, error) {
	input, err := validateShardRuleInput(raw)
	if err != nil { return ShardRule{}, err }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return ShardRule{}, fmt.Errorf("begin shard rule update: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	var sourceExists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM admin_data_sources WHERE id = ?", input.DataSourceID).Scan(&sourceExists); errors.Is(err, sql.ErrNoRows) {
		return ShardRule{}, ErrShardRuleDataSourceNotFound
	} else if err != nil {
		return ShardRule{}, fmt.Errorf("verify shard rule data source: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE admin_shard_rules SET data_source_id = ?,
		base_table = ?, shard_type = ?, hash_shards = ?, range_size = ?, updated_at = ? WHERE id = ?`,
		input.DataSourceID, input.BaseTable, input.ShardType, input.HashShards, input.RangeSize,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") { return ShardRule{}, ErrShardRuleConflict }
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(ErrShardRuleHasActiveDDLTasks.Error())) { return ShardRule{}, ErrShardRuleHasActiveDDLTasks }
		return ShardRule{}, fmt.Errorf("update shard rule: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil { return ShardRule{}, err }
	if count == 0 { return ShardRule{}, ErrShardRuleNotFound }
	if err := tx.Commit(); err != nil { return ShardRule{}, fmt.Errorf("commit shard rule update: %w", err) }
	return s.GetShardRule(ctx, id)
}

// DeleteShardRule removes only the management rule; it never drops physical tables.
func (s *Store) DeleteShardRule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM admin_shard_rules WHERE id = ?", id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(ErrShardRuleHasActiveDDLTasks.Error())) { return ErrShardRuleHasActiveDDLTasks }
		return fmt.Errorf("delete shard rule: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil { return err }
	if count == 0 { return ErrShardRuleNotFound }
	return nil
}
