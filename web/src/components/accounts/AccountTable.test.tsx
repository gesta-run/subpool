import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { ProviderAccount } from '../../types'
import { AccountTable } from './AccountTable'

const baseAccount: ProviderAccount = {
  id: 'account-1',
  provider: 'codex',
  credential_type: 'subscription',
  display_name: 'Team account',
  status: 'active',
  health_status: 'healthy',
  quota_snapshot: {
    weekly: {
      used_percent: 25,
      remaining_percent: 75,
      window_seconds: 604800,
      reset_at: 1900000000,
    },
  },
}

function renderTable(account: ProviderAccount) {
  const noop = vi.fn()
  return render(<AccountTable
    accounts={[account]}
    busyID=""
    resetBusyID=""
    resetStates={{}}
    onModels={noop}
    onRefresh={noop}
    onResetLoad={noop}
    onReset={noop}
    onFastMode={noop}
    onToggle={noop}
    onRemove={noop}
  />)
}

describe('AccountTable health states', () => {
  it('shows the account-level Fast mode state', () => {
    renderTable({ ...baseAccount, fast_mode_enabled: true })
    expect(screen.getByRole('button', { name: 'Disable Fast mode for Team account' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('does not offer Fast mode for OpenAI-compatible accounts', () => {
    renderTable({ ...baseAccount, provider: 'openai_compatible', credential_type: 'api_key' })
    expect(screen.queryByRole('button', { name: /Fast mode/ })).not.toBeInTheDocument()
  })

  it('shows an active account as degraded while a failed probe is being retried', () => {
    renderTable({
      ...baseAccount,
      consecutive_health_failures: 1,
      last_health_error_code: 'provider_unavailable',
    })

    expect(screen.getByText('degraded')).toBeInTheDocument()
    expect(screen.getByText('Routing enabled while retrying')).toBeInTheDocument()
    expect(screen.getByText('last known weekly capacity')).toBeInTheDocument()
    expect(screen.getByText('provider unavailable')).toBeInTheDocument()
  })

  it('shows routing as suspended after repeated probe failures', () => {
    renderTable({
      ...baseAccount,
      health_status: 'unhealthy',
      consecutive_health_failures: 3,
      last_health_error_code: 'provider_unavailable',
    })

    expect(screen.getByText('unhealthy')).toBeInTheDocument()
    expect(screen.getByText('Routing suspended')).toBeInTheDocument()
    expect(screen.getByText('last known weekly capacity')).toBeInTheDocument()
  })
})
