export type TenantHealthLeaseStatus =
  | "held_by_this_instance"
  | "held_by_other_instance"
  | "expired_or_unclaimed"
  | "unavailable"
  | "not_configured"

const labels: Record<TenantHealthLeaseStatus, string> = {
  held_by_this_instance: "本实例持有",
  held_by_other_instance: "其他实例持有",
  expired_or_unclaimed: "当前无有效持有者",
  unavailable: "状态读取失败",
  not_configured: "未配置",
}

export function tenantHealthLeaseStatusLabel(status: TenantHealthLeaseStatus | string): string {
  return labels[status as TenantHealthLeaseStatus] ?? "未知状态"
}

export function tenantHealthLeaseStatusType(status: TenantHealthLeaseStatus | string): "success" | "warning" | "danger" | "info" {
  if (status === "held_by_this_instance") return "success"
  if (status === "held_by_other_instance") return "warning"
  if (status === "unavailable" || status === "not_configured") return "danger"
  return "info"
}
