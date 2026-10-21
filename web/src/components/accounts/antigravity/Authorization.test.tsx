import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAntigravityLogin } from '../../../hooks/antigravity/useLogin'
import { ConnectAccountDialog } from '../ConnectAccountDialog'

const authorization = {
  login_id: 'example-state',
  authorization_url: 'https://accounts.google.com/example',
  expires_at: '2030-01-01T00:00:00Z',
}

function response(body: unknown, status = 201) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function GoogleDialog({ onConnected, onClose = vi.fn() }: { onConnected: () => Promise<void>, onClose?: () => void }) {
  const google = useAntigravityLogin(onConnected)
  return <ConnectAccountDialog google={google} apiKey="" baseURL="" busy={google.busy}
    displayName="Example Google account" deviceLogin={null} error={google.error} fieldErrors={{}}
    provider="antigravity" copyStatus="" onAPIKeyChange={vi.fn()} onBaseURLChange={vi.fn()}
    onClose={onClose} onContinueToProvider={vi.fn()} onDisplayNameChange={vi.fn()}
    onFieldErrorsChange={vi.fn()} onProviderChange={vi.fn()} onSubmit={() => void google.start()} />
}

afterEach(() => vi.unstubAllGlobals())

describe('Google authorization with server-managed OAuth credentials', () => {
  it('starts login, explains the fixed callback, and submits only the account authorization', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response(authorization)).mockResolvedValueOnce(response({ id: 'example-account' }))
    vi.stubGlobal('fetch', fetch)
    const onConnected = vi.fn(async () => {})
    render(<GoogleDialog onConnected={onConnected} />)
    expect(screen.getByText(/deployment administrator must configure the OAuth client first/)).toBeInTheDocument()
    expect(screen.queryByLabelText(/Client ID|Client Secret/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Authorize Google' }))
    const link = await screen.findByRole('link', { name: 'Continue to Google' })
    expect(link).toHaveAttribute('href', authorization.authorization_url)
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getByText(/localhost:8085\/callback on your computer/)).toBeInTheDocument()
    const callback = screen.getByLabelText(/Callback URL/)
    expect(callback).toHaveAttribute('type', 'password')
    expect(callback).toHaveAccessibleDescription(/one-time code/)
    fireEvent.change(callback, { target: { value: 'http://localhost:8085/callback?code=example-code&state=example-state' } })
    fireEvent.click(screen.getByRole('button', { name: 'Connect account' }))
    await waitFor(() => expect(onConnected).toHaveBeenCalledOnce())
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/provider-accounts/antigravity/oauth')
    expect(fetch.mock.calls[0][1].body).toBeUndefined()
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({
      login_id: 'example-state', callback_url: 'http://localhost:8085/callback?code=example-code&state=example-state',
      display_name: 'Example Google account',
    })
    expect(callback).toHaveValue('')
  })

  it('announces missing server configuration, supports retry, and remains keyboard dismissible', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ error: 'Configure both SUBPOOL_ANTIGRAVITY_CLIENT_ID and SUBPOOL_ANTIGRAVITY_CLIENT_SECRET.' }, 503))
      .mockResolvedValueOnce(response(authorization))
    vi.stubGlobal('fetch', fetch)
    const onClose = vi.fn()
    render(<GoogleDialog onConnected={vi.fn(async () => {})} onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: 'Authorize Google' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Configure both SUBPOOL_ANTIGRAVITY_CLIENT_ID and SUBPOOL_ANTIGRAVITY_CLIENT_SECRET.')
    const retry = screen.getByRole('button', { name: 'Authorize Google' })
    expect(retry).toBeEnabled()
    fireEvent.click(retry)
    await screen.findByRole('link', { name: 'Continue to Google' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('shows a disabled loading action while authorization is starting', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
    render(<GoogleDialog onConnected={vi.fn(async () => {})} />)
    fireEvent.click(screen.getByRole('button', { name: 'Authorize Google' }))
    expect(screen.getByRole('button', { name: 'Starting…' })).toBeDisabled()
  })
})
