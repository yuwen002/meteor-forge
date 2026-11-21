export const TENANT_HEALTH_SCHEDULE_DELAY_THRESHOLD_MS = 30_000

export function isTenantHealthScheduleDelayDelayed(delayMs: number): boolean {
  return delayMs > TENANT_HEALTH_SCHEDULE_DELAY_THRESHOLD_MS
}

export function tenantHealthScheduleDelayTagType(delayMs: number): "danger" | "info" {
  return isTenantHealthScheduleDelayDelayed(delayMs) ? "danger" : "info"
}
