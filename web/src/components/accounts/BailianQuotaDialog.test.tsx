import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { BailianQuotaDialog } from './BailianQuotaDialog'
import type { ProviderAccount } from '../../types'

const account: ProviderAccount = { id: 'illustrative-account', display_name: 'Example Token Plan', provider: 'openai_compatible', status: 'active', quota_provider: 'bailian_token_plan' }
afterEach(() => vi.unstubAllGlobals())

describe('BailianQuotaDialog', () => {
  it('sends quota credentials only to the authenticated configuration endpoint', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ quota_authorized: true }), { headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)
    const onSaved = vi.fn().mockResolvedValue(undefined)
    const onClose = vi.fn()
    render(<BailianQuotaDialog account={account} onSaved={onSaved} onClose={onClose} />)
    fireEvent.change(screen.getByLabelText('AccessKey ID'), { target: { value: 'illustrative-id' } })
    fireEvent.change(screen.getByLabelText('AccessKey secret'), { target: { value: 'illustrative-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Connect quota' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/provider-accounts/illustrative-account/bailian-quota')
    expect(fetch.mock.calls[0][1]).toMatchObject({ method: 'PUT', credentials: 'same-origin', body: JSON.stringify({ access_key_id: 'illustrative-id', access_key_secret: 'illustrative-secret' }) })
    expect(onSaved).toHaveBeenCalledOnce()
  })

  it('keeps the dialog open when upstream permission is denied', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: 'Grant Token Plan access' }), { status: 502, headers: { 'Content-Type': 'application/json' } })))
    const onClose = vi.fn()
    render(<BailianQuotaDialog account={account} onSaved={vi.fn()} onClose={onClose} />)
    fireEvent.change(screen.getByLabelText('AccessKey ID'), { target: { value: 'illustrative-id' } })
    fireEvent.change(screen.getByLabelText('AccessKey secret'), { target: { value: 'illustrative-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Connect quota' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Grant Token Plan access')
    expect(onClose).not.toHaveBeenCalled()
  })
})
