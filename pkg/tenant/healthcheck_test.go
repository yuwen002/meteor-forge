package tenant

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakePinger func(context.Context) error

func (f fakePinger) PingContext(ctx context.Context) error { return f(ctx) }

func TestNewHealthCheckerValidatesBounds(t *testing.T) {
	tests := []struct {
		name string
		timeout time.Duration
		concurrency int
		wantErr bool
	}{
		{name:"defaults", wantErr:false},
		{name:"minimum", timeout:time.Millisecond, concurrency:1},
		{name:"maximum", timeout:maxHealthCheckTimeout, concurrency:maxHealthCheckWorkers},
		{name:"timeout too short", timeout:time.Microsecond, concurrency:1, wantErr:true},
		{name:"timeout too long", timeout:maxHealthCheckTimeout+time.Millisecond, concurrency:1, wantErr:true},
		{name:"concurrency too small", timeout:time.Second, concurrency:-1, wantErr:true},
		{name:"concurrency too large", timeout:time.Second, concurrency:maxHealthCheckWorkers+1, wantErr:true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHealthChecker(tt.timeout, tt.concurrency)
			if (err != nil) != tt.wantErr { t.Fatalf("error=%v, wantErr=%v", err, tt.wantErr) }
		})
	}
}

func TestHealthCheckerSuccessAndFailureAreNormalized(t *testing.T) {
	checker, err := NewHealthChecker(time.Second, 2)
	if err != nil { t.Fatal(err) }
	secret := "mysql://admin:super-secret@db.internal/app"
	tests := []struct {
		name string
		pinger Pinger
		wantStatus HealthStatus
		wantMessage string
	}{
		{name:"success", pinger:fakePinger(func(context.Context) error { return nil }), wantStatus:HealthStatusHealthy, wantMessage:"connection successful"},
		{name:"driver failure", pinger:fakePinger(func(context.Context) error { return errors.New(secret) }), wantStatus:HealthStatusUnhealthy, wantMessage:"connection check failed; verify the registered data source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checker.Check(context.Background(), "tenant-1", tt.pinger)
			if result.Status != tt.wantStatus { t.Fatalf("status=%q, want %q", result.Status, tt.wantStatus) }
			if result.Message != tt.wantMessage { t.Fatalf("message=%q, want %q", result.Message, tt.wantMessage) }
			if strings.Contains(result.Message, secret) || strings.Contains(result.Message, "super-secret") {
				t.Fatal("result exposed connection credentials")
			}
			if result.TenantID != "tenant-1" || result.CheckedAt.IsZero() { t.Fatalf("incomplete result: %+v", result) }
		})
	}
}

func TestHealthCheckerTimeoutAndCanceledContext(t *testing.T) {
	checker, err := NewHealthChecker(20*time.Millisecond, 1)
	if err != nil { t.Fatal(err) }
	result := checker.Check(context.Background(), "slow", fakePinger(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	if result.Status != HealthStatusUnhealthy || result.Message != "connection check timed out" {
		t.Fatalf("unexpected timeout result: %+v", result)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = checker.Check(ctx, "canceled", fakePinger(func(context.Context) error {
		t.Fatal("ping should not start for a canceled context")
		return nil
	}))
	if result.Message != "connection check canceled before it started" {
		t.Fatalf("unexpected canceled result: %+v", result)
	}
}

func TestHealthCheckerBoundsConcurrencyAndPreservesOrder(t *testing.T) {
	checker, err := NewHealthChecker(time.Second, 2)
	if err != nil { t.Fatal(err) }
	var active, maximum atomic.Int32
	pinger := fakePinger(func(ctx context.Context) error {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) { break }
		}
		select {
		case <-time.After(15*time.Millisecond):
		case <-ctx.Done():
			active.Add(-1)
			return ctx.Err()
		}
		active.Add(-1)
		return nil
	})
	targets := make([]HealthTarget, 8)
	for i := range targets { targets[i] = HealthTarget{TenantID:string(rune('a'+i)), DB:pinger} }
	results := checker.CheckMany(context.Background(), targets)
	if maximum.Load() > 2 { t.Fatalf("maximum concurrent checks=%d, want <=2", maximum.Load()) }
	for i, result := range results {
		if result.TenantID != targets[i].TenantID { t.Fatalf("result[%d] tenant=%q, want %q", i, result.TenantID, targets[i].TenantID) }
		if result.Status != HealthStatusHealthy { t.Fatalf("result[%d] unhealthy: %+v", i, result) }
	}
}
