/**
 * Admin Kiro OAuth API endpoints.
 */

import { apiClient } from '../client'

export interface KiroDeviceStartResult {
  session_id: string
  user_code: string
  verification_uri: string
  expires_in: number
  interval: number
  region: string
  start_url?: string
}

export interface KiroIDEStartResult {
  session_id: string
  auth_url: string
  state: string
  code_challenge: string
  redirect_uri: string
  redirect_from: string
  expires_in: number
  region: string
  start_url?: string
}

export interface KiroTokenInfo {
  access_token?: string
  refresh_token?: string
  expires_at?: number | string
  expires_in?: number
  token_type?: string
  client_id?: string
  client_secret?: string
  profile_arn?: string
  region?: string
  idc_region?: string
  auth_method?: string
  start_url?: string
  last_refresh?: string
  [key: string]: unknown
}

export interface KiroDevicePollResult {
  completed: boolean
  status?: string
  token_info?: KiroTokenInfo
}

export async function startDeviceFlow(payload: {
  region?: string
  start_url?: string
  proxy_id?: number
}): Promise<KiroDeviceStartResult> {
  const { data } = await apiClient.post<KiroDeviceStartResult>(
    '/admin/kiro/oauth/device/start',
    payload
  )
  return data
}

export async function startKiroIDEAuth(payload: {
  region?: string
  start_url?: string
  redirect_uri?: string
  redirect_from?: string
  proxy_id?: number
}): Promise<KiroIDEStartResult> {
  const { data } = await apiClient.post<KiroIDEStartResult>(
    '/admin/kiro/oauth/kiroide/start',
    payload
  )
  return data
}

export async function exchangeKiroIDEAuth(payload: {
  session_id: string
  code?: string
  state?: string
  callback_url?: string
  proxy_id?: number
}): Promise<KiroTokenInfo> {
  const { data } = await apiClient.post<KiroTokenInfo>(
    '/admin/kiro/oauth/kiroide/exchange',
    payload
  )
  return data
}

export async function cancelKiroIDEAuth(payload: {
  session_id: string
}): Promise<{ canceled: boolean }> {
  const { data } = await apiClient.post<{ canceled: boolean }>(
    '/admin/kiro/oauth/kiroide/cancel',
    payload
  )
  return data
}

export async function pollDeviceFlow(payload: {
  session_id: string
  proxy_id?: number
}): Promise<KiroDevicePollResult> {
  const { data } = await apiClient.post<KiroDevicePollResult>(
    '/admin/kiro/oauth/device/poll',
    payload
  )
  return data
}

export async function cancelDeviceFlow(payload: {
  session_id: string
}): Promise<{ canceled: boolean }> {
  const { data } = await apiClient.post<{ canceled: boolean }>(
    '/admin/kiro/oauth/device/cancel',
    payload
  )
  return data
}

export async function refreshKiroToken(
  refreshToken: string,
  proxyId?: number | null,
  credentials?: Record<string, unknown>,
  startUrl?: string
): Promise<KiroTokenInfo> {
  const payload: Record<string, unknown> = {
    refresh_token: refreshToken,
    credentials: credentials || {}
  }
  if (proxyId) payload.proxy_id = proxyId
  if (startUrl) payload.start_url = startUrl

  const { data } = await apiClient.post<KiroTokenInfo>(
    '/admin/kiro/oauth/refresh-token',
    payload
  )
  return data
}

export default {
  startDeviceFlow,
  pollDeviceFlow,
  cancelDeviceFlow,
  startKiroIDEAuth,
  exchangeKiroIDEAuth,
  cancelKiroIDEAuth,
  refreshKiroToken
}
