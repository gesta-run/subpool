import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAntigravityLogin } from './useLogin'

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => vi.unstubAllGlobals())

describe('Google Antigravity authorization', () => {
  it('starts OAuth and exchanges the callback without keeping it in state', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(response({ login_id: 'example-state', authorization_url: 'https://accounts.google.com/example', expires_at: '2030-01-01T00:00:00Z' }, 201))
      .mockResolvedValueOnce(response({ id: 'example-account' }, 201))
    vi.stubGlobal('fetch', fetch)
    const connected = vi.fn(async () => {})
    const { result } = renderHook(() => useAntigravityLogin(connected))
    await act(async () => { await result.current.start() })
    expect(result.current.login?.login_id).toBe('example-state')
    act(() => result.current.setCallbackURL('http://localhost:8085/callback?code=example-code&state=example-state'))
    await act(async () => { await result.current.complete('Example Google account') })
    expect(fetch.mock.calls[1][0]).toBe('/api/v1/provider-accounts/antigravity/oauth/complete')
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toMatchObject({ login_id: 'example-state', display_name: 'Example Google account' })
    expect(result.current.callbackURL).toBe('')
    expect(connected).toHaveBeenCalledOnce()
  })

  it('shows configuration errors and allows a clean retry', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response({ error: 'Configure Google OAuth first.' }, 503)))
    const { result } = renderHook(() => useAntigravityLogin(vi.fn()))
    await act(async () => { await result.current.start() })
    expect(result.current.error).toBe('Configure Google OAuth first.')
    expect(result.current.busy).toBe(false)
    act(() => result.current.cancel())
    expect(result.current.login).toBeNull()
    expect(result.current.error).toBe('')
  })

  it('does not exchange an expired authorization', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ login_id: 'expired', authorization_url: 'https://accounts.google.com/example', expires_at: '2020-01-01T00:00:00Z' }, 201))
    vi.stubGlobal('fetch', fetch)
    const { result } = renderHook(() => useAntigravityLogin(vi.fn()))
    await act(async () => { await result.current.start() })
    act(() => result.current.setCallbackURL('http://localhost:8085/callback?code=example'))
    await act(async () => { await result.current.complete('Example Google account') })
    expect(result.current.error).toContain('expired')
    expect(fetch).toHaveBeenCalledOnce()
  })
})
