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
  status: AccountStatus
  max_api_keys: number
  assigned_api_keys?: number
  quota_snapshot?: {
    remaining_percent?: number
    resets_at?: string
  } | null
  last_success_at?: string | null
  last_failure_at?: string | null
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
}

export interface Pool {
  id: string
  name: string
  provider: string
  strategy: string
  model_allowlist?: string[]
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
  input_tokens?: number
  output_tokens?: number
}

export interface UsageRecord {
  api_key_id: string
  employee_name?: string
  key_hint?: string
  usage_date?: string
  input_tokens: number
  output_tokens: number
}

export interface UsageResponse {
  items: UsageRecord[]
  input_tokens: number
  output_tokens: number
}
