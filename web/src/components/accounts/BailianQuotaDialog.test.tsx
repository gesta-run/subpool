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
    fireEvent.click(screen.getByRole('button', { name: 'Authorization method' }))
    fireEvent.click(screen.getByRole('option', { name: 'RAM AccessKey (automatic renewal)' }))
    fireEvent.change(screen.getByLabelText(/AccessKey ID/), { target: { value: 'illustrative-id' } })
    fireEvent.change(screen.getByLabelText(/AccessKey secret/), { target: { value: 'illustrative-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Connect quota' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/provider-accounts/illustrative-account/bailian-quota')
    expect(fetch.mock.calls[0][1]).toMatchObject({ method: 'PUT', credentials: 'same-origin', body: JSON.stringify({ access_key_id: 'illustrative-id', access_key_secret: 'illustrative-secret' }) })
    expect(onSaved).toHaveBeenCalledOnce()
  })

  it('imports a temporary console token without requesting an AccessKey', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ quota_authorized: true }), { headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)
    const onClose = vi.fn()
    render(<BailianQuotaDialog account={account} onSaved={vi.fn()} onClose={onClose} />)
    expect(screen.queryByLabelText('AccessKey ID')).not.toBeInTheDocument()
    expect(screen.getByText(/sign in again when it expires/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/Console access token/), { target: { value: ' illustrative-token ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Connect quota' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0][1]).toMatchObject({ method: 'PUT', body: JSON.stringify({ access_token: 'illustrative-token' }) })
  })

  it('keeps the dialog open when upstream permission is denied', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: 'Grant Token Plan access' }), { status: 502, headers: { 'Content-Type': 'application/json' } })))
    const onClose = vi.fn()
    render(<BailianQuotaDialog account={account} onSaved={vi.fn()} onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: 'Authorization method' }))
    fireEvent.click(screen.getByRole('option', { name: 'RAM AccessKey (automatic renewal)' }))
    fireEvent.change(screen.getByLabelText(/AccessKey ID/), { target: { value: 'illustrative-id' } })
    fireEvent.change(screen.getByLabelText(/AccessKey secret/), { target: { value: 'illustrative-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Connect quota' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Grant Token Plan access')
    expect(onClose).not.toHaveBeenCalled()
  })

  it('uses the shared form layout and validates before sending credentials', () => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    render(<BailianQuotaDialog account={account} onSaved={vi.fn()} onClose={vi.fn()} />)
    const submit = screen.getByRole('button', { name: 'Connect quota' })
    expect(submit.closest('form')).toHaveClass('form-stack')
    expect(screen.getByText(/official Bailian CLI browser flow/).closest('.field')).not.toBeNull()
    expect(screen.queryByText('Enter a console access token.')).not.toBeInTheDocument()
    expect(submit).toBeEnabled()
    fireEvent.click(submit)
    expect(screen.getByLabelText(/Console access token/)).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText(/Console access token/)).toHaveFocus()
    expect(fetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Authorization method' }))
    fireEvent.click(screen.getByRole('option', { name: 'RAM AccessKey (automatic renewal)' }))
    expect(screen.queryByText('Enter a console access token.')).not.toBeInTheDocument()
    fireEvent.blur(screen.getByLabelText(/AccessKey ID/))
    expect(screen.getByLabelText(/AccessKey ID/)).toHaveAttribute('aria-describedby', 'bailian-key-id-error')
  })

  it('shows the correct disconnect state without requiring replacement credentials', async () => {
    let finish: (response: Response) => void = () => {}
    const fetch = vi.fn(() => new Promise<Response>((resolve) => { finish = resolve }))
    vi.stubGlobal('fetch', fetch)
    const onClose = vi.fn()
    const onSaved = vi.fn().mockResolvedValue(undefined)
    render(<BailianQuotaDialog account={{ ...account, quota_authorized: true }} onSaved={onSaved} onClose={onClose} />)
    expect(screen.getByRole('dialog', { name: 'Manage quota authorization' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Disconnect quota' }))
    expect(screen.getByRole('button', { name: 'Disconnecting…' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Authorization method' })).toBeDisabled()
    expect(screen.getByLabelText(/Console access token/)).toBeDisabled()
    expect(fetch.mock.calls[0]).toEqual(['/api/v1/provider-accounts/illustrative-account/bailian-quota', expect.objectContaining({ method: 'DELETE', body: undefined })])
    finish(new Response(null, { status: 204 }))
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
    expect(onSaved).toHaveBeenCalledOnce()
  })
})
