package tenant

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultHealthCheckTimeout = 5 * time.Second
	maxHealthCheckTimeout     = 30 * time.Second
	defaultHealthCheckWorkers = 4
	maxHealthCheckWorkers     = 32
)

// Pinger is implemented by database/sql.DB and can be faked in unit tests.
type Pinger interface {
	PingContext(context.Context) error
}

// HealthStatus is the public result of a tenant connection check.
type HealthStatus string

const (
	HealthStatusHealthy HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// HealthResult contains only safe operational metadata; it never includes a DSN
// or the underlying database driver's error text.
type HealthResult struct {
	TenantID   string        `json:"tenant_id"`
	Status     HealthStatus  `json:"status"`
	CheckedAt  time.Time     `json:"checked_at"`
	DurationMS int64         `json:"duration_ms"`
	Message    string        `json:"message"`
}

// HealthChecker bounds both the duration and concurrency of target database pings.
type HealthChecker struct {
	timeout time.Duration
	slots   chan struct{}
	now     func() time.Time
}

// NewHealthChecker creates a checker with safe defaults when zero values are
// supplied. Values outside the supported bounds are rejected.
func NewHealthChecker(timeout time.Duration, concurrency int) (*HealthChecker, error) {
	if timeout == 0 {
		timeout = defaultHealthCheckTimeout
	}
	if timeout < time.Millisecond || timeout > maxHealthCheckTimeout {
		return nil, fmt.Errorf("health check timeout must be between 1ms and %s", maxHealthCheckTimeout)
	}
	if concurrency == 0 {
		concurrency = defaultHealthCheckWorkers
	}
	if concurrency < 1 || concurrency > maxHealthCheckWorkers {
		return nil, fmt.Errorf("health check concurrency must be between 1 and %d", maxHealthCheckWorkers)
	}
	return &HealthChecker{
		timeout: timeout,
		slots:   make(chan struct{}, concurrency),
		now:     time.Now,
	}, nil
}

// Check waits for an available worker slot, then performs one bounded ping.
// Driver errors are deliberately normalized before they can reach an API client.
func (c *HealthChecker) Check(ctx context.Context, tenantID string, db Pinger) HealthResult {
	started := c.now().UTC()
	result := HealthResult{
		TenantID: tenantID,
		Status: HealthStatusUnhealthy,
		CheckedAt: started,
		Message: "connection check failed; verify the registered data source",
	}
	if c == nil || db == nil {
		result.Message = "connection check is unavailable"
		return result
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		result.Message = "connection check canceled before it started"
		result.DurationMS = c.now().Sub(started).Milliseconds()
		return result
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	err := db.PingContext(checkCtx)
	result.DurationMS = c.now().Sub(started).Milliseconds()
	if err == nil {
		result.Status = HealthStatusHealthy
		result.Message = "connection successful"
		return result
	}
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
		result.Message = "connection check timed out"
	} else if errors.Is(checkCtx.Err(), context.Canceled) {
		result.Message = "connection check canceled"
	}
	return result
}

// CheckMany checks each item with the same concurrency budget. The output order
// matches the input order, and one failure does not prevent checks of other items.
func (c *HealthChecker) CheckMany(ctx context.Context, items []HealthTarget) []HealthResult {
	results := make([]HealthResult, len(items))
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = c.Check(ctx, items[index].TenantID, items[index].DB)
		}(i)
	}
	wg.Wait()
	return results
}

// HealthTarget is an internal input to CheckMany and is never serialized.
type HealthTarget struct {
	TenantID string
	DB       Pinger
}
