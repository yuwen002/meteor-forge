import axios from 'axios'

export const api = axios.create({
  baseURL: '/api/v1',
  timeout: 10_000,
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
