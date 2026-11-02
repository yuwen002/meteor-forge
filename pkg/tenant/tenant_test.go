package tenant

import (
	"context"
	"errors"
	"testing"
)

func TestWithTenantIDRoundTrip(t *testing.T) {
	ctx := WithTenantID(context.Background(), "tenant_a")
	got, err := FromContext(ctx)
	if err != nil {
		t.Fatalf("FromContext failed: %v", err)
	}
	if got != "tenant_a" {
		t.Errorf("expected tenant_a, got %q", got)
	}
}

func TestFromContextMissing(t *testing.T) {
	// 未注入租户的上下文应返回 ErrTenantNotFound
	if _, err := FromContext(context.Background()); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}

	// 注入空字符串等同于缺失
	ctx := WithTenantID(context.Background(), "")
	if _, err := FromContext(ctx); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound for empty tenant id, got %v", err)
	}
}

func TestHeaderResolver(t *testing.T) {
	r := HeaderResolver{}

	// 上下文中有租户时正常解析
	ctx := WithTenantID(context.Background(), "tenant_b")
	got, err := r.Resolve(ctx)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if got != "tenant_b" {
		t.Errorf("expected tenant_b, got %q", got)
	}

	// 无租户时透传 ErrTenantNotFound
	if _, err := r.Resolve(context.Background()); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}
