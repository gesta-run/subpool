import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { AccountsPage } from './pages/AccountsPage'
import { APIKeysPage } from './pages/APIKeysPage'
import { PoolsPage } from './pages/PoolsPage'
import { UsagePage } from './pages/UsagePage'
import { SettingsPage } from './pages/SettingsPage'
import { request } from './api'

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

describe('Subpool console', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  it('authenticates the administrator and opens the accounts page', async () => {
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/auth/session') {
        const hasLogin = vi.mocked(fetch).mock.calls.some(([url]) => String(url) === '/api/v1/auth/login')
        return hasLogin ? json({ data: [] }) : json({ message: 'unauthorized' }, 401)
      }
      if (path === '/api/v1/provider-accounts') return json({ data: [] })
      if (path === '/api/v1/auth/login' && init?.method === 'POST') return new Response(null, { status: 204 })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<App />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText(/username/i), 'admin')
    await user.type(screen.getByLabelText(/password/i), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: /enter console/i }))

    expect(await screen.findByRole('heading', { name: 'Provider accounts' })).toBeInTheDocument()
    expect(await screen.findByText('No accounts connected')).toBeInTheDocument()
    expect(screen.queryByText('LOCAL')).not.toBeInTheDocument()
    expect(screen.queryByText('Administrator')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Supported by\s*Gesta/i })).toHaveAttribute('href', 'https://gesta.run')
    expect(fetch).toHaveBeenCalledWith('/api/v1/auth/login', expect.objectContaining({ method: 'POST' }))
  })

  it('restores an existing administrator session after refresh', async () => {
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = String(input)
      if (path === '/api/v1/auth/session') return json({ authenticated: true })
      if (path === '/api/v1/provider-accounts') return json({ data: [] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Provider accounts' })).toBeInTheDocument()
    expect(screen.queryByLabelText(/username/i)).not.toBeInTheDocument()
  })

  it('returns to login when an authenticated action reports an expired session', async () => {
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = String(input)
      if (path === '/api/v1/auth/session') return json({ authenticated: true })
      if (path === '/api/v1/provider-accounts') return json({ data: [] })
      if (path === '/api/v1/pools') return json({ error: { message: 'authentication required' } }, 401)
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<App />)
    await screen.findByRole('heading', { name: 'Provider accounts' })
    await expect(request('/api/v1/pools', { method: 'POST' })).rejects.toThrow('authentication required')

    expect(await screen.findByRole('heading', { name: 'Sign in to Subpool' })).toBeInTheDocument()
  })

  it('shows a newly generated API key exactly in the one-time dialog', async () => {
    const secret = 'sk-example-one-time-secret'
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/api-keys' && init?.method === 'POST') return json({ key: secret }, 201)
      if (path === '/api/v1/api-keys') return json({ api_keys: [] })
      if (path === '/api/v1/pools') return json({ pools: [{ id: 'pool-1', name: 'Engineering', provider: 'codex' }] })
      if (path === '/api/v1/provider-accounts') return json({ data: [] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<APIKeysPage />)
    const user = userEvent.setup()
    await screen.findByText('No API keys issued')
    expect(screen.getAllByRole('button', { name: /create api key/i })).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: /create api key/i }))
    await user.type(screen.getByLabelText(/employee name/i), 'Alex Chen')
    await user.click(screen.getByRole('button', { name: /^create key$/i }))

    expect(await screen.findByText('Copy this key now')).toBeInTheDocument()
    expect(screen.getByTestId('created-key')).toHaveTextContent(secret)
    expect(screen.getByText(/will not show the complete key again/i)).toBeInTheDocument()
  })

  it('disables an account through the update endpoint without exposing credentials', async () => {
    let status = 'active'
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts/account-1' && init?.method === 'PUT') {
        status = 'disabled'
        return json({ id: 'account-1', status })
      }
      if (path === '/api/v1/provider-accounts') return json({ data: [{ id: 'account-1', display_name: 'Primary Codex', provider: 'codex', status, assigned_api_keys: 1 }] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<AccountsPage />)
    const user = userEvent.setup()
    expect(await screen.findByRole('columnheader', { name: 'Availability' })).toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: 'Assigned API keys' })).not.toBeInTheDocument()
    expect(screen.getByText('Bound keys')).toBeInTheDocument()
    expect(screen.queryByText(/of 3 keys/i)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh credentials for Primary Codex' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Check health for Primary Codex' })).not.toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'Disable Primary Codex' }))
    await user.click(await screen.findByRole('button', { name: 'Disable account' }))

    await waitFor(() => expect(screen.getByRole('button', { name: 'Enable Primary Codex' })).toBeInTheDocument())
    const update = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path).endsWith('/account-1') && init?.method === 'PUT')
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({ display_name: 'Primary Codex', status: 'disabled' })
  })

  it('keeps focus in the account name field while typing', async () => {
    vi.mocked(fetch).mockResolvedValue(json({ data: [] }))

    render(<AccountsPage />)
    const user = userEvent.setup()
    await screen.findByText('No accounts connected')
    expect(screen.getAllByRole('button', { name: /connect account/i })).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: /connect account/i }))
    const input = screen.getByLabelText(/display name/i)

    await user.type(input, 'Codex Primary')

    expect(input).toHaveValue('Codex Primary')
    expect(input).toHaveFocus()
  })

  it('starts Codex device authorization without navigating to localhost', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts') return json({ data: [] })
      if (path === '/api/v1/provider-accounts/codex/device-login' && init?.method === 'POST') return json({
        login_id: 'device-login', user_code: 'ABCD-EFGH', verification_url: 'https://auth.openai.com/device', expires_at: '2026-09-02T12:00:00Z',
      }, 201)
      if (path === '/api/v1/provider-accounts/codex/device-login/device-login') return json({ status: 'pending' })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<AccountsPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /connect account/i }))
    await user.type(screen.getByLabelText(/display name/i), 'Primary Codex')
    await user.click(screen.getByRole('button', { name: /generate code/i }))

    expect(await screen.findByTestId('device-code')).toHaveTextContent('ABCD-EFGH')
    expect(screen.getByText(/no localhost callback is required/i)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /copy code and continue/i }))
    expect(open).toHaveBeenCalledWith('https://auth.openai.com/device', '_blank', 'noopener,noreferrer')
    const start = vi.mocked(fetch).mock.calls.find(([path, options]) => String(path).endsWith('/codex/device-login') && options?.method === 'POST')
    expect(JSON.parse(String(start?.[1]?.body))).toEqual({ display_name: 'Primary Codex' })
  })

  it('recovers Codex authorization after a transient polling failure', async () => {
    let polls = 0
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts') return json({ data: [] })
      if (path === '/api/v1/provider-accounts/codex/device-login' && init?.method === 'POST') return json({
        login_id: 'device-login', user_code: 'ABCD-EFGH', verification_url: 'https://auth.openai.com/device', expires_at: '2099-09-02T12:00:00Z',
      }, 201)
      if (path === '/api/v1/provider-accounts/codex/device-login/device-login') {
        polls++
        return polls === 1 ? json({ message: 'temporary failure' }, 503) : json({ status: 'completed' })
      }
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<AccountsPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /connect account/i }))
    await user.type(screen.getByLabelText(/display name/i), 'Primary Codex')
    await user.click(screen.getByRole('button', { name: /generate code/i }))

    expect(await screen.findByTestId('device-code')).toHaveTextContent('ABCD-EFGH')
    expect(await screen.findByText(/connection interrupted.*retrying/i, {}, { timeout: 2_000 })).toBeInTheDocument()
    expect(screen.getByTestId('device-code')).toHaveTextContent('ABCD-EFGH')
    await waitFor(() => expect(screen.queryByTestId('device-code')).not.toBeInTheDocument(), { timeout: 4_000 })
    expect(polls).toBe(2)
  })

  it('removes an unassigned account after confirmation', async () => {
    let accounts = [{ id: 'account-1', display_name: 'Primary Codex', provider: 'codex', status: 'active', assigned_api_keys: 0 }]
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts/account-1' && init?.method === 'DELETE') {
        accounts = []
        return new Response(null, { status: 204 })
      }
      if (path === '/api/v1/provider-accounts') return json({ data: accounts })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<AccountsPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Remove Primary Codex' }))
    await user.click(await screen.findByRole('button', { name: 'Remove account' }))

    expect(await screen.findByText('No accounts connected')).toBeInTheDocument()
    expect(fetch).toHaveBeenCalledWith('/api/v1/provider-accounts/account-1', expect.objectContaining({ method: 'DELETE' }))
  })

  it('shows remaining weekly subscription usage', async () => {
    vi.mocked(fetch).mockResolvedValue(json({ data: [{
      id: 'account-1', display_name: 'Primary Codex', email: 'employee@example.com', provider: 'codex', credential_type: 'subscription_oauth', status: 'active', assigned_api_keys: 0,
      quota_snapshot: { plan_type: 'plus', weekly: { used_percent: 40, remaining_percent: 60, window_seconds: 604800, reset_at: 1800500000 } },
    }] }))

    render(<AccountsPage />)

    expect(await screen.findByText('60%')).toBeInTheDocument()
    expect(screen.getByText(/employee@example\.com/)).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: /weekly usage remaining/i })).toHaveAttribute('aria-valuenow', '60')
    expect(screen.getByText(/^Resets /i)).toBeInTheDocument()
  })

  it('shows and consumes an earned Codex reset credit', async () => {
    let availableCount = 2
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts') return json({ data: [{
        id: 'account-1', display_name: 'Primary Codex', provider: 'codex', credential_type: 'subscription_oauth', status: 'exhausted', assigned_api_keys: 1,
      }] })
      if ((path === '/api/v1/provider-accounts/account-1/reset-credits' || path === '/api/v1/provider-accounts/account-1/reset-credits?refresh=true') && !init?.method) return json({
        reset_credits: { available_count: availableCount, credits: [{ id: 'credit-1', reset_type: 'codexRateLimits', status: 'available', granted_at: 1800000000, expires_at: 1800500000 }] },
      })
      if (path === '/api/v1/provider-accounts/account-1/reset-credits/consume' && init?.method === 'POST') {
        availableCount = 1
        return json({ outcome: 'reset', reset_credits: { available_count: availableCount, credits: [] } })
      }
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<AccountsPage />)
    await screen.findByText(/Expires/)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Reset quota' }))
    await user.click((await screen.findAllByRole('button', { name: 'Use full reset' })).at(-1)!)

    expect(await screen.findByText(/Full reset applied/)).toBeInTheDocument()
    const consume = vi.mocked(fetch).mock.calls.find(([path, options]) => String(path).endsWith('/reset-credits/consume') && options?.method === 'POST')
    const body = JSON.parse(String(consume?.[1]?.body))
    expect(body.credit_id).toBe('credit-1')
    expect(body.idempotency_key).toMatch(/^[0-9a-f-]{36}$/)
  })

  it('opens the supported model list for an account', async () => {
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts') return json({ data: [{ id: 'account-1', display_name: 'Primary Codex', provider: 'codex', credential_type: 'subscription_oauth', status: 'active', assigned_api_keys: 0 }] })
      if (path === '/api/v1/provider-accounts/account-1/reset-credits') return json({ reset_credits: null })
      if (path === '/api/v1/provider-accounts/account-1/models') return json({ data: [
        { id: 'model-alpha', display_name: 'Model Alpha', description: 'General-purpose model', is_default: true, reasoning_efforts: ['medium', 'high'], input_modalities: ['text', 'image'] },
        { id: 'model-beta' },
      ] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<AccountsPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'View supported models for Primary Codex' }))

    expect(await screen.findByRole('dialog', { name: 'Supported models' })).toBeInTheDocument()
    expect(screen.getByText('2 models available')).toBeInTheDocument()
    expect(screen.getByText('Model Alpha')).toBeInTheDocument()
    expect(screen.getByText('model-alpha')).toBeInTheDocument()
    expect(screen.getByText('Reasoning: medium, high')).toBeInTheDocument()
    expect(fetch).toHaveBeenCalledWith('/api/v1/provider-accounts/account-1/models', expect.objectContaining({ credentials: 'same-origin' }))
  })

  it('shows pool members and attaches a paid API fallback', async () => {
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/pools/pool-1/accounts' && init?.method === 'POST') return json({ provider_account_id: 'account-api', priority: 100 })
      if (path === '/api/v1/pools') return json({ data: [{ id: 'pool-1', name: 'Engineering', provider: 'codex', accounts: [{ pool_id: 'pool-1', provider_account_id: 'account-1', weight: 1, priority: 0, enabled: true }] }] })
      if (path === '/api/v1/provider-accounts') return json({ data: [
        { id: 'account-1', display_name: 'Primary Codex', provider: 'codex', credential_type: 'subscription_oauth', status: 'active', assigned_api_keys: 1 },
        { id: 'account-api', display_name: 'Paid API', provider: 'openai_compatible', credential_type: 'api_key', status: 'active', assigned_api_keys: 0 },
      ] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<PoolsPage />)
    const user = userEvent.setup()
    expect((await screen.findAllByText('1')).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: 'Manage Engineering' }))
    expect(await screen.findByText('Primary Codex')).toBeInTheDocument()
    expect(screen.getByText('Current members')).toBeInTheDocument()
    expect(screen.queryByLabelText(/allowed models/i)).not.toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: /Paid API/i }))
    await user.click(screen.getByRole('button', { name: /attach selected/i }))
    await waitFor(() => {
      const attach = vi.mocked(fetch).mock.calls.find(([path, options]) => String(path) === '/api/v1/pools/pool-1/accounts' && options?.method === 'POST')
      expect(JSON.parse(String(attach?.[1]?.body))).toEqual({ provider_account_id: 'account-api', weight: 1, enabled: true })
    })
  })

  it('creates a mixed pool with subscription and paid API accounts', async () => {
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts') return json({ data: [
        { id: 'account-1', display_name: 'Primary Codex', provider: 'codex', credential_type: 'subscription_oauth', status: 'active' },
        { id: 'account-2', display_name: 'Paid API', provider: 'openai_compatible', credential_type: 'api_key', status: 'active' },
      ] })
      if (path === '/api/v1/pools' && init?.method === 'POST') return json({ id: 'pool-1', name: 'Engineering' }, 201)
      if (path === '/api/v1/pools') return json({ data: [] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<PoolsPage />)
    const user = userEvent.setup()
    await screen.findByText('No pools configured')
    expect(screen.getAllByRole('button', { name: 'Create pool' })).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Create pool' }))
    await user.type(screen.getByLabelText(/pool name/i), 'Engineering')
    await user.click(screen.getByRole('checkbox', { name: /Primary Codex/i }))
    await user.click(screen.getByRole('checkbox', { name: /Paid API/i }))
    await user.click(screen.getByRole('button', { name: /create pool with 2/i }))

    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([path, options]) => {
      if (String(path) !== '/api/v1/pools' || options?.method !== 'POST') return false
      const body = JSON.parse(String(options.body))
      return body.name === 'Engineering' && body.model_allowlist === undefined && body.provider_account_ids.join(',') === 'account-1,account-2'
    })).toBe(true))
  })

  it('aggregates input and output usage without rendering request content', async () => {
    vi.mocked(fetch).mockResolvedValue(json({ usage: [
      { api_key_id: 'key-1', employee_name: 'Alex Chen', key_hint: '1a2b', usage_date: '2026-09-01', input_tokens: 1_200_000, output_tokens: 300_000 },
      { api_key_id: 'key-1', employee_name: 'Alex Chen', key_hint: '1a2b', usage_date: '2026-08-31', input_tokens: 800_000, output_tokens: 200_000 },
    ] }))

    render(<UsagePage />)

    expect((await screen.findAllByText('2.00M')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('500,000').length).toBeGreaterThan(0)
    expect(screen.getAllByText('2.50M').length).toBeGreaterThan(0)
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([path]) => String(path).startsWith('/api/v1/usage?from='))).toBe(true))
  })

  it('updates the employee key capacity setting', async () => {
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/settings' && init?.method === 'PUT') return json({ max_api_keys_per_account: 5 })
      if (path === '/api/v1/settings') return json({ max_api_keys_per_account: 2 })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<SettingsPage />)
    const user = userEvent.setup()
    const input = await screen.findByLabelText('Maximum API keys per account')
    await user.clear(input)
    await user.type(input, '5')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))

    expect(await screen.findByRole('status')).toHaveTextContent('Global routing setting saved.')
    const update = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path) === '/api/v1/settings' && init?.method === 'PUT')
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({ max_api_keys_per_account: 5 })
  })
})
