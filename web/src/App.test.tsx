import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { AccountsPage } from './pages/AccountsPage'
import { APIKeysPage } from './pages/APIKeysPage'
import { PoolsPage } from './pages/PoolsPage'
import { UsagePage } from './pages/UsagePage'
import { SettingsPage } from './pages/SettingsPage'

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

    expect(await screen.findByRole('heading', { name: 'Codex accounts' })).toBeInTheDocument()
    expect(await screen.findByText('No accounts connected')).toBeInTheDocument()
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

    expect(await screen.findByRole('heading', { name: 'Codex accounts' })).toBeInTheDocument()
    expect(screen.queryByLabelText(/username/i)).not.toBeInTheDocument()
  })

  it('shows a newly generated API key exactly in the one-time dialog', async () => {
    const secret = 'sk-subpool-example-one-time-secret'
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/api-keys' && init?.method === 'POST') return json({ key: secret }, 201)
      if (path === '/api/v1/api-keys') return json({ api_keys: [] })
      if (path === '/api/v1/pools') return json({ pools: [{ id: 'pool-1', name: 'Engineering', provider: 'codex', strategy: 'least_assigned' }] })
      if (path === '/api/v1/provider-accounts') return json({ data: [] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<APIKeysPage />)
    const user = userEvent.setup()
    await screen.findByText('No API keys issued')
    await user.click(screen.getAllByRole('button', { name: /create api key/i })[0])
    await user.type(screen.getByLabelText(/employee name/i), 'Alex Chen')
    await user.click(screen.getByRole('button', { name: /^create key$/i }))

    expect(await screen.findByText('Copy this key now')).toBeInTheDocument()
    expect(screen.getByTestId('created-key')).toHaveTextContent(secret)
    expect(screen.getByText(/will not show the complete key again/i)).toBeInTheDocument()
  })

  it('disables an account through the update endpoint without exposing credentials', async () => {
    let status = 'active'
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/provider-accounts/account-1' && init?.method === 'PUT') {
        status = 'disabled'
        return json({ id: 'account-1', status })
      }
      if (path === '/api/v1/provider-accounts') return json({ data: [{ id: 'account-1', display_name: 'Primary Codex', provider: 'codex', status, max_api_keys: 3, assigned_api_keys: 1 }] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<AccountsPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Disable' }))

    await waitFor(() => expect(screen.getByRole('button', { name: 'Enable' })).toBeInTheDocument())
    const update = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path).endsWith('/account-1') && init?.method === 'PUT')
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({ display_name: 'Primary Codex', status: 'disabled' })
  })

  it('keeps focus in the account name field while typing', async () => {
    vi.mocked(fetch).mockResolvedValue(json({ data: [] }))

    render(<AccountsPage />)
    const user = userEvent.setup()
    await screen.findByText('No accounts connected')
    await user.click(screen.getAllByRole('button', { name: /connect account/i })[0])
    const input = screen.getByLabelText(/display name/i)

    await user.type(input, 'Codex Primary')

    expect(input).toHaveValue('Codex Primary')
    expect(input).toHaveFocus()
  })

  it('removes an unassigned account after confirmation', async () => {
    let accounts = [{ id: 'account-1', display_name: 'Primary Codex', provider: 'codex', status: 'active', max_api_keys: 3, assigned_api_keys: 0 }]
    vi.spyOn(window, 'confirm').mockReturnValue(true)
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
    await user.click(await screen.findByRole('button', { name: 'Remove' }))

    expect(await screen.findByText('No accounts connected')).toBeInTheDocument()
    expect(fetch).toHaveBeenCalledWith('/api/v1/provider-accounts/account-1', expect.objectContaining({ method: 'DELETE' }))
  })

  it('shows pool members returned by the control API', async () => {
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = String(input)
      if (path === '/api/v1/pools') return json({ data: [{ id: 'pool-1', name: 'Engineering', provider: 'codex', strategy: 'least_assigned', model_allowlist: ['gpt-5-codex'], accounts: [{ pool_id: 'pool-1', provider_account_id: 'account-1', weight: 1, priority: 0, enabled: true }] }] })
      if (path === '/api/v1/provider-accounts') return json({ data: [{ id: 'account-1', display_name: 'Primary Codex', provider: 'codex', status: 'active', max_api_keys: 3, assigned_api_keys: 1 }] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<PoolsPage />)
    const user = userEvent.setup()
    expect((await screen.findAllByText('1')).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: 'Manage Engineering' }))
    expect(await screen.findByText('Primary Codex')).toBeInTheDocument()
    expect(screen.getByText('Current members')).toBeInTheDocument()
    await user.clear(screen.getByLabelText(/allowed models/i))
    await user.click(screen.getByRole('button', { name: 'Save settings' }))
    expect((await screen.findAllByText('Enter at least one allowed model ID.')).length).toBeGreaterThan(0)
  })

  it('requires a model allowlist when creating a pool', async () => {
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = String(input)
      if (path === '/api/v1/pools' || path === '/api/v1/provider-accounts') return json({ data: [] })
      throw new Error(`Unexpected request: ${path}`)
    })

    render(<PoolsPage />)
    const user = userEvent.setup()
    await screen.findByText('No pools configured')
    await user.click(screen.getAllByRole('button', { name: 'Create pool' })[0])
    await user.type(screen.getByLabelText(/pool name/i), 'Engineering')
    await user.clear(screen.getByLabelText(/allowed models/i))
    await user.click(screen.getAllByRole('button', { name: /^create pool$/i }).at(-1)!)

    expect(await screen.findByText('Enter at least one allowed model ID.')).toBeInTheDocument()
    expect(vi.mocked(fetch).mock.calls.every(([, init]) => init?.method !== 'POST')).toBe(true)
  })

  it('aggregates input and output usage without rendering request content', async () => {
    vi.mocked(fetch).mockResolvedValue(json({ usage: [
      { api_key_id: 'key-1', employee_name: 'Alex Chen', key_hint: '1a2b', usage_date: '2026-09-01', input_tokens: 1200, output_tokens: 300 },
      { api_key_id: 'key-1', employee_name: 'Alex Chen', key_hint: '1a2b', usage_date: '2026-08-31', input_tokens: 800, output_tokens: 200 },
    ] }))

    render(<UsagePage />)

    expect((await screen.findAllByText('2,000')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('500').length).toBeGreaterThan(0)
    expect(screen.getAllByText('2,500').length).toBeGreaterThan(0)
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([path]) => String(path).startsWith('/api/v1/usage?from='))).toBe(true))
  })

  it('updates the global API key capacity setting', async () => {
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = String(input)
      if (path === '/api/v1/settings' && init?.method === 'PUT') return json({ max_api_keys_per_account: 5 })
      if (path === '/api/v1/settings') return json({ max_api_keys_per_account: 3 })
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
