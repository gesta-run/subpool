import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SettingsPage } from './SettingsPage'

const MiB = 1024 * 1024
const settings = { max_api_keys_per_account: 3, max_request_body_bytes: 256 * MiB, max_inflight_request_body_bytes: 1024 * MiB }
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

describe('Request limit settings', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () => json(settings)))
  })

  it('displays MiB values and persists byte values', async () => {
    vi.mocked(fetch).mockImplementation(async (_input, init) => json(init?.method === 'PUT' ? JSON.parse(String(init.body)) : settings))
    render(<SettingsPage />)
    const user = userEvent.setup()
    const body = await screen.findByLabelText('Maximum request body (MiB)')
    const buffer = screen.getByLabelText('Concurrent request buffer budget (MiB)')
    expect(body).toHaveValue(256)
    expect(buffer).toHaveValue(1024)
    await user.clear(body)
    await user.type(body, '512')
    await user.clear(buffer)
    await user.type(buffer, '2048')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Global settings saved.')
    const update = vi.mocked(fetch).mock.calls.find(([, init]) => init?.method === 'PUT')
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({
      max_api_keys_per_account: 3, max_request_body_bytes: 512 * MiB, max_inflight_request_body_bytes: 2048 * MiB,
    })
    expect(screen.getByText(/does not increase Copilot/)).toBeInTheDocument()
  })

  it('preserves byte-precise limits when only employee capacity changes', async () => {
    const precise = { ...settings, max_request_body_bytes: MiB + 1, max_inflight_request_body_bytes: 4 * (MiB + 1) }
    vi.mocked(fetch).mockImplementation(async (_input, init) => json(init?.method === 'PUT' ? { ...precise, ...JSON.parse(String(init.body)) } : precise))
    render(<SettingsPage />)
    const user = userEvent.setup()
    const keys = await screen.findByLabelText('Maximum API keys per account')
    await user.clear(keys)
    await user.type(keys, '5')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Global settings saved.')
    const update = vi.mocked(fetch).mock.calls.find(([, init]) => init?.method === 'PUT')
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({ max_api_keys_per_account: 5 })
  })

  it('accepts fractional MiB values without rounding byte limits', async () => {
    vi.mocked(fetch).mockImplementation(async (_input, init) => json(init?.method === 'PUT' ? { ...settings, ...JSON.parse(String(init.body)) } : settings))
    render(<SettingsPage />)
    const user = userEvent.setup()
    const body = await screen.findByLabelText('Maximum request body (MiB)')
    await user.clear(body)
    await user.type(body, '1.5')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Global settings saved.')
    const update = vi.mocked(fetch).mock.calls.find(([, init]) => init?.method === 'PUT')
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({ max_api_keys_per_account: 3, max_request_body_bytes: 1.5 * MiB })
  })

  it('validates on blur and focuses an unsafe buffer budget on submit', async () => {
    render(<SettingsPage />)
    const user = userEvent.setup()
    const buffer = await screen.findByLabelText('Concurrent request buffer budget (MiB)')
    await user.clear(buffer)
    await user.type(buffer, '512')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await user.tab()
    expect(await screen.findByRole('alert')).toHaveTextContent('at least four times')
    expect(buffer).toHaveAttribute('aria-invalid', 'true')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(buffer).toHaveFocus()
    expect(vi.mocked(fetch).mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0)
  })

  it('rejects fractional bytes and preserves the values for correction', async () => {
    render(<SettingsPage />)
    const user = userEvent.setup()
    const body = await screen.findByLabelText('Maximum request body (MiB)')
    await user.clear(body)
    await user.type(body, '0.0000001')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('whole number of bytes')
    expect(body).toHaveFocus()
    expect(body).toHaveValue(0.0000001)
    expect(vi.mocked(fetch).mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0)
  })

  it('keeps editable values after a save failure and allows retry', async () => {
    let attempts = 0
    vi.mocked(fetch).mockImplementation(async (_input, init) => {
      if (init?.method !== 'PUT') return json(settings)
      attempts++
      return attempts === 1 ? json({ message: 'Illustrative settings service unavailable' }, 503) : json({ ...settings, ...JSON.parse(String(init.body)) })
    })
    render(<SettingsPage />)
    const user = userEvent.setup()
    const body = await screen.findByLabelText('Maximum request body (MiB)')
    await user.clear(body)
    await user.type(body, '128')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Illustrative settings service unavailable')
    expect(body).toHaveValue(128)
    expect(body).not.toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Global settings saved.')
  })

  it('provides a retry action when settings fail to load', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ message: 'Illustrative load failure' }, 503)).mockResolvedValue(json(settings))
    render(<SettingsPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Try again' }))
    expect(await screen.findByLabelText('Maximum request body (MiB)')).toHaveValue(256)
  })

  it('disables the fields while a save is in progress', async () => {
    let finish: ((response: Response) => void) | undefined
    vi.mocked(fetch).mockImplementation(async (_input, init) => init?.method === 'PUT' ? new Promise<Response>((resolve) => { finish = resolve }) : json(settings))
    render(<SettingsPage />)
    const user = userEvent.setup()
    await screen.findByLabelText('Maximum request body (MiB)')
    await user.click(screen.getByRole('button', { name: 'Save setting' }))
    expect(screen.getByLabelText('Maximum request body (MiB)')).toBeDisabled()
    expect(screen.getByRole('button', { name: /Saving/ })).toBeDisabled()
    finish?.(json(settings))
    await waitFor(() => expect(screen.getByLabelText('Maximum request body (MiB)')).not.toBeDisabled())
  })
})
