import { describe, expect, it } from "vitest"
import { tenantHealthLeaseStatusLabel, tenantHealthLeaseStatusType } from "./tenantHealthLease"

describe("tenant health lease status", () => {
  it.each([
    ["held_by_this_instance", "本实例持有", "success"],
    ["held_by_other_instance", "其他实例持有", "warning"],
    ["expired_or_unclaimed", "当前无有效持有者", "info"],
    ["unavailable", "状态读取失败", "danger"],
    ["not_configured", "未配置", "danger"],
  ])("maps %s to a safe label and status", (status, label, type) => {
    expect(tenantHealthLeaseStatusLabel(status)).toBe(label)
    expect(tenantHealthLeaseStatusType(status)).toBe(type)
  })

  it("uses a neutral fallback for unknown statuses", () => {
    expect(tenantHealthLeaseStatusLabel("future_state")).toBe("未知状态")
    expect(tenantHealthLeaseStatusType("future_state")).toBe("info")
  })
})
