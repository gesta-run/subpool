import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { ResetCreditState } from '../../hooks/useResetCredits'
import type { ProviderAccount } from '../../types'
import { AccountTable } from './AccountTable'

const baseAccount: ProviderAccount = {
  id: 'account-1',
  provider: 'codex',
  credential_type: 'subscription',
  display_name: 'Team account',
  status: 'active',
  health_status: 'healthy',
  quota_checked_at: '2026-09-10T08:00:00Z',
  quota_snapshot: {
    weekly: {
      used_percent: 25,
      remaining_percent: 75,
      window_seconds: 604800,
      reset_at: 1900000000,
    },
  },
}

function renderTable(account: ProviderAccount, resetStates: Record<string, ResetCreditState> = {}) {
  const noop = vi.fn()
  return render(<AccountTable
    accounts={[account]}
    busyID=""
    resetBusyID=""
    resetStates={resetStates}
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
  it('groups availability and the latest check in the health column', () => {
    renderTable({ ...baseAccount, last_checked_at: '2026-09-10T08:00:00Z' })

    expect(screen.getByRole('columnheader', { name: 'Health' })).toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: 'Last checked' })).not.toBeInTheDocument()
    expect(screen.getByText(/^Checked /)).toBeInTheDocument()
  })

  it('shows both Codex subscription limit windows', () => {
    renderTable({
      ...baseAccount,
      quota_snapshot: {
        five_hour: {
          used_percent: 40,
          remaining_percent: 60,
          window_seconds: 18000,
          reset_at: 1899500000,
        },
        weekly: baseAccount.quota_snapshot!.weekly,
      },
    })

    expect(screen.getByText('5-hour capacity')).toBeInTheDocument()
    expect(screen.getByText('weekly capacity')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: /5-hour usage remaining/i })).toHaveAttribute('aria-valuenow', '60')
    expect(screen.getByRole('progressbar', { name: /weekly usage remaining/i })).toHaveAttribute('aria-valuenow', '75')
  })

  it('shows a 5-hour-only Codex Plus limit', () => {
    renderTable({
      ...baseAccount,
      quota_snapshot: {
        plan_type: 'plus',
        five_hour: {
          used_percent: 25,
          remaining_percent: 75,
          window_seconds: 18000,
          reset_at: 1899500000,
        },
      },
    })

    expect(screen.getByText('5-hour capacity')).toBeInTheDocument()
    expect(screen.queryByText('Usage unavailable')).not.toBeInTheDocument()
  })

  it('shows Codex AI credits alongside subscription capacity', () => {
    const { container } = renderTable({
      ...baseAccount,
      quota_snapshot: {
        ...baseAccount.quota_snapshot,
        credits: {
          has_credits: true,
          unlimited: false,
          balance: '499',
        },
      },
    })

    expect(screen.getByText('weekly capacity')).toBeInTheDocument()
    expect(screen.getByText('499')).toBeInTheDocument()
    expect(container.querySelector('.capacity__heading')).toHaveTextContent('weekly capacity with 499 AI credits')
    expect(screen.queryByText('Available credit balance')).not.toBeInTheDocument()
  })

  it('preserves a reported zero Codex AI credit balance', () => {
    const { container } = renderTable({
      ...baseAccount,
      quota_snapshot: {
        ...baseAccount.quota_snapshot,
        credits: {
          has_credits: true,
          unlimited: false,
          balance: '0',
        },
      },
    })

    expect(container.querySelector('.capacity__heading')).toHaveTextContent('weekly capacity with 0 AI credits')
    expect(container.querySelector('.capacity__heading')).not.toHaveTextContent('Available AI credits')
  })

  it('keeps AI credits with capacity and expiry with reset credits', () => {
    const { container } = renderTable({
      ...baseAccount,
      last_quota_error_code: 'quota_probe_rate_limited',
      quota_snapshot: {
        ...baseAccount.quota_snapshot,
        credits: {
          has_credits: true,
          unlimited: false,
          balance: '48200',
        },
      },
    }, {
      'account-1': {
        loading: false,
        notice: 'Full reset applied. Quota has been refreshed.',
        data: {
          available_count: 3,
          credits: [{
            id: 'reset-1',
            reset_type: 'full',
            status: 'available',
            granted_at: 1899000000,
            expires_at: 1900000000,
          }],
        },
      },
    })

    const heading = container.querySelector('.capacity__heading')
    const reset = container.querySelector('.reset-credit')
    expect(heading).toHaveTextContent('weekly capacity with 48,200 AI credits')
    expect(reset).not.toHaveTextContent('AI credits')
    expect(screen.getByText('3 resets available')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reset credits' })).toBeInTheDocument()
    expect(screen.getByText(/^Next reset expires /)).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Full reset applied')
  })

  it('shows remaining GitHub Copilot subscription capacity in AI credits', () => {
    renderTable({
      ...baseAccount,
      provider: 'copilot',
      credential_type: 'subscription_oauth',
      quota_snapshot: {
        plan_type: 'pro_plus',
        usage_allowed: true,
        credits: {
          used: 375,
          entitlement: 1500,
          remaining: 1125,
          remaining_percent: 75,
          unlimited: false,
          overage_permitted: false,
          overage_count: 0,
          reset_at: 1790812800,
        },
      },
    })

    expect(screen.getByText('1,125 / 1,500')).toBeInTheDocument()
    expect(screen.getByText('AI credits')).toBeInTheDocument()
    expect(screen.getByText('Additional usage disabled')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: /AI credits remaining/i })).toHaveAttribute('aria-valuenow', '75')
    expect(screen.getByRole('button', { name: 'Refresh AI credits for Team account' })).toBeInTheDocument()
    expect(screen.queryByText('Usage unavailable')).not.toBeInTheDocument()
  })

  it('shows the account-level Fast mode state', () => {
    renderTable({ ...baseAccount, fast_mode_enabled: true })
    expect(screen.getByRole('button', { name: 'Disable Fast mode for Team account' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('does not offer Fast mode for OpenAI-compatible accounts', () => {
    renderTable({ ...baseAccount, provider: 'openai_compatible', credential_type: 'api_key' })
    expect(screen.queryByRole('button', { name: /Fast mode/ })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Check health for Team account' })).toBeInTheDocument()
  })

  it('shows an active account as degraded while a failed probe is being retried', () => {
    renderTable({
      ...baseAccount,
      consecutive_health_failures: 1,
      last_health_error_code: 'provider_unavailable',
      last_quota_error_code: 'provider_unavailable',
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
      last_quota_error_code: 'provider_unavailable',
    })

    expect(screen.getByText('unhealthy')).toBeInTheDocument()
    expect(screen.getByText('Routing suspended')).toBeInTheDocument()
    expect(screen.getByText('last known weekly capacity')).toBeInTheDocument()
  })

  it('labels cached capacity as last known when quota probing is rate limited', () => {
    renderTable({
      ...baseAccount,
      health_status: 'unknown',
      last_health_error_code: 'quota_probe_rate_limited',
      last_quota_error_code: 'quota_probe_rate_limited',
    })

    expect(screen.getByText('unchecked')).toBeInTheDocument()
    expect(screen.getByText('last known weekly capacity')).toBeInTheDocument()
    expect(screen.getByText('quota probe rate limited')).toBeInTheDocument()
  })

  it('keeps cached capacity marked as last known after a successful model request', () => {
    renderTable({
      ...baseAccount,
      health_status: 'healthy',
      last_success_at: '2026-09-10T08:01:00Z',
      last_quota_error_code: 'quota_probe_rate_limited',
    })

    expect(screen.getByText('healthy')).toBeInTheDocument()
    expect(screen.getByText('Routing enabled')).toBeInTheDocument()
    expect(screen.getByText('last known weekly capacity')).toBeInTheDocument()
  })

  it('keeps reported capacity visible when the provider blocks usage', () => {
    renderTable({
      ...baseAccount,
      status: 'exhausted',
    })

    expect(screen.getByText('75%')).toBeInTheDocument()
    expect(screen.getByText('reported weekly capacity')).toBeInTheDocument()
    expect(screen.getByText('Routing suspended')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: /weekly usage remaining/i })).toHaveAttribute('aria-valuenow', '75')
  })

  it('shows each reported window while suspended by the 5-hour limit', () => {
    renderTable({
      ...baseAccount,
      quota_snapshot: {
        usage_allowed: false,
        five_hour: {
          used_percent: 100,
          remaining_percent: 0,
          window_seconds: 18000,
          reset_at: 1899500000,
        },
        weekly: {
          used_percent: 40,
          remaining_percent: 60,
          window_seconds: 604800,
          reset_at: 1900000000,
        },
      },
    })

    expect(screen.getByText('exhausted')).toBeInTheDocument()
    expect(screen.getByText('Routing suspended')).toBeInTheDocument()
    expect(screen.getByText('0%')).toBeInTheDocument()
    expect(screen.getByText('60%')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: /5-hour usage remaining/i })).toHaveAttribute('aria-valuenow', '0')
    expect(screen.getByRole('progressbar', { name: /weekly usage remaining/i })).toHaveAttribute('aria-valuenow', '60')
  })
})
