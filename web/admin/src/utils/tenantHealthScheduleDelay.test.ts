import { describe, expect, it } from "vitest"
import {
  isTenantHealthScheduleDelayDelayed,
  tenantHealthScheduleDelayTagType,
  tenantHealthScheduleDelayBarWidth,
  TENANT_HEALTH_SCHEDULE_DELAY_THRESHOLD_MS,
} from "./tenantHealthScheduleDelay"

describe("租户巡检调度延迟判定", () => {
  it("将 30 秒作为延迟告警边界，等于阈值时不告警", () => {
    expect(TENANT_HEALTH_SCHEDULE_DELAY_THRESHOLD_MS).toBe(30_000)
    expect(isTenantHealthScheduleDelayDelayed(29_999)).toBe(false)
    expect(isTenantHealthScheduleDelayDelayed(30_000)).toBe(false)
    expect(isTenantHealthScheduleDelayDelayed(30_001)).toBe(true)
  })

  it("将延迟样本映射为对应的标签状态", () => {
    expect(tenantHealthScheduleDelayTagType(0)).toBe("info")
    expect(tenantHealthScheduleDelayTagType(30_000)).toBe("info")
    expect(tenantHealthScheduleDelayTagType(30_001)).toBe("danger")
  })
})

describe("租户巡检调度延迟趋势条宽度", () => {
  it("按最大样本归一化并限制最小可见宽度", () => {
    expect(tenantHealthScheduleDelayBarWidth(0, 0)).toBe(2)
    expect(tenantHealthScheduleDelayBarWidth(0, 10_000)).toBe(2)
    expect(tenantHealthScheduleDelayBarWidth(5_000, 10_000)).toBe(50)
    expect(tenantHealthScheduleDelayBarWidth(10_000, 10_000)).toBe(100)
    expect(tenantHealthScheduleDelayBarWidth(12_000, 10_000)).toBe(100)
  })
})
