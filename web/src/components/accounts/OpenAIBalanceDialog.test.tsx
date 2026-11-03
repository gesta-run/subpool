import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { OpenAIBalanceDialog } from './OpenAIBalanceDialog'
import type { ProviderAccount } from '../../types'

const account: ProviderAccount = { id: 'example', display_name: 'Example API', provider: 'openai_compatible', status: 'active', quota_provider: 'openai_api_credits', quota_authorized: true }

describe('OpenAI balance authorization', () => {
  it('disconnects without exposing or requiring the saved session key', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    const onSaved = vi.fn()
    render(<OpenAIBalanceDialog account={account} onClose={vi.fn()} onSaved={onSaved} />)
    expect(screen.queryByLabelText('Browser session key')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Disconnect billing' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(fetch).toHaveBeenCalledWith('/api/v1/provider-accounts/example/openai-balance', expect.objectContaining({ method: 'DELETE' }))
  })

  it('uses a masked input and keeps the dialog open when verification rejects replacement credentials', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"error":{"message":"Organization mismatch"}}', { status: 502, headers: { 'Content-Type': 'application/json' } })))
    const onSaved = vi.fn()
    render(<OpenAIBalanceDialog account={{ ...account, quota_authorized: false }} onClose={vi.fn()} onSaved={onSaved} />)
    fireEvent.change(screen.getByLabelText('Balance source'), { target: { value: 'browser' } })
    expect(screen.getByLabelText('Browser session key')).toHaveAttribute('type', 'password')
    fireEvent.change(screen.getByLabelText('Browser session key'), { target: { value: 'illustrative-session' } })
    fireEvent.change(screen.getByLabelText('Organization ID'), { target: { value: 'org-example' } })
    fireEvent.submit(screen.getByRole('button', { name: 'Save authorization' }).closest('form')!)
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Organization mismatch'))
    expect(onSaved).not.toHaveBeenCalled()
  })
})

 it('submits Costs credentials and a grant with explicit Beijing timestamps', async () => {
  const fetch=vi.fn().mockResolvedValue(new Response('{}',{status:200}));vi.stubGlobal('fetch',fetch)
  const saved=vi.fn();render(<OpenAIBalanceDialog account={{...account,quota_authorized:false}} onClose={vi.fn()} onSaved={saved} />)
  expect(screen.getByLabelText('Admin API key')).toHaveAttribute('type','password')
  fireEvent.change(screen.getByLabelText('Organization ID'),{target:{value:'org-example'}})
  fireEvent.change(screen.getByLabelText('Admin API key'),{target:{value:'sk-admin-illustrative'}})
  fireEvent.change(screen.getByLabelText('Grant amount (USD)'),{target:{value:'50'}})
  fireEvent.change(screen.getByLabelText('Grant start (Beijing time)'),{target:{value:'2027-01-01T00:00:00'}})
  fireEvent.change(screen.getByLabelText('Grant expiry (Beijing time)'),{target:{value:'2027-02-01T00:00:00'}})
  fireEvent.submit(screen.getByRole('button',{name:'Save authorization'}).closest('form')!)
  await waitFor(()=>expect(saved).toHaveBeenCalled())
  const body=JSON.parse(fetch.mock.calls[0][1].body)
  expect(body).toEqual({organization_id:'org-example',admin_key:'sk-admin-illustrative',grant_amount_usd:50,grant_start_at:Date.parse('2027-01-01T00:00:00+08:00')/1000,grant_expires_at:Date.parse('2027-02-01T00:00:00+08:00')/1000})
 })
