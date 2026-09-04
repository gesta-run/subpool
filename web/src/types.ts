export type AccountStatus =
  | 'active'
  | 'cooling_down'
  | 'exhausted'
  | 'auth_failed'
  | 'disabled'

export interface ProviderAccount {
  id: string
  provider: string
  credential_type?: string
  display_name: string
  email?: string
  status: AccountStatus
  fast_mode_enabled?: boolean
  health_status?: 'unknown' | 'healthy' | 'unhealthy'
  last_checked_at?: string | null
  last_health_error_code?: string | null
  consecutive_health_failures?: number
  assigned_api_keys?: number
  quota_snapshot?: {
    plan_type?: string
    remaining_percent?: number
    resets_at?: string
    five_hour?: QuotaWindow
    weekly?: QuotaWindow
  } | null
  last_success_at?: string | null
  last_failure_at?: string | null
}

export interface ProviderModel {
  id: string
  display_name?: string
  description?: string
  is_default?: boolean
  reasoning_efforts?: string[]
  input_modalities?: string[]
}

export interface QuotaWindow {
  used_percent: number
  remaining_percent: number
  window_seconds: number
  reset_at: number
}

export interface CodexResetCredit {
	id: string
	reset_type: string
	status: string
	granted_at: number
	expires_at: number | null
	title?: string
	description?: string
}

export interface CodexResetCredits {
	available_count: number
	credits: CodexResetCredit[] | null
}

export interface CodexResetCreditsResponse {
	reset_credits: CodexResetCredits | null
}

export interface CodexResetConsumeResponse {
	outcome: 'reset' | 'alreadyRedeemed' | 'nothingToReset' | 'noCredit'
	reset_credits: CodexResetCredits | null
}

export interface GlobalSettings {
  max_api_keys_per_account: number
  updated_at?: string
}

export interface PoolAccount {
  pool_id?: string
  provider_account_id: string
  display_name?: string
  status?: AccountStatus
  enabled?: boolean
  weight?: number
  priority?: number
}

export interface Pool {
  id: string
  name: string
  provider: string
  accounts?: PoolAccount[]
  account_count?: number
  enabled?: boolean
  created_at?: string
}

export interface APIKeyRecord {
  id: string
  pool_id: string
  provider_account_id?: string
  pool_name?: string
  employee_name: string
  key_hint: string
  scopes?: string[]
  expires_at?: string | null
  revoked_at?: string | null
  last_used_at?: string | null
  created_at: string
}

export interface UsageRecord {
  api_key_id: string
  employee_name?: string
  key_hint?: string
  model?: string
  usage_date?: string
  input_tokens: number
  output_tokens: number
}

export interface UsageResponse {
  items: UsageRecord[]
  input_tokens: number
  output_tokens: number
}
