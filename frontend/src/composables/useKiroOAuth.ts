import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { KiroTokenInfo } from '@/api/admin/kiro'

export function useKiroOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  const authUrl = ref('')
  const userCode = ref('')
  const sessionId = ref('')
  const region = ref('')
  const state = ref('')
  const authMode = ref<'device' | 'kiroide'>('device')
  const startUrl = ref('')
  const redirectUri = ref('')
  const loading = ref(false)
  const error = ref('')

  const resetState = () => {
    authUrl.value = ''
    userCode.value = ''
    sessionId.value = ''
    region.value = ''
    state.value = ''
    authMode.value = 'device'
    startUrl.value = ''
    redirectUri.value = ''
    loading.value = false
    error.value = ''
  }

  const startDeviceFlow = async (
    proxyId: number | null | undefined,
    selectedRegion?: string,
    selectedStartUrl?: string
  ): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    userCode.value = ''
    sessionId.value = ''
    region.value = ''
    state.value = ''
    authMode.value = 'device'
    startUrl.value = selectedStartUrl?.trim() || ''
    redirectUri.value = ''
    error.value = ''

    try {
      const payload: Record<string, unknown> = {}
      if (selectedRegion) payload.region = selectedRegion
      if (selectedStartUrl?.trim()) payload.start_url = selectedStartUrl.trim()
      if (proxyId) payload.proxy_id = proxyId

      const response = await adminAPI.kiro.startDeviceFlow(payload as any)
      authUrl.value = response.verification_uri
      userCode.value = response.user_code
      sessionId.value = response.session_id
      region.value = response.region
      startUrl.value = response.start_url || selectedStartUrl?.trim() || ''
      return true
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.kiro.failedToStart')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  const startKiroIDEAuth = async (
    proxyId: number | null | undefined,
    selectedRegion?: string,
    selectedStartUrl?: string,
    selectedRedirectUri?: string
  ): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    userCode.value = ''
    sessionId.value = ''
    region.value = ''
    state.value = ''
    authMode.value = 'kiroide'
    startUrl.value = selectedStartUrl?.trim() || ''
    redirectUri.value = selectedRedirectUri?.trim() || ''
    error.value = ''

    try {
      const payload: Record<string, unknown> = {}
      if (selectedRegion) payload.region = selectedRegion
      if (selectedStartUrl?.trim()) payload.start_url = selectedStartUrl.trim()
      if (selectedRedirectUri?.trim()) payload.redirect_uri = selectedRedirectUri.trim()
      if (proxyId) payload.proxy_id = proxyId

      const response = await adminAPI.kiro.startKiroIDEAuth(payload as any)
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      region.value = response.region
      state.value = response.state
      startUrl.value = response.start_url || selectedStartUrl?.trim() || ''
      redirectUri.value = response.redirect_uri || selectedRedirectUri?.trim() || ''
      return true
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.kiro.failedToStartKiroIDE')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  const pollDeviceFlow = async (
    activeSessionId: string,
    proxyId?: number | null
  ): Promise<KiroTokenInfo | null> => {
    if (!activeSessionId) {
      error.value = t('admin.accounts.oauth.kiro.missingSession')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = { session_id: activeSessionId }
      if (proxyId) payload.proxy_id = proxyId

      const response = await adminAPI.kiro.pollDeviceFlow(payload as any)
      if (!response.completed) {
        error.value =
          response.status === 'slow_down'
            ? t('admin.accounts.oauth.kiro.slowDown')
            : t('admin.accounts.oauth.kiro.authorizationPending')
        return null
      }
      return response.token_info || null
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.kiro.failedToPoll')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const validateRefreshToken = async (
    refreshToken: string,
    proxyId?: number | null,
    credentials?: Record<string, unknown>,
    selectedStartUrl?: string
  ): Promise<KiroTokenInfo | null> => {
    if (!refreshToken.trim()) {
      error.value = t('admin.accounts.oauth.kiro.pleaseEnterRefreshToken')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      return await adminAPI.kiro.refreshKiroToken(
        refreshToken.trim(),
        proxyId,
        credentials,
        selectedStartUrl?.trim()
      )
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.kiro.failedToValidateRT')
      return null
    } finally {
      loading.value = false
    }
  }

  const exchangeKiroIDEAuth = async (params: {
    callbackUrl?: string
    code?: string
    sessionId: string
    state?: string
    proxyId?: number | null
  }): Promise<KiroTokenInfo | null> => {
    if (!params.sessionId) {
      error.value = t('admin.accounts.oauth.kiro.missingSession')
      return null
    }
    if (!params.callbackUrl?.trim() && !params.code?.trim()) {
      error.value = t('admin.accounts.oauth.kiro.missingCallbackUrl')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = {
        session_id: params.sessionId
      }
      if (params.callbackUrl?.trim()) payload.callback_url = params.callbackUrl.trim()
      if (params.code?.trim()) payload.code = params.code.trim()
      if (params.state?.trim()) payload.state = params.state.trim()
      if (params.proxyId) payload.proxy_id = params.proxyId

      return await adminAPI.kiro.exchangeKiroIDEAuth(payload as any)
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.kiro.failedToExchangeKiroIDE')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const buildCredentials = (tokenInfo: KiroTokenInfo): Record<string, unknown> => {
    let expiresAt: string | undefined
    if (typeof tokenInfo.expires_at === 'number' && Number.isFinite(tokenInfo.expires_at)) {
      expiresAt = Math.floor(tokenInfo.expires_at).toString()
    } else if (typeof tokenInfo.expires_at === 'string' && tokenInfo.expires_at.trim()) {
      expiresAt = tokenInfo.expires_at.trim()
    }

    return {
      access_token: tokenInfo.access_token,
      refresh_token: tokenInfo.refresh_token,
      token_type: tokenInfo.token_type,
      expires_at: expiresAt,
      expires_in: tokenInfo.expires_in,
      client_id: tokenInfo.client_id,
      client_secret: tokenInfo.client_secret,
      profile_arn: tokenInfo.profile_arn,
      region: tokenInfo.region,
      idc_region: tokenInfo.idc_region,
      auth_method: tokenInfo.auth_method,
      start_url: tokenInfo.start_url,
      last_refresh: tokenInfo.last_refresh
    }
  }

  return {
    authUrl,
    userCode,
    sessionId,
    region,
    state,
    authMode,
    startUrl,
    redirectUri,
    loading,
    error,
    resetState,
    startDeviceFlow,
    startKiroIDEAuth,
    pollDeviceFlow,
    exchangeKiroIDEAuth,
    validateRefreshToken,
    buildCredentials
  }
}
