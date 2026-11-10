import { describe, expect, it } from 'vitest'
import taskCenterView from '../views/TaskCenterView.vue?raw'

describe('迁移任务人工处置界面', () => {
  it('只在中断迁移任务详情中展示处置面板', () => {
    expect(taskCenterView).toContain("selected.kind === 'migration' && selected.status === 'interrupted'")
    expect(taskCenterView).toContain('canSubmitMigrationDisposition(isAdministrator, selected.status, disposition)')
    expect(taskCenterView).toContain('当前账号无管理员处置权限或任务已存在处置记录')
  })

  it('提供三种明确的人工决定，并要求处理说明和风险确认', () => {
    expect(taskCenterView).toContain('确认已完成')
    expect(taskCenterView).toContain('授权后续重新提交')
    expect(taskCenterView).toContain('终止并保留现场')
    expect(taskCenterView).toContain('处理说明（1–2000 字）')
    expect(taskCenterView).toContain('请输入完整的风险确认语句')
  })

  it('读取并展示已保存记录，重复处置后不再显示提交表单', () => {
    expect(taskCenterView).toContain("'/migration-tasks/' + encodeURIComponent(task.id) + '/disposition'")
    expect(taskCenterView).toContain('disposition.actor')
    expect(taskCenterView).toContain('disposition.operator_note')
    expect(taskCenterView).toContain('每个任务只能记录一次')
  })

  it('明确处置不会改变任务状态或执行 SQL', () => {
    expect(taskCenterView).toContain('不会修改原任务状态、执行 SQL 或自动创建重试任务')
    expect(taskCenterView).toContain('不会执行或重放 SQL')
  })
})
