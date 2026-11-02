package tenant

import "context"

// HeaderResolver 从 Context 中解析租户 ID 的最简 Resolver 实现。
// 适用于上游中间件（HTTP Header / gRPC Metadata）已将 tenant_id 写入 Context 的场景。
type HeaderResolver struct{}

// 确保 HeaderResolver 实现了 Resolver 接口。
var _ Resolver = HeaderResolver{}

// Resolve 从 Context 中提取 tenant_id，缺失时返回 ErrTenantNotFound。
func (HeaderResolver) Resolve(ctx context.Context) (string, error) {
	return FromContext(ctx)
}

// TODO: 后续可提供 SubdomainResolver（按请求域名解析）、DBResolver（按元数据表校验）等实现。
