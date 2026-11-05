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
