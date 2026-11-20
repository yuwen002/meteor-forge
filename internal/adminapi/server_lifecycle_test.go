package adminapi

import (
	"errors"
	"testing"
)

func TestCloseBackgroundWorkersAttemptsEveryManager(t *testing.T) {
	schedulerErr := errors.New("scheduler did not stop")
	shardDDLErr := errors.New("shard DDL worker did not stop")
	migrationErr := errors.New("migration worker did not stop")
	shardDDLClosed := false
	migrationsClosed := false

	err := closeBackgroundWorkers(
		func() error { return schedulerErr },
		func() error {
			shardDDLClosed = true
			return shardDDLErr
		},
		func() error {
			migrationsClosed = true
			return migrationErr
		},
	)

	if !shardDDLClosed || !migrationsClosed {
		t.Fatalf("manager shutdown calls: shard DDL=%v migrations=%v; both must be attempted", shardDDLClosed, migrationsClosed)
	}
	if !errors.Is(err, schedulerErr) || !errors.Is(err, shardDDLErr) || !errors.Is(err, migrationErr) {
		t.Fatalf("combined close error=%v, want both manager errors", err)
	}
}

func TestCloseBackgroundWorkersAllowsMissingManagers(t *testing.T) {
	if err := closeBackgroundWorkers(nil, nil, nil); err != nil {
		t.Fatalf("close absent managers: %v", err)
	}
}
