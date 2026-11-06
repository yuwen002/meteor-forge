import axios from 'axios'

export const api = axios.create({
  baseURL: '/api/v1',
  timeout: 10_000,
  withCredentials: true,
  headers: { Accept: 'application/json' },
})

export interface HealthResponse {
  status: string
  service: string
  api: string
  timestamp: string
}

export async function getHealth(): Promise<HealthResponse> {
  const { data } = await api.get<HealthResponse>('/health')
  return data
}

export interface AdminSession { username: string; role: string; expires_at: string }

export async function loginAdmin(username: string, password: string): Promise<AdminSession> {
  const { data } = await api.post<AdminSession>('/auth/login', { username, password })
  return data
}

export async function getAdminSession(): Promise<AdminSession> {
  const { data } = await api.get<AdminSession>('/auth/session')
  return data
}

export async function logoutAdmin(): Promise<void> {
  await api.post('/auth/logout', {})
}

export interface AdminUser {
  id: string
  username: string
  role: 'administrator' | 'viewer' | 'migration_operator' | 'shard_operator'
  created_at: string
}

export async function listAdminUsers(): Promise<AdminUser[]> {
  const { data } = await api.get<{ items: AdminUser[] }>('/auth/users')
  return data.items
}

export async function createAdminUser(input: { username: string; password: string; role: AdminUser['role'] }): Promise<AdminUser> {
  const { data } = await api.post<AdminUser>('/auth/users', input)
  return data
}

export async function updateAdminUserRole(id: string, role: AdminUser['role']): Promise<void> {
  await api.put(`/auth/users/${encodeURIComponent(id)}/role`, { role })
}

export async function deleteAdminUser(id: string): Promise<void> {
  await api.delete(`/auth/users/${encodeURIComponent(id)}`, { data: {} })
}

export interface SecurityAuditEvent {
  id: number
  actor: string
  subject: string
  action: 'login' | 'logout' | 'password_change' | 'admin_create' | 'admin_role_update' | 'admin_delete'
  outcome: 'succeeded' | 'failed' | 'denied'
  request_source?: string
  details?: string
  occurred_at: string
}

export async function listSecurityAuditEvents(limit = 50, offset = 0): Promise<{ items: SecurityAuditEvent[]; total: number }> {
  const { data } = await api.get<{ items: SecurityAuditEvent[]; total: number }>('/auth/security-events', { params: { limit, offset } })
  return data
}
