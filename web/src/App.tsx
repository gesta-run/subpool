import { useCallback, useEffect, useState } from 'react'
import { APIError, errorMessage, request } from './api'
import { Shell, type PageID } from './components/Shell'
import { AccountsPage } from './pages/AccountsPage'
import { APIKeysPage } from './pages/APIKeysPage'
import { LoginPage } from './pages/LoginPage'
import { PoolsPage } from './pages/PoolsPage'
import { UsagePage } from './pages/UsagePage'
import { SettingsPage } from './pages/SettingsPage'

type AuthState = 'checking' | 'authenticated' | 'anonymous'

function pageFromHash(): PageID {
  const value = window.location.hash.replace(/^#\//, '')
  return ['accounts', 'pools', 'api-keys', 'usage', 'settings'].includes(value) ? value as PageID : 'accounts'
}

export default function App() {
  const [auth, setAuth] = useState<AuthState>('checking')
  const [connectionError, setConnectionError] = useState('')
  const [page, setPage] = useState<PageID>(pageFromHash)

  const checkSession = useCallback(async () => {
    setAuth('checking')
    setConnectionError('')
    try {
      await request('/api/v1/auth/session')
      setAuth('authenticated')
    } catch (error) {
      if (error instanceof APIError && error.status === 401) {
        setAuth('anonymous')
      } else {
        setConnectionError(errorMessage(error))
        setAuth('anonymous')
      }
    }
  }, [])

  useEffect(() => { void checkSession() }, [checkSession])
  useEffect(() => {
    const onHashChange = () => setPage(pageFromHash())
    window.addEventListener('hashchange', onHashChange)
    return () => window.removeEventListener('hashchange', onHashChange)
  }, [])

  async function logout() {
    try { await request('/api/v1/auth/logout', { method: 'POST' }) } catch { /* Clear local state even if the session expired. */ } finally { setAuth('anonymous') }
  }

  if (auth === 'checking') {
    return <main className="boot-screen" aria-live="polite"><span className="brand__mark" aria-hidden="true"><i /><i /><i /></span><p>Connecting to Subpool…</p></main>
  }

  if (auth === 'anonymous') {
    return <LoginPage onSuccess={() => setAuth('authenticated')} connectionError={connectionError} onRetryConnection={() => void checkSession()} />
  }

  return (
    <Shell activePage={page} onNavigate={setPage} onLogout={() => void logout()}>
      {page === 'accounts' ? <AccountsPage /> : page === 'pools' ? <PoolsPage /> : page === 'api-keys' ? <APIKeysPage /> : page === 'usage' ? <UsagePage /> : <SettingsPage />}
    </Shell>
  )
}
