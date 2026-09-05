import { apiClient } from '../client'
import type { AlertRule } from './ops'

export type NetworkStatus = 'ok' | 'disabled' | 'unconfigured' | 'collecting' | 'down' | 'missing' | 'error' | 'stale'
export interface NetworkLink {
  id: 'public' | 'private'
  enabled: boolean
  device: string
  rx_capacity_mbps: number
  tx_capacity_mbps: number
}
export interface NetworkSettings {
  enabled: boolean
  links: NetworkLink[]
  raw_retention_hours: number
  minute_retention_days: number
  hourly_retention_days: number
}
export interface NetworkDevice { name: string; up: boolean; speed_mbps: number }
export interface NetworkSample extends NetworkLink {
  start: string
  end: string
  status: NetworkStatus
  rx_mbps: number | null
  tx_mbps: number | null
  rx_bytes: number
  tx_bytes: number
  valid_seconds: number
}
export interface NetworkOverview {
  server_id: string
  collected_at: string
  status: NetworkStatus
  devices: NetworkDevice[]
  links: NetworkSample[]
}
export interface NetworkBucket {
  bucket_start: string
  bucket_seconds: number
  rx_bytes: number
  tx_bytes: number
  valid_seconds: number
  rx_avg_mbps: number | null
  tx_avg_mbps: number | null
  rx_peak_mbps: number | null
  tx_peak_mbps: number | null
  rx_capacity_mbps: number
  tx_capacity_mbps: number
  complete: boolean
}
export interface NetworkTrend { bucket_seconds: number; points: NetworkBucket[]; summary: NetworkBucket }
export interface NetworkSettingsResponse {
  settings: NetworkSettings
  server_id: string
  source_configured: boolean
  devices: NetworkDevice[]
  alert_presets: AlertRule[]
}
export interface NetworkRange { time_range?: string; start_time?: string; end_time?: string; link: string }

export const opsNetworkAPI = {
  async overview(signal?: AbortSignal) {
    return (await apiClient.get<NetworkOverview>('/admin/ops/network/overview', { signal })).data
  },
  async trend(params: NetworkRange, signal?: AbortSignal) {
    return (await apiClient.get<NetworkTrend>('/admin/ops/network/trend', { params, signal })).data
  },
  async settings(signal?: AbortSignal) {
    return (await apiClient.get<NetworkSettingsResponse>('/admin/ops/network/settings', { signal })).data
  },
  async updateSettings(settings: NetworkSettings) {
    return (await apiClient.put<NetworkSettingsResponse>('/admin/ops/network/settings', settings)).data
  }
}
